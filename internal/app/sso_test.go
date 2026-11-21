package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"teleport/internal/app"
	"teleport/internal/model"
)

func TestGoogleEnrollmentPoliciesAndAdministratorGrant(t *testing.T) {
	ctx := context.Background()
	svc, admin, db := fixtureWithStore(t)
	if got := call(t, svc, "", "getAuthCapabilities", struct{}{}).(app.AuthCapabilities); got.Google {
		t.Fatal("Google sign-in unexpectedly enabled on a fresh installation")
	}
	configure := func(mode string, version int64) app.GoogleSSOSettings {
		return call(t, svc, admin, "setupGoogleSSO", app.GoogleSSOSetup{
			ExpectedVersion: version, ClientID: "fixture.apps.googleusercontent.com", ClientSecret: "fake-secret",
			RedirectURI: "https://example.test" + app.GoogleCallbackPath, Mode: mode,
		}).(app.GoogleSSOSettings)
	}
	config := configure("waitlist", 0)
	if config.Enabled || !config.Configured {
		t.Fatal("setup should store credentials without enabling Google")
	}
	raw, _ := json.Marshal(call(t, svc, admin, "getGoogleSSO", struct{}{}))
	if strings.Contains(string(raw), "fake-secret") || strings.Contains(string(raw), "clientSecret") {
		t.Fatal("Google configuration returned a secret")
	}
	config = call(t, svc, admin, "enableGoogleSSO", app.GoogleSSOVersion{ExpectedVersion: config.Version}).(app.GoogleSSOSettings)
	if got := call(t, svc, "", "getAuthCapabilities", struct{}{}).(app.AuthCapabilities); !got.Google {
		t.Fatal("anonymous sign-in capability was not published")
	}
	const email = "simon.gauld@gmail.com"
	signIn := func(subject, email, outcome string, version int64) string {
		t.Helper()
		token, got, err := svc.GoogleSignIn(ctx, version, subject, email)
		if err != nil || got != outcome || (token == "") != (outcome != "") {
			t.Fatalf("expected %q outcome, got %q, token issued %t, error %v", outcome, got, token != "", err)
		}
		return token
	}
	queue := func() []app.GoogleEnrollmentView {
		t.Helper()
		return call(t, svc, admin, "listGoogleEnrollments", struct{}{}).(app.GoogleEnrollmentList).Items
	}
	signIn("first", email, "waitlist", config.Version)
	signIn("first", email, "waitlist", config.Version)
	items := queue()
	if len(items) != 1 || items[0].Email != email || items[0].AccountID != "" {
		t.Fatalf("unexpected deduplicated waitlist: %#v", items)
	}
	first := items[0].ID
	decision := app.GoogleEnrollmentDecision{ID: first, Confirm: true}
	enrolled := call(t, svc, admin, "approveGoogleEnrollment", decision).(app.GoogleEnrollmentView)
	if enrolled.AccountID == "" || enrolled.Administrator {
		t.Fatal("approval did not produce an ordinary account")
	}
	token := signIn("first", "changed@example.test", "", config.Version)
	for _, op := range []string{"getGoogleSSO", "listGoogleEnrollments", "grantGoogleAdministrator", "disableGoogleSSO"} {
		_, err := svc.Call(ctx, token, op, []byte(`{}`))
		if err == nil || app.ErrorResponse(err).Status != 403 {
			t.Fatalf("ordinary user reached %s", op)
		}
	}
	promoted := call(t, svc, admin, "grantGoogleAdministrator", decision).(app.GoogleEnrollmentView)
	session := call(t, svc, token, "getSession", struct{}{}).(map[string]any)
	account := session["account"].(model.Account)
	if !promoted.Administrator || !account.Administrator {
		t.Fatal("explicit administrator grant did not take effect")
	}
	if account.Username != "simon.gauld" || account.ID != enrolled.AccountID {
		t.Fatal("Google username should use the enrolled address and preserve the account ID")
	}
	afterGrant, err := db.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range afterGrant.GoogleEnrollment {
		if row.ID == first && (row.AdminGrantBy != "admin" || row.AdminGrantAt.IsZero()) {
			t.Fatal("administrator grant was not attributed")
		}
	}
	config = configure("registered", config.Version)
	signIn("second", "changed@example.test", "registered", config.Version)
	items = queue()
	if len(items) != 2 || items[0].AccountID != "" {
		t.Fatal("a matching email linked the new subject to an existing account")
	}
	config = configure("auto-enrol", config.Version)
	call(t, svc, admin, "setExecutionMode", app.ExecutionModeInput{Mode: "demo"})
	signIn("second", email, "waitlist", config.Version)
	st, err := db.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	call(t, svc, admin, "setExecutionMode", app.ExecutionModeInput{Mode: "off", ExpectedVersion: st.Settings.Version})
	second := signIn("second", email, "", config.Version)
	secondAccount := call(t, svc, second, "getSession", struct{}{}).(map[string]any)["account"].(model.Account)
	if secondAccount.Administrator || secondAccount.ID == enrolled.AccountID {
		t.Fatal("auto-enrolled account inherited another account's identity or role")
	}
	if secondAccount.Username != "simon.gauld-2" {
		t.Fatalf("duplicate email local part should receive a distinct username, got %q", secondAccount.Username)
	}
	st, err = db.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	personal := 0
	for _, p := range st.Projects {
		if p.PersonalOwner == secondAccount.ID {
			personal++
		}
	}
	if personal != 1 {
		t.Fatalf("enrollment should atomically create one Personal project, got %d", personal)
	}
	if err := db.Update(ctx, func(st *model.State) (bool, error) {
		legacy := st.Accounts[account.ID]
		legacy.Username = legacy.ID
		st.Accounts[account.ID] = legacy
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	upgradedToken := signIn("first", email, "", config.Version)
	upgraded := call(t, svc, upgradedToken, "getSession", struct{}{}).(map[string]any)["account"].(model.Account)
	if upgraded.Username != "simon.gauld" || upgraded.ID != account.ID || !upgraded.Administrator {
		t.Fatal("legacy Google username update changed the account identity or administrator role")
	}
	call(t, svc, admin, "disableGoogleSSO", app.GoogleSSOVersion{ExpectedVersion: config.Version})
	if _, _, err := svc.GoogleSignIn(ctx, config.Version, "second", email); err == nil {
		t.Fatal("in-flight callback survived disablement")
	}
	if _, err := svc.Call(ctx, second, "getSession", []byte(`{}`)); err != nil {
		t.Fatal("SSO disablement revoked an independent Teleport session")
	}
	if _, err := svc.Call(ctx, "", "login", []byte(`{"username":"admin","password":"password"}`)); err != nil {
		t.Fatal("local bootstrap administrator lost password sign-in")
	}
}
