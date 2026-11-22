package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"teleport/internal/app"
	"teleport/internal/clientauth"
	"teleport/internal/model"
)

type googleTestService struct {
	cfg      model.GoogleSSOConfig
	signedIn int
	outcome  string
}

func (s *googleTestService) GoogleSignInConfig(context.Context) (model.GoogleSSOConfig, error) {
	return s.cfg, nil
}

func (s *googleTestService) GoogleSignIn(_ context.Context, version int64, subject, email string) (string, string, error) {
	if version != s.cfg.Version || subject != "stable-subject" || email != "test@example.test" {
		panic("unexpected verified identity")
	}
	s.signedIn++
	if s.outcome != "" {
		return "", s.outcome, nil
	}
	return "new-teleport-session", "", nil
}

type googleTestTransport func(*http.Request) (*http.Response, error)

func (f googleTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGoogleSSOCallback(t *testing.T) {
	for _, testName := range []string{"admitted", "waitlist", "registered", "unverified", "changed", "desktop"} {
		outcome := testName
		name := outcome
		if name == "admitted" || name == "desktop" {
			outcome = ""
		}
		t.Run(name, func(t *testing.T) {
			const origin = "https://workspace.example.test"
			svc := &googleTestService{
				cfg:     model.GoogleSSOConfig{Version: 2, ClientID: "fixture", ClientSecret: "fixture-secret", RedirectURI: origin + app.GoogleCallbackPath, Enabled: true},
				outcome: outcome,
			}
			var challenge string
			var requests int
			client := &http.Client{Transport: googleTestTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				var body string
				switch req.URL.String() {
				case "https://oauth2.googleapis.com/token":
					if req.Method != http.MethodPost || req.ParseForm() != nil || req.Form.Get("client_secret") != svc.cfg.ClientSecret || req.Form.Get("code") != "one-time-code" || req.Form.Get("redirect_uri") != svc.cfg.RedirectURI {
						t.Fatalf("unexpected Google token request")
					}
					sum := sha256.Sum256([]byte(req.Form.Get("code_verifier")))
					if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
						t.Fatal("PKCE challenge does not match the verifier")
					}
					body = `{"access_token":"fixture-access","token_type":"Bearer"}`
				case "https://openidconnect.googleapis.com/v1/userinfo":
					if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer fixture-access" {
						t.Fatal("expected a bearer-authenticated UserInfo request")
					}
					body = `{"sub":"stable-subject","email":"test@example.test","email_verified":true}`
					if outcome == "unverified" {
						body = `{"sub":"stable-subject","email":"test@example.test","email_verified":false}`
					}
				default:
					t.Fatalf("unexpected provider location: %s", req.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			g := &googleSSO{service: svc, hosts: []string{"workspace.example.test"}, client: client, attempts: map[string]googleAttempt{}}
			var desktop url.Values
			startURL := origin + googleStartPath
			if name == "desktop" {
				desktop = url.Values{
					clientauth.PortField: {"32123"}, clientauth.StateField: {clientauth.Secret()},
					clientauth.ChallengeField: {clientauth.Challenge(clientauth.Secret())},
				}
				startURL += "?" + desktop.Encode()
			}
			start := httptest.NewRecorder()
			g.start(start, httptest.NewRequest(http.MethodGet, startURL, nil))
			target, err := url.Parse(start.Header().Get("Location"))
			if err != nil || target.Host != "accounts.google.com" || target.Query().Get("code_challenge_method") != "S256" {
				t.Fatalf("unexpected authorization redirect: %v", target)
			}
			if target.Query().Has(clientauth.PortField) {
				t.Fatal("desktop continuation leaked to Google")
			}
			state := target.Query().Get("state")
			challenge = target.Query().Get("code_challenge")
			cookies := start.Result().Cookies()
			if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Value == state {
				t.Fatalf("missing independent browser binding")
			}
			callback := func(cookie *http.Cookie) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, origin+app.GoogleCallbackPath+"?state="+url.QueryEscape(state)+"&code=one-time-code", nil)
				req.AddCookie(cookie)
				result := httptest.NewRecorder()
				g.callback(result, req)
				return result
			}
			bad := callback(&http.Cookie{Name: googleCookie, Value: "wrong-browser"})
			if bad.Header().Get("Location") != "/?google=failed" || requests != 0 {
				t.Fatal("callback accepted the wrong browser")
			}
			if outcome == "changed" {
				svc.cfg.Version++
			}
			got := callback(cookies[0])
			want, calls := "/", 1
			if desktop != nil {
				want += "?" + desktop.Encode()
			}
			switch outcome {
			case "waitlist", "registered":
				want = "/?google=" + outcome
			case "unverified":
				want, calls = "/?google=failed", 0
			case "changed":
				want, calls = "/?google=unavailable", 0
			}
			if got.Header().Get("Location") != want || svc.signedIn != calls {
				t.Fatalf("unexpected result %q, sign-ins %d", got.Header().Get("Location"), svc.signedIn)
			}
			session := false
			for _, cookie := range got.Result().Cookies() {
				if cookie.Name == "teleport_session" {
					session = cookie.Value == "new-teleport-session" && cookie.HttpOnly && cookie.Secure
				}
			}
			if session != (outcome == "") {
				t.Fatal("session cookie does not match the admission outcome")
			}
			requestCount := requests
			repeat := callback(cookies[0])
			if repeat.Header().Get("Location") != "/?google=failed" || requests != requestCount {
				t.Fatal("consumed callback could be replayed")
			}
		})
	}
}
