package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"teleport/internal/app"
	"teleport/internal/model"
)

const (
	googleStartPath       = "/auth/google/start"
	googleCookie          = "teleport_google"
	googleAttemptLifetime = 5 * time.Minute
)

type googleService interface {
	GoogleSignInConfig(context.Context) (model.GoogleSSOConfig, error)
	GoogleSignIn(context.Context, int64, string, string) (string, string, error)
}

type googleAttempt struct {
	browser  string
	verifier string
	expires  time.Time
	version  int64
}

type googleSSO struct {
	service  googleService
	hosts    []string
	client   *http.Client
	mu       sync.Mutex
	attempts map[string]googleAttempt
}

func registerGoogleSSO(mux *http.ServeMux, service googleService, hosts []string) {
	g := &googleSSO{
		service: service,
		hosts:   hosts,
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		attempts: make(map[string]googleAttempt),
	}
	mux.HandleFunc("GET "+googleStartPath, g.start)
	mux.HandleFunc("GET "+app.GoogleCallbackPath, g.callback)
}

func googleNonce() string {
	return rand.Text() + rand.Text()
}

func (g *googleSSO) requestOrigin(r *http.Request) string {
	scheme := "http"
	if secureRequest(r, g.hosts) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (g *googleSSO) start(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	cfg, err := g.service.GoogleSignInConfig(r.Context())
	if err != nil {
		googleLanding(w, r, "unavailable")
		return
	}
	callback, err := url.Parse(cfg.RedirectURI)
	if err != nil || !trustedHost(callback.Host, g.hosts) {
		googleLanding(w, r, "unavailable")
		return
	}
	origin := callback.Scheme + "://" + callback.Host
	if origin != g.requestOrigin(r) {
		// Only redirect to a configured HTTPS host. This also prevents a
		// bad loopback port or scheme from creating a redirect loop.
		if !configuredHost(callback.Host, g.hosts) || callback.Scheme != "https" {
			googleLanding(w, r, "unavailable")
			return
		}
		http.Redirect(w, r, origin+googleStartPath, http.StatusSeeOther)
		return
	}

	state, browser, verifier := googleNonce(), googleNonce(), googleNonce()
	now := time.Now()
	g.mu.Lock()
	for key, entry := range g.attempts {
		if !now.Before(entry.expires) {
			delete(g.attempts, key)
		}
	}
	if len(g.attempts) >= 1024 {
		g.mu.Unlock()
		googleLanding(w, r, "unavailable")
		return
	}
	g.attempts[state] = googleAttempt{browser, verifier, now.Add(googleAttemptLifetime), cfg.Version}
	g.mu.Unlock()

	http.SetCookie(w, &http.Cookie{Name: googleCookie, Value: browser, Path: app.GoogleCallbackPath, Secure: callback.Scheme == "https", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(googleAttemptLifetime.Seconds())})
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id": {cfg.ClientID}, "redirect_uri": {cfg.RedirectURI},
		"response_type": {"code"}, "scope": {"openid email"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+query.Encode(), http.StatusSeeOther)
}

func (g *googleSSO) consume(state, browser string) (googleAttempt, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	attempt, ok := g.attempts[state]
	if !ok || subtle.ConstantTimeCompare([]byte(attempt.browser), []byte(browser)) != 1 {
		return googleAttempt{}, false
	}
	delete(g.attempts, state)
	return attempt, time.Now().Before(attempt.expires)
}

func (g *googleSSO) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	cookie, err := r.Cookie(googleCookie)
	if err != nil {
		googleLanding(w, r, "failed")
		return
	}
	query := r.URL.Query()
	attempt, ok := g.consume(query.Get("state"), cookie.Value)
	if !ok {
		googleLanding(w, r, "failed")
		return
	}
	secure := secureRequest(r, g.hosts)
	http.SetCookie(w, &http.Cookie{Name: googleCookie, Path: app.GoogleCallbackPath, Secure: secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	cfg, err := g.service.GoogleSignInConfig(r.Context())
	if err != nil || cfg.Version != attempt.version || cfg.RedirectURI != g.requestOrigin(r)+app.GoogleCallbackPath {
		googleLanding(w, r, "unavailable")
		return
	}
	code := query.Get("code")
	if query.Get("error") != "" || code == "" || len(code) > 4096 {
		googleLanding(w, r, "failed")
		return
	}
	subject, email, ok := g.googleIdentity(r.Context(), cfg, code, attempt.verifier)
	if !ok {
		googleLanding(w, r, "failed")
		return
	}
	token, outcome, err := g.service.GoogleSignIn(r.Context(), attempt.version, subject, email)
	if err != nil {
		googleLanding(w, r, "failed")
		return
	}
	if token != "" {
		setSignInCookie(w, token, secure)
	}
	googleLanding(w, r, outcome)
}

func googleLanding(w http.ResponseWriter, r *http.Request, outcome string) {
	target := "/"
	if outcome != "" {
		target += "?google=" + url.QueryEscape(outcome)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (g *googleSSO) googleIdentity(ctx context.Context, cfg model.GoogleSSOConfig, code, verifier string) (string, string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	form := url.Values{
		"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "redirect_uri": {cfg.RedirectURI},
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if !g.fetchGoogle(req, &token) || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		return "", "", false
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return "", "", false
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	var identity struct {
		Subject  string `json:"sub"`
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if !g.fetchGoogle(req, &identity) || !identity.Verified || identity.Subject == "" || identity.Email == "" {
		return "", "", false
	}
	return identity.Subject, identity.Email, true
}

func (g *googleSSO) fetchGoogle(req *http.Request, target any) bool {
	response, err := g.client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	return err == nil && len(data) <= 64<<10 && json.Unmarshal(data, target) == nil
}

func init() {
	entries := []struct {
		name, action, summary string
		input, output         any
	}{
		{"getAuthCapabilities", "ACT-1301", "Read available sign-in methods", struct{}{}, app.AuthCapabilities{}},
		{"getGoogleSSO", "ACT-1313", "Read Google sign-in configuration metadata", struct{}{}, app.GoogleSSOSettings{}},
		{"setupGoogleSSO", "ACT-1313", "Save Google sign-in credentials and enrollment mode", app.GoogleSSOSetup{}, app.GoogleSSOSettings{}},
		{"enableGoogleSSO", "ACT-1313", "Enable configured Google sign-in", app.GoogleSSOVersion{}, app.GoogleSSOSettings{}},
		{"disableGoogleSSO", "ACT-1313", "Disable Google sign-in", app.GoogleSSOVersion{}, app.GoogleSSOSettings{}},
		{"listGoogleEnrollments", "ACT-1330", "List verified Google enrollment requests", struct{}{}, app.GoogleEnrollmentList{}},
		{"approveGoogleEnrollment", "ACT-1330", "Register a verified Google identity", app.GoogleEnrollmentDecision{}, app.GoogleEnrollmentView{}},
		{"grantGoogleAdministrator", "ACT-1329", "Appoint an enrolled Google user as an administrator", app.GoogleEnrollmentDecision{}, app.GoogleEnrollmentView{}},
	}
	for _, e := range entries {
		Operations = append(Operations, Operation{e.name, e.action, e.summary})
		contracts[e.name] = contract{e.input, e.output}
	}
}

func addGoogleOpenAPI(paths map[string]any) {
	for _, route := range []struct{ path, action, summary string }{
		{googleStartPath, "ACT-1302", "Start browser-bound Google sign-in"},
		{app.GoogleCallbackPath, "ACT-1303", "Complete Google sign-in or show enrollment status"},
	} {
		get := map[string]any{
			"summary": route.summary, "x-action-id": route.action,
			"responses": map[string]any{"303": map[string]string{"description": "Redirect to Google or the Teleport sign-in result"}},
		}
		if route.path == app.GoogleCallbackPath {
			params := []any{}
			for _, name := range []string{"state", "code", "error"} {
				params = append(params, map[string]any{"name": name, "in": "query", "schema": map[string]string{"type": "string"}})
			}
			get["parameters"] = params
		}
		paths[route.path] = map[string]any{"get": get}
	}
}
