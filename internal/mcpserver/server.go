// Package mcpserver serves the cleaned remote index to authenticated MCP clients.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/remote"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/transcript"
)

type Config struct{ Issuer, AllowedUserID, ResourceURL string }

type Backend interface {
	Search(context.Context, string, string, string, string, int) ([]remote.Hit, error)
	Show(context.Context, string, string) ([]model.Session, error)
}
type RemoteBackend struct{}

func (RemoteBackend) Search(ctx context.Context, q, s, p, c string, l int) ([]remote.Hit, error) {
	return remote.Search(ctx, q, s, p, c, l)
}
func (RemoteBackend) Show(ctx context.Context, s, id string) ([]model.Session, error) {
	return remote.Show(ctx, s, id)
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")))
}
func (c Config) validate() error {
	if !validURL(c.Issuer) {
		return errors.New("AUTHKIT_ISSUER must be an HTTPS URL")
	}
	if !validURL(c.ResourceURL) {
		return errors.New("MCP_RESOURCE_URL must be an HTTPS URL")
	}
	u, _ := url.Parse(c.ResourceURL)
	if u.Path != "/mcp" {
		return errors.New("MCP_RESOURCE_URL must have path /mcp")
	}
	if strings.TrimSpace(c.AllowedUserID) == "" {
		return errors.New("WORKOS_ALLOWED_USER_ID is required")
	}
	return nil
}

// NewHandler retains no access tokens. ctx controls the JWKS refresh lifecycle.
func NewHandler(ctx context.Context, c Config, backend Backend) (http.Handler, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, errors.New("MCP backend is required")
	}
	v := &verifier{ctx: ctx, config: c, client: &http.Client{Timeout: 10 * time.Second}}
	server := mcp.NewServer(&mcp.Implementation{Name: "sessions", Version: "1.0.0"}, nil)
	addTools(server, backend)
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	resource, _ := url.Parse(c.ResourceURL)
	metadataURL := resource.Scheme + "://" + resource.Host + "/.well-known/oauth-protected-resource"
	protected := auth.RequireBearerToken(v.verify, &auth.RequireBearerTokenOptions{ResourceMetadataURL: metadataURL})(transport)
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK supplies discovery but omits the OAuth error parameter.
		protected.ServeHTTP(&challengeWriter{ResponseWriter: w, metadataURL: metadataURL}, r)
	}))
	metadata := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{Resource: c.ResourceURL, AuthorizationServers: []string{c.Issuer}, BearerMethodsSupported: []string{"header"}})
	mux.Handle("/.well-known/oauth-protected-resource", metadata)
	mux.Handle("/.well-known/oauth-protected-resource/mcp", metadata)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK); fmt.Fprintln(w, "ok") })
	return http.MaxBytesHandler(mux, 1<<20), nil
}

type challengeWriter struct {
	http.ResponseWriter
	metadataURL string
}

func (w *challengeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *challengeWriter) WriteHeader(code int) {
	if code == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+w.metadataURL+`"`)
	} else {
		w.Header().Del("WWW-Authenticate")
	}
	w.ResponseWriter.WriteHeader(code)
}

type verifier struct {
	ctx    context.Context
	config Config
	client *http.Client
	mu     sync.Mutex
	keys   keyfunc.Keyfunc
}

