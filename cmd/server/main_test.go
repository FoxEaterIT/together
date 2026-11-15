package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestServeDrainsAndStopsBackgroundWork(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, server, func() { close(stopped) }, nil, 5*time.Second)
	}()

	url := "http://" + listener.Addr().String() + "/"
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became reachable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("background work was not stopped")
	}
	if conn, err := net.DialTimeout("tcp", listener.Addr().String(), 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener still accepts connections after shutdown")
	}
}

// TestServeClosesStreamsBeforeDrain covers the failure mode of a long-lived
// SSE handler: http.Server.Shutdown does not cancel request contexts, so
// without an explicit close before the drain the process would sit for the
// whole timeout.
func TestServeClosesStreamsBeforeDrain(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	var once sync.Once
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-closed
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, server, nil, func() {
			once.Do(func() { close(closed) })
		}, 5*time.Second)
	}()

	response, err := http.Get("http://" + listener.Addr().String() + "/stream")
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer response.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not close the open stream before draining")
	}
}
