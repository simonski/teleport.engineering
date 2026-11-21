package app

import (
	"context"
	"encoding/json"
	"net"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"teleport/internal/model"
)

const GoogleCallbackPath = "/auth/google/callback"

type GoogleSSOSettings struct {
	Version     int64  `json:"version"`
	Enabled     bool   `json:"enabled"`
	Configured  bool   `json:"configured"`
	ClientID    string `json:"clientId"`
	RedirectURI string `json:"redirectUri"`
	Mode        string `json:"mode"`
}

type GoogleSSOSetup struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	ClientID        string `json:"clientId"`
	ClientSecret    string `json:"clientSecret,omitempty"`
	RedirectURI     string `json:"redirectUri"`
	Mode            string `json:"mode"`
}

type GoogleSSOVersion struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type GoogleEnrollmentDecision struct {
	ID      string `json:"id"`
	Confirm bool   `json:"confirm"`
}

type GoogleEnrollmentView struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	AccountID     string    `json:"accountId,omitempty"`
	RequestedAt   time.Time `json:"requestedAt"`
	Administrator bool      `json:"administrator"`
	Suspended     bool      `json:"suspended"`
}

type GoogleEnrollmentList struct {
	Items []GoogleEnrollmentView `json:"items"`
}

type AuthCapabilities struct {
	Google bool `json:"google"`
}

func publicGoogleSSO(c model.GoogleSSOConfig) GoogleSSOSettings {
	mode := c.Mode
	if mode == "" {
		mode = "registered"
	}
	return GoogleSSOSettings{c.Version, c.Enabled, c.ClientSecret != "" && c.ClientID != "" && c.RedirectURI != "", c.ClientID, c.RedirectURI, mode}
}

func (s *Service) GoogleSignInConfig(ctx context.Context) (model.GoogleSSOConfig, error) {
	st, err := s.db.Read(ctx)
	if err != nil {
		return model.GoogleSSOConfig{}, err
	}
	c := st.GoogleSSO
	if !c.Enabled || !publicGoogleSSO(c).Configured {
		return model.GoogleSSOConfig{}, failure(403, "sso_disabled", "Google sign-in is disabled")
	}
	return c, nil
}

func validGoogleRedirect(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Path != GoogleCallbackPath ||
		u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}

func validGoogleCredential(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	})
}

func googleEnrollmentView(st *model.State, row model.GoogleEnrollment) GoogleEnrollmentView {
	a := st.Accounts[row.AccountID]
	return GoogleEnrollmentView{
		ID: row.ID, Email: row.Email, AccountID: row.AccountID, RequestedAt: row.RequestedAt,
		Administrator: a.Administrator, Suspended: a.Suspended,
	}
}

func googleAdminCall(st *model.State, a model.Account, operation string, raw json.RawMessage) (any, bool, error) {
	if !a.Administrator || a.ServiceRunID != "" || a.ServiceSessionID != "" {
		return nil, false, failure(403, "forbidden", "Google sign-in administration requires a human installation administrator")
	}
	c := st.GoogleSSO
	switch operation {
	case "getGoogleSSO", "listGoogleEnrollments":
		if err := parse(raw, &struct{}{}); err != nil {
			return nil, false, err
		}
		if operation == "getGoogleSSO" {
			return publicGoogleSSO(c), false, nil
		}
		items := make([]GoogleEnrollmentView, 0, len(st.GoogleEnrollment))
		for _, row := range st.GoogleEnrollment {
			items = append(items, googleEnrollmentView(st, row))
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].RequestedAt.Equal(items[j].RequestedAt) {
				return items[i].ID < items[j].ID
			}
			return items[i].RequestedAt.After(items[j].RequestedAt)
		})
		return GoogleEnrollmentList{items}, false, nil
	case "setupGoogleSSO":
		var in GoogleSSOSetup
		if err := parse(raw, &in); err != nil {
			return nil, false, err
		}
		if in.ExpectedVersion != c.Version {
			return nil, false, failure(409, "version_conflict", "Google sign-in settings changed")
		}
		if !validGoogleCredential(in.ClientID, 512) || !strings.HasSuffix(in.ClientID, ".apps.googleusercontent.com") ||
			!validGoogleRedirect(in.RedirectURI) || len(in.RedirectURI) > 2048 ||
			!contains([]string{"auto-enrol", "waitlist", "registered"}, in.Mode) {
			return nil, false, failure(422, "invalid_sso_config", "Supply a Google OAuth client ID, a valid callback URL and an enrollment mode")
		}
		if in.ClientSecret == "" && in.ClientID == c.ClientID {
			in.ClientSecret = c.ClientSecret
		}
		if !validGoogleCredential(in.ClientSecret, 1024) {
			return nil, false, failure(422, "invalid_sso_config", "Supply the OAuth client secret for this client ID")
		}
		c.ClientID, c.ClientSecret, c.RedirectURI, c.Mode = in.ClientID, in.ClientSecret, in.RedirectURI, in.Mode
	case "enableGoogleSSO", "disableGoogleSSO":
		var in GoogleSSOVersion
		if err := parse(raw, &in); err != nil {
			return nil, false, err
		}
		if in.ExpectedVersion != c.Version {
			return nil, false, failure(409, "version_conflict", "Google sign-in settings changed")
		}
		enable := operation == "enableGoogleSSO"
		if enable && !publicGoogleSSO(c).Configured {
			return nil, false, failure(409, "sso_unconfigured", "Set up Google sign-in before enabling it")
		}
		if enable == c.Enabled {
			return publicGoogleSSO(c), false, nil
		}
		c.Enabled = enable
	case "approveGoogleEnrollment", "grantGoogleAdministrator":
		var in GoogleEnrollmentDecision
		if err := parse(raw, &in); err != nil {
			return nil, false, err
		}
		if !in.Confirm {
			return nil, false, failure(422, "confirmation_required", "Confirm this account access change")
		}
		for key, row := range st.GoogleEnrollment {
			if row.ID != in.ID {
				continue
			}
			if operation == "approveGoogleEnrollment" && row.AccountID == "" {
				if err := googleAdmission(st); err != nil {
					return nil, false, err
				}
				enrolGoogle(st, &row, a.ID)
			} else if operation == "grantGoogleAdministrator" {
				account, ok := st.Accounts[row.AccountID]
				if !ok || account.Suspended {
					return nil, false, failure(409, "enrollment_required", "The Google account must be enrolled and active")
				}
				if account.Administrator {
					return googleEnrollmentView(st, row), false, nil
				}
				account.Administrator = true
				st.Accounts[account.ID] = account
				row.AdminGrantBy, row.AdminGrantAt = a.ID, time.Now().UTC()
			} else {
				return googleEnrollmentView(st, row), false, nil
			}
			st.GoogleEnrollment[key] = row
			return googleEnrollmentView(st, row), true, nil
		}
		return nil, false, failure(404, "enrollment_not_found", "Google enrollment request not found")
	}
	c.Version++
	st.GoogleSSO = c
	return publicGoogleSSO(c), true, nil
}

