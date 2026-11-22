package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"teleport/internal/app"
	"teleport/internal/clientauth"
	"teleport/internal/server"
	"teleport/internal/teststore"
)

func TestDesktopAuthorizationUsesSameOriginBrowserCookie(t *testing.T) {
	db := teststore.Open(t)
	if _, err := app.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	handler := server.New(app.New(db), "test")
	post := func(operation, body, origin string, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://localhost/api/tools/"+operation, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	login := post("login", `{"username":"admin","password":"password"}`, "http://localhost", nil, "")
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatal("fixture browser login did not establish a session")
	}
	cookie := login.Result().Cookies()[0]
	verifier := clientauth.Secret()
	input, _ := json.Marshal(app.ClientAuthorizeInput{Challenge: clientauth.Challenge(verifier), AccountID: "admin"})
	for _, origin := range []string{"", "https://untrusted.example"} {
		if got := post("authorizeClient", string(input), origin, cookie, "").Code; got != http.StatusForbidden {
			t.Fatalf("origin %q returned status %d", origin, got)
		}
	}
	if got := post("authorizeClient", string(input), "http://localhost", nil, cookie.Value).Code; got != http.StatusForbidden {
		t.Fatalf("bearer-only authorization returned status %d", got)
	}
	approval := post("authorizeClient", string(input), "http://localhost", cookie, "")
	var code app.ClientAuthorizationCode
	if approval.Code != http.StatusOK || json.Unmarshal(approval.Body.Bytes(), &code) != nil || code.Code == "" {
		t.Fatalf("approved browser authorization returned %d", approval.Code)
	}
	input, _ = json.Marshal(app.ClientExchangeInput{Code: code.Code, Verifier: verifier})
	exchange := post("exchangeClientAuthorization", string(input), "", nil, "")
	var desktop app.LoginResult
	if exchange.Code != http.StatusOK || json.Unmarshal(exchange.Body.Bytes(), &desktop) != nil ||
		desktop.Token == "" || desktop.Token == cookie.Value || desktop.Account.ID != "admin" || len(exchange.Result().Cookies()) != 0 {
		t.Fatal("exchange must return a separate account token to the CLI only")
	}
}
