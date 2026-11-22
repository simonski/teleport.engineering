package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"teleport/internal/app"
	"teleport/internal/clientauth"
)

func browserSignIn(ctx context.Context, api *apiClient, diagnostics io.Writer, open func(string) error) ([]byte, error) {
	target, err := url.Parse(api.endpoint)
	if err != nil || target.Hostname() == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" {
		return nil, errors.New("enter a Teleport server origin such as https://teleport.engineering")
	}
	host := target.Hostname()
	ip := net.ParseIP(host)
	if target.Scheme != "https" && (target.Scheme != "http" || host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return nil, errors.New("browser sign-in requires HTTPS or local loopback")
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("could not open the local browser sign-in callback")
	}
	defer listener.Close()
	state, verifier := clientauth.Secret(), clientauth.Secret()
	codes := make(chan string, 1)
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet || r.Host != address || r.URL.Path != clientauth.CallbackPath {
			http.NotFound(w, r)
			return
		}
		code, received := r.URL.Query().Get("code"), r.URL.Query().Get("state")
		if !clientauth.Valid(code) || subtle.ConstantTimeCompare([]byte(received), []byte(state)) != 1 {
			http.Error(w, "This callback does not match the active Teleport sign-in.", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Authorization received. Return to Teleport to check sign-in.")
		select {
		case codes <- code:
		default:
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, MaxHeaderBytes: 8192}
	go server.Serve(listener)
	defer server.Close()

	query := url.Values{clientauth.PortField: {port}, clientauth.StateField: {state}, clientauth.ChallengeField: {clientauth.Challenge(verifier)}}
	target.Path, target.RawQuery = "/", query.Encode()
	fmt.Fprintln(diagnostics, "Approve this sign-in in the Teleport browser window.")
	if err := open(target.String()); err != nil {
		fmt.Fprintln(diagnostics, "Open this address in your browser:", target.String())
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case code := <-codes:
		// The selected origin is fixed for the exchange. Its TLS connection
		// carries the verifier, and redirects are refused by the API client.
		payload, _ := json.Marshal(app.ClientExchangeInput{Code: code, Verifier: verifier})
		return api.operationContext(ctx, "exchangeClientAuthorization", payload)
	case <-ctx.Done():
		return nil, errors.New("browser sign-in cancelled or expired")
	}
}

func openSignInBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("/usr/bin/open", target).Run()
	case "linux":
		return exec.Command("xdg-open", target).Run()
	default:
		return errors.New("open the browser manually")
	}
}
