package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeDrainsInFlightRequest(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, finish := make(chan struct{}), make(chan struct{})
	draining := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-finish
		io.WriteString(w, "completed")
	})}
	srv.RegisterOnShutdown(func() { close(draining) })
	served := make(chan error, 1)
	go func() { served <- serveUntilStopped(ctx, srv, listener) }()
	response := make(chan string, 1)
	go func() {
		client := http.Client{Timeout: 3 * time.Second}
		res, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			response <- err.Error()
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		response <- string(body)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the server")
	}
	cancel()
	select {
	case <-draining:
	case <-time.After(time.Second):
		close(finish)
		t.Fatal("server did not enter shutdown")
	}
	select {
	case err := <-served:
		close(finish)
		t.Fatalf("server returned before the request drained: %v", err)
	default:
		close(finish)
	}
	if body := <-response; body != "completed" {
		t.Fatalf("in-flight response: %q", body)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