func (v *verifier) loadKeys(ctx context.Context) (keyfunc.Keyfunc, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys != nil {
		return v.keys, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(v.config.Issuer, "/")+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("authorization metadata unavailable")
	}
	var doc struct {
		Issuer string `json:"issuer"`
		JWKS   string `json:"jwks_uri"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, err
	}
	if doc.Issuer != v.config.Issuer {
		return nil, errors.New("authorization issuer mismatch")
	}
	if doc.JWKS == "" {
		doc.JWKS = strings.TrimRight(v.config.Issuer, "/") + "/oauth2/jwks"
	}
	if !validURL(doc.JWKS) {
		return nil, errors.New("invalid JWKS URL")
	}
	keys, err := keyfunc.NewDefaultOverrideCtx(v.ctx, []string{doc.JWKS}, keyfunc.Override{Client: v.client, HTTPTimeout: 10 * time.Second, RateLimitWaitMax: time.Second, RefreshErrorHandlerFunc: func(string) func(context.Context, error) { return func(context.Context, error) {} }})
	if err != nil {
		return nil, err
	}
	v.keys = keys
	return keys, nil
}
func (v *verifier) verify(ctx context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
	keys, err := v.loadKeys(ctx)
	if err != nil {
		return nil, auth.ErrInvalidToken
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, keys.KeyfuncCtx(ctx), jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512", "EdDSA"}), jwt.WithIssuer(v.config.Issuer), jwt.WithAudience(v.config.ResourceURL), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || claims.Subject != v.config.AllowedUserID || claims.ExpiresAt == nil {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{UserID: claims.Subject, Expiration: claims.ExpiresAt.Time}, nil
}

type searchInput struct {
	Query     string `json:"query" jsonschema:"Search words in the cleaned remote index"`
	Source    string `json:"source,omitempty" jsonschema:"Optional literal source: cursor, claude, codex, t3code"`
	Provider  string `json:"provider,omitempty" jsonschema:"Optional agent provider slug, such as cursor, claude, codex, grok, opencode"`
	AllCopies bool   `json:"all_copies,omitempty" jsonschema:"Accepted for CLI compatibility; remote search already returns all copies"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum results; default 20"`
}
type showInput struct {
	ID string `json:"id" jsonschema:"Session id, source:id, or path locator"`
}

func result(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
func addTools(server *mcp.Server, b Backend) {
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(server, &mcp.Tool{Name: "search", Description: "Search cleaned agent chats in the remote index. Remote search returns all copies.", Annotations: annotations}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, nil, errors.New("query is required")
		}
		if in.Source != "" && !source.ValidSource(in.Source) {
			return nil, nil, errors.New("invalid source")
		}
		if in.Provider != "" {
			p, ok := source.ParseProvider(in.Provider)
			if !ok {
				return nil, nil, errors.New("invalid provider")
			}
			in.Provider = p
		}
		if in.Limit <= 0 {
			in.Limit = 20
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		hits, err := b.Search(ctx, in.Query, in.Source, in.Provider, "", in.Limit)
		if err != nil {
			return nil, nil, errors.New("remote search unavailable")
		}
		if len(hits) == 0 {
			return result("no matches"), nil, nil
		}
		var out strings.Builder
		for _, h := range hits {
			date := ""
			if h.StartedAt != nil {
				date = *h.StartedAt
			}
			if date == "" && h.UpdatedAt != nil {
				date = *h.UpdatedAt
			}
			cwd := ""
			if h.CWD != nil {
				cwd = *h.CWD
			}
			fmt.Fprintf(&out, "%s\t%s\t%s\t%s\n", h.ID, h.Source, date, cwd)
			if h.Provider != "" && h.Source != h.Provider {
				fmt.Fprintln(&out, "  provider: "+h.Provider)
			}
			if h.Title != "" {
				fmt.Fprintln(&out, "  title: "+h.Title)
			}
			fmt.Fprintln(&out, "  snippet: "+strings.Join(strings.Fields(h.Snippet), " "))
			fmt.Fprintln(&out, "  path: "+h.Path)
		}
		return result(out.String()), nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "show", Description: "Show cleaned turns from a remote chat.", Annotations: annotations}, func(ctx context.Context, _ *mcp.CallToolRequest, in showInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, nil, errors.New("id is required")
		}
		src, id := remote.ParseLocator(in.ID)
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		rows, err := b.Show(ctx, src, id)
		if err != nil {
			return nil, nil, errors.New("remote show unavailable")
		}
		if len(rows) == 0 {
			return nil, nil, fmt.Errorf("not found: %s", in.ID)
		}
		if len(rows) > 1 {
			var out strings.Builder
			fmt.Fprintf(&out, "multiple sessions match %s; use source:id or the path locator\n", in.ID)
			for _, r := range rows {
				fmt.Fprintf(&out, "%s:%s\t%s\n", r.Source, r.ID, r.Path)
			}
			return nil, nil, errors.New(out.String())
		}
		return result(transcript.Format(&rows[0], 200000)), nil, nil
	})
}

func Run() error {
	for _, name := range []string{"AUTHKIT_ISSUER", "WORKOS_ALLOWED_USER_ID", "MCP_RESOURCE_URL", "DATABASE_URL_READONLY"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("PORT must be between 1 and 65535")
	}
	host := os.Getenv("BIND_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	handler, err := NewHandler(ctx, Config{Issuer: os.Getenv("AUTHKIT_ISSUER"), AllowedUserID: os.Getenv("WORKOS_ALLOWED_USER_ID"), ResourceURL: os.Getenv("MCP_RESOURCE_URL")}, RemoteBackend{})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: net.JoinHostPort(host, port), Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}

	return serveUntilCanceled(ctx, server, server.ListenAndServe, 10*time.Second)
}

// serveUntilCanceled waits for active handlers to drain before returning to the
// CLI, whose main function exits the process immediately after Run returns.
func serveUntilCanceled(ctx context.Context, server *http.Server, serve func() error, grace time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- serve() }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = server.Close()
		}
		serveErr := <-served
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(serveErr, shutdownErr)
	}
}
