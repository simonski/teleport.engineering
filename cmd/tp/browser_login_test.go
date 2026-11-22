package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"teleport/internal/app"
	"teleport/internal/clientauth"
	"teleport/internal/version"
)

func TestBrowserSignInRejectsMismatchedLoopbackState(t *testing.T) {
	challenge := make(chan string, 1)
	code := clientauth.Secret()
	tp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req app.ClientExchangeInput
		if r.URL.Path != "/api/tools/exchangeClientAuthorization" ||
			json.NewDecoder(r.Body).Decode(&req) != nil || req.Code != code ||
			clientauth.Challenge(req.Verifier) != <-challenge {
			http.Error(w, "invalid request", 400)
			return
		}
		w.Header().Set(version.Header, version.CLI)
		io.WriteString(w, `{"account":{"username":"pilot"},"token":"fixture"}`)
	}))
	defer tp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := apiClient{endpoint: tp.URL, diagnostics: io.Discard}
	body, err := browserSignIn(ctx, &client, io.Discard, func(address string) error {
		landing, err := url.Parse(address)
		if err != nil {
			return err
		}
		desktop := clientauth.Continuation(landing.Query())
		if desktop == nil {
			t.Fatal("invalid desktop browser URL")
		}
		challenge <- desktop.Get(clientauth.ChallengeField)
		navigate := func(state string) int {
			values := url.Values{"code": {code}, "state": {state}}
			callback := "http://127.0.0.1:" + desktop.Get(clientauth.PortField) + clientauth.CallbackPath + "?" + values.Encode()
			response, err := http.Get(callback)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			return response.StatusCode
		}
		if got := navigate(clientauth.Secret()); got != http.StatusBadRequest {
			t.Fatalf("unbound callback status: %d", got)
		}
		if got := navigate(desktop.Get(clientauth.StateField)); got != http.StatusOK {
			return fmt.Errorf("callback status: %d", got)
		}
		return nil
	})
	var login app.LoginResult
	if err != nil || json.Unmarshal(body, &login) != nil || login.Account.Username != "pilot" {
		t.Fatalf("failed to exchange the authorized callback: %s (%v)", body, err)
	}
}
