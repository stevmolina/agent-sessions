// Package mcpserver serves the public Agent Sessions pages behind `sessions mcp`.
// The MCP endpoint it used to serve moved into Agent Brain, which reads the
// same sessions table and shows each person only their own rows. /mcp and the
// OAuth discovery paths now answer 410 Gone with the new address, so a client
// that still points here learns where to go instead of failing to sign in.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// MovedURL is where the sessions tools live now.
const MovedURL = "https://brain.mcpbox.dev/mcp"

var movedBody = func() []byte {
	b, _ := json.Marshal(map[string]string{
		"error":   "moved",
		"message": "The sessions MCP moved into Agent Brain. Use " + MovedURL + " (tools search_sessions, show_session).",
		"url":     MovedURL,
	})
	return append(b, '\n')
}()

func moved(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	_, _ = w.Write(movedBody)
}

// isMoved matches /mcp, anything under it, and every OAuth protected-resource
// metadata path, for any method.
func isMoved(path string) bool {
	return path == "/mcp" || strings.HasPrefix(path, "/mcp/") ||
		strings.HasPrefix(path, "/.well-known/oauth-protected-resource")
}

// NewHandler serves the public pages, /healthz, and the 410 answers. It reads
// no database and needs no secrets.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK); fmt.Fprintln(w, "ok") })
	// Exact public paths. GET also serves HEAD.
	registerPublic(mux)
	return http.MaxBytesHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMoved(r.URL.Path) {
			moved(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}), 1<<20)
}

// Run serves until SIGINT or SIGTERM. Only PORT (default 8080) and BIND_HOST
// (default 0.0.0.0) are read.
func Run() error {
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
	server := &http.Server{Addr: net.JoinHostPort(host, port), Handler: NewHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
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
