package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"sort"
	"time"

	"teleport/internal/clientauth"
	"teleport/internal/model"
)

type ClientAuthorizeInput struct {
	Challenge string `json:"challenge"`
	AccountID string `json:"accountId"`
}

type ClientAuthorizationCode struct {
	Code string `json:"code"`
}

type ClientExchangeInput struct {
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
}

type clientAuthorization struct {
	challenge string
	session   string
	expires   time.Time
}

func (s *Service) authorizeClient(ctx context.Context, token string, raw json.RawMessage) (any, error) {
	var in ClientAuthorizeInput
	if err := parse(raw, &in); err != nil {
		return nil, err
	}
	if !clientauth.Valid(in.Challenge) {
		return nil, failure(422, "invalid_challenge", "Start a new Teleport desktop sign-in")
	}
	st, err := s.db.Read(ctx)
	if err != nil {
		return nil, err
	}
	sessionKey := digest(token)
	if account, err := clientBrowserAccount(st, sessionKey); err != nil {
		return nil, err
	} else if account.ID != in.AccountID {
		return nil, failure(409, "account_changed", "The browser account changed. Restart desktop sign-in")
	}

	now := time.Now()
	s.clientAuthMu.Lock()
	defer s.clientAuthMu.Unlock()
	for key, grant := range s.clientGrants {
		if !now.Before(grant.expires) {
			delete(s.clientGrants, key)
		}
	}
	if len(s.clientGrants) >= 512 {
		return nil, failure(429, "client_authorizations_full", "Retry desktop sign-in shortly")
	}
	if s.clientGrants == nil {
		s.clientGrants = make(map[string]clientAuthorization)
	}
	code := clientauth.Secret()
	s.clientGrants[digest(code)] = clientAuthorization{in.Challenge, sessionKey, now.Add(time.Minute)}
	return ClientAuthorizationCode{Code: code}, nil
}

func clientBrowserAccount(st *model.State, key string) (model.Account, error) {
	session := st.Sessions[key]
	account, found := st.Accounts[session.AccountID]
	if !found || account.Suspended || !time.Now().Before(session.Expires) {
		return model.Account{}, failure(401, "unauthenticated", "Sign into Teleport in the browser first")
	}
	return account, nil
}

func (s *Service) exchangeClient(ctx context.Context, raw json.RawMessage) (any, error) {
	var in ClientExchangeInput
	if err := parse(raw, &in); err != nil {
		return nil, err
	}
	if !clientauth.Valid(in.Code) || !clientauth.Valid(in.Verifier) {
		return nil, failure(422, "invalid_client_exchange", "Start a new Teleport desktop sign-in")
	}
	key, proof := digest(in.Code), clientauth.Challenge(in.Verifier)
	s.clientAuthMu.Lock()
	grant, found := s.clientGrants[key]
	valid := found && time.Now().Before(grant.expires) && subtle.ConstantTimeCompare([]byte(proof), []byte(grant.challenge)) == 1
	if valid {
		delete(s.clientGrants, key)
	}
	s.clientAuthMu.Unlock()
	if !valid {
		return nil, failure(401, "invalid_client_grant", "Desktop authorization expired or did not match this client")
	}
	var result LoginResult
	err := s.db.Update(ctx, func(st *model.State) (bool, error) {
		account, err := clientBrowserAccount(st, grant.session)
		if err != nil {
			return false, err
		}
		token := newSignInSession(st, account)
		key := digest(token)
		session := st.Sessions[key]
		if expiry := st.Sessions[grant.session].Expires; expiry.Before(session.Expires) {
			session.Expires = expiry
			st.Sessions[key] = session
		}
		result = LoginResult{token, publicAccount(account)}
		return true, nil
	})
	return result, err
}

type MenuProject struct {
	Name      string `json:"name"`
	Doing     int    `json:"doing"`
	Attention int    `json:"attention"`
}

type MenuStatus struct {
	Username string        `json:"username"`
	Projects []MenuProject `json:"projects"`
}

func (s *Service) menuStatus(ctx context.Context, token string, raw json.RawMessage) (any, error) {
	if err := parse(raw, &struct{}{}); err != nil {
		return nil, err
	}
	st, err := s.db.Read(ctx)
	if err != nil {
		return nil, err
	}
	account, err := directoryActor(st, token)
	if err != nil {
		return nil, err
	}
	rows := make(map[int64]*MenuProject)
	for id, project := range st.Projects {
		if role(st, account, id) != "" {
			rows[id] = &MenuProject{Name: project.Name}
		}
	}
	for _, ticket := range st.Tickets {
		row := rows[ticket.ProjectID]
		if row == nil || ticket.Protected || ticket.Archived || ticket.Deleted || ticket.Status == "Done" || ticket.Status == "Cancelled" {
			continue
		}
		if ticket.Kind == "task" && ticket.Status == "Doing" {
			row.Doing++
		}
		if ticket.Attention != "" {
			row.Attention++
		}
	}
	result := MenuStatus{Username: account.Username, Projects: make([]MenuProject, 0, len(rows))}
	for _, row := range rows {
		result.Projects = append(result.Projects, *row)
	}
	sort.Slice(result.Projects, func(i, j int) bool {
		a, b := result.Projects[i], result.Projects[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Attention != b.Attention {
			return a.Attention > b.Attention
		}
		return a.Doing > b.Doing
	})
	return result, nil
}
