package mcpserver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownDrainsActiveRequestBeforeReturning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, "finished")
		close(handlerDone)
	})}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		returned <- serveUntilCanceled(ctx, server, func() error { return server.Serve(listener) }, 5*time.Second)
	}()
	response := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			response <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			response <- err.Error()
			return
		}
		response <- string(body)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	// Listener closure proves Shutdown has started while the handler is still active.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), 50*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not close listener")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-returned:
		t.Fatalf("returned before draining handler: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-handlerDone:
	default:
		t.Fatal("returned before handler completed")
	}
	select {
	case body := <-response:
		if body != "finished" {
			t.Fatalf("response %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("response was not drained")
	}
}

func TestShutdownDeadlineClosesActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered := make(chan struct{})
	handlerDone := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(handlerDone) })}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		returned <- serveUntilCanceled(ctx, server, func() error { return server.Serve(listener) }, 50*time.Millisecond)
	}()
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown exceeded its deadline")
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out request was not closed")
	}
}