// Account activation shares the store transaction used by local session and
// Git intent reservations. A second account keeps real host admissions closed.
func googleAdmission(st *model.State) error {
	if st.Settings.Agents || !contains([]string{"", "off"}, st.Settings.ExecutionMode) {
		return failure(409, "execution_active", "Set execution to Off before enrolling additional accounts")
	}
	for _, row := range st.LocalSessions {
		if activeLocal(row) || row.Managed != nil && !row.Managed.Applied {
			return failure(409, "execution_active", "Drain and reconcile existing local sessions before enrolling additional accounts")
		}
	}
	for _, ticket := range st.Tickets {
		if ticket.Delivery != nil && ticket.Delivery.Operation != nil && ticket.Delivery.Operation.State != "complete" {
			return failure(409, "execution_active", "Reconcile pending Git operations before enrolling additional accounts")
		}
	}
	return nil
}

func enrolGoogle(st *model.State, row *model.GoogleEnrollment, approver string) {
	id := "google-" + randomID()[:24]
	for st.Accounts[id].ID != "" {
		id = "google-" + randomID()[:24]
	}
	a := model.Account{ID: id, Username: id}
	st.Accounts[id] = a
	ensurePersonalProject(st, a)
	row.AccountID, row.ApprovedBy, row.ApprovedAt = id, approver, time.Now().UTC()
}

// GoogleSignIn accepts only claims obtained by the HTTP handler directly from
// Google's authenticated UserInfo endpoint. Email is metadata, never an identity key.
func (s *Service) GoogleSignIn(ctx context.Context, version int64, subject, email string) (string, string, error) {
	address, err := mail.ParseAddress(email)
	if !validGoogleCredential(subject, 255) || len(email) > 254 || err != nil || address.Address != email {
		return "", "", failure(401, "invalid_google_identity", "Google did not provide an eligible verified identity")
	}
	var token, outcome string
	err = s.db.Update(ctx, func(st *model.State) (bool, error) {
		c := st.GoogleSSO
		if !c.Enabled || version != c.Version {
			return false, failure(409, "sso_settings_changed", "Google sign-in changed; start again")
		}
		collections(st)
		key := digest("google\x00" + subject)
		row, exists := st.GoogleEnrollment[key]
		if !exists {
			pending := 0
			for _, entry := range st.GoogleEnrollment {
				if entry.AccountID == "" {
					pending++
				}
			}
			if pending >= 1000 {
				return false, failure(429, "sso_queue_full", "The enrollment queue is full")
			}
			row = model.GoogleEnrollment{ID: randomID(), RequestedAt: time.Now().UTC()}
		}
		changed := !exists || row.Email != email
		row.Email = email
		if row.AccountID == "" && c.Mode == "auto-enrol" && googleAdmission(st) == nil {
			enrolGoogle(st, &row, "")
			changed = true
		}
		if st.GoogleEnrollment == nil {
			st.GoogleEnrollment = map[string]model.GoogleEnrollment{}
		}
		st.GoogleEnrollment[key] = row
		if row.AccountID == "" {
			outcome = "waitlist"
			if c.Mode == "registered" {
				outcome = "registered"
			}
			return changed, nil
		}
		account, ok := st.Accounts[row.AccountID]
		if !ok || account.Suspended {
			return false, failure(403, "account_unavailable", "This account cannot sign in")
		}
		token = newSignInSession(st, account)
		return true, nil
	})
	return token, outcome, err
}
