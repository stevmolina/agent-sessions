package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/remote"
)

type fakeBackend struct {
	query, src, provider, cwd, id string
	limit                         int
	rows                          []model.Session
	hits                          []remote.Hit
	called                        bool
}

func (b *fakeBackend) Search(ctx context.Context, q, s, p, c string, l int) ([]remote.Hit, error) {
	b.called = true
	b.query = q
	b.src = s
	b.provider = p
	b.cwd = c
	b.limit = l
	if _, ok := ctx.Deadline(); !ok {
		panic("missing deadline")
	}
	return b.hits, nil
}
func (b *fakeBackend) Show(ctx context.Context, s, id string) ([]model.Session, error) {
	b.called = true
	b.src = s
	b.id = id
	if _, ok := ctx.Deadline(); !ok {
		panic("missing deadline")
	}
	return b.rows, nil
}

type fixture struct {
	server  *httptest.Server
	issuer  string
	key     *rsa.PrivateKey
	claims  jwt.RegisteredClaims
	backend *fakeBackend
}

func newFixture(t *testing.T, metadataMode string) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{key: key, backend: &fakeBackend{}}
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			if metadataMode == "failure" {
				http.Error(w, "unavailable", 503)
				return
			}
			issuer := f.issuer
			if metadataMode == "mismatch" {
				issuer += "/other"
			}
			doc := map[string]string{"issuer": issuer}
			if metadataMode != "fallback" {
				doc["jwks_uri"] = f.issuer + "/oauth2/jwks"
			}
			json.NewEncoder(w).Encode(doc)
		case "/oauth2/jwks":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		default:
			http.NotFound(w, r)
		}
	}))
	f.issuer = authority.URL
	t.Cleanup(authority.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := NewHandler(ctx, Config{Issuer: f.issuer, AllowedUserID: "test-subject", ResourceURL: "https://example.test/mcp"}, f.backend)
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(h)
	t.Cleanup(f.server.Close)
	f.claims = jwt.RegisteredClaims{Issuer: f.issuer, Subject: "test-subject", Audience: jwt.ClaimStrings{"https://example.test/mcp"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	return f
}
func (f *fixture) token(t *testing.T, claims jwt.RegisteredClaims, key *rsa.PrivateKey) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func (f *fixture) request(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	method := http.MethodGet
	var reader io.Reader
	if body != "" {
		method = http.MethodPost
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
func TestBearerVerification(t *testing.T) {
	f := newFixture(t, "")
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		change  func(*jwt.RegisteredClaims)
		key     *rsa.PrivateKey
		missing bool
		want    int
	}{
		{name: "valid", want: 200}, {name: "wrong issuer", change: func(c *jwt.RegisteredClaims) { c.Issuer = "https://wrong.test" }, want: 401},
		{name: "wrong audience", change: func(c *jwt.RegisteredClaims) { c.Audience = jwt.ClaimStrings{"https://wrong.test/mcp"} }, want: 401},
		{name: "other user", change: func(c *jwt.RegisteredClaims) { c.Subject = "other-test-subject" }, want: 401},
		{name: "expired", change: func(c *jwt.RegisteredClaims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour)) }, want: 401},
		{name: "missing expiry", change: func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil }, want: 401}, {name: "missing token", missing: true, want: 401}, {name: "bad signature", key: other, want: 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := f.claims
			if tc.change != nil {
				tc.change(&claims)
			}
			key := f.key
			if tc.key != nil {
				key = tc.key
			}
			token := ""
			if !tc.missing {
				token = f.token(t, claims, key)
			}
			resp := f.request(t, "/mcp", token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
			if tc.want == 401 {
				challenge := resp.Header.Get("WWW-Authenticate")
				if !strings.Contains(challenge, `error="invalid_token"`) || !strings.Contains(challenge, "https://example.test/.well-known/oauth-protected-resource") {
					t.Fatalf("bad challenge %q", challenge)
				}
			}
		})
	}
}
func TestMetadataFailuresAreUnauthorized(t *testing.T) {
	for _, mode := range []string{"failure", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			resp := f.request(t, "/mcp", f.token(t, f.claims, f.key), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			if resp.StatusCode != 401 {
				t.Fatalf("status %d", resp.StatusCode)
			}
		})
	}
}
func TestHealthAndDiscovery(t *testing.T) {
	f := newFixture(t, "")
	for _, path := range []string{"/healthz", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		resp := f.request(t, path, "", "")
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
		if path != "/healthz" {
			var doc map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
				t.Fatal(err)
			}
			if doc["resource"] != "https://example.test/mcp" || doc["authorization_servers"].([]any)[0] != f.issuer {
				t.Fatalf("metadata %v", doc)
			}
		}
	}
}
func (f *fixture) call(t *testing.T, name string, args any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	resp := f.request(t, "/mcp", f.token(t, f.claims, f.key), string(body))
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var reply map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func toolText(reply map[string]any) string {
	result, _ := reply["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	return content[0].(map[string]any)["text"].(string)
}
func TestHTTPTools(t *testing.T) {
	f := newFixture(t, "fallback")
	f.backend.hits = []remote.Hit{{ID: "session", Source: "t3code", Provider: "codex", Snippet: "matched  words", Path: "t3code://session"}}
	reply := f.call(t, "search", map[string]any{"query": "words", "source": "t3code", "provider": "codex", "all_copies": true, "limit": 7})
	if f.backend.query != "words" || f.backend.src != "t3code" || f.backend.provider != "codex" || f.backend.limit != 7 || f.backend.cwd != "" {
		t.Fatalf("backend %v", f.backend)
	}
	if !strings.Contains(toolText(reply), "snippet: matched words") {
		t.Fatalf("reply %v", reply)
	}
	f.backend.hits = nil
	reply = f.call(t, "search", map[string]any{"query": "words"})
	if toolText(reply) != "no matches" || f.backend.limit != 20 {
		t.Fatalf("reply %v", reply)
	}
	f.backend.rows = []model.Session{{ID: "session", Source: "t3code", Turns: []model.Turn{{Role: "user", Text: "cleaned human text"}, {Role: "assistant", Text: "assistant text"}}}}
	reply = f.call(t, "show", map[string]any{"id": "t3code:session"})
	if f.backend.src != "t3code" || f.backend.id != "session" || !strings.Contains(toolText(reply), "cleaned human text") {
		t.Fatalf("reply %v", reply)
	}
	f.backend.rows = append(f.backend.rows, model.Session{ID: "session", Source: "codex"})
	reply = f.call(t, "show", map[string]any{"id": "session"})
	if !strings.Contains(toolText(reply), "multiple sessions match") || reply["result"].(map[string]any)["isError"] != true {
		t.Fatalf("reply %v", reply)
	}
	f.backend.rows = nil
	reply = f.call(t, "show", map[string]any{"id": "missing"})
	if !strings.Contains(toolText(reply), "not found") {
		t.Fatalf("reply %v", reply)
	}
	for _, args := range []map[string]any{{"query": ""}, {"query": "words", "source": "bad"}, {"query": "words", "provider": "bad provider"}} {
		f.backend.called = false
		reply = f.call(t, "search", args)
		if f.backend.called || reply["result"].(map[string]any)["isError"] != true {
			t.Fatalf("invalid args accepted: %v", reply)
		}
	}
}
func TestRequestBodyLimit(t *testing.T) {
	f := newFixture(t, "")
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "search", "arguments": map[string]any{"query": strings.Repeat("x", 1<<20)}}})
	if err != nil {
		t.Fatal(err)
	}
	resp := f.request(t, "/mcp", f.token(t, f.claims, f.key), string(body))
	if resp.StatusCode != http.StatusRequestEntityTooLarge || f.backend.called {
		t.Fatalf("oversized body status %d, backend called %v", resp.StatusCode, f.backend.called)
	}
}
func TestConfigValidation(t *testing.T) {
	for _, c := range []Config{{}, {Issuer: "http://untrusted.test", AllowedUserID: "test", ResourceURL: "https://example.test/mcp"}, {Issuer: "https://issuer.test", AllowedUserID: "test", ResourceURL: "https://example.test/other"}} {
		if _, err := NewHandler(context.Background(), c, &fakeBackend{}); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
