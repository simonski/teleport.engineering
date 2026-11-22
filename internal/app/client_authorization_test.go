package app_test

import (
	"context"
	"encoding/json"
	"testing"

	"teleport/internal/app"
	"teleport/internal/clientauth"
	"teleport/internal/model"
)

func TestClientAuthorizationRequiresProofAndCurrentBrowserSession(t *testing.T) {
	ctx := context.Background()
	svc, browser := fixture(t)
	verifier := clientauth.Secret()
	issue := app.ClientAuthorizeInput{Challenge: clientauth.Challenge(verifier), AccountID: "admin"}
	getCode := func() string {
		return call(t, svc, browser, "authorizeClient", issue).(app.ClientAuthorizationCode).Code
	}
	exchange := func(code, proof string) (any, error) {
		body, _ := json.Marshal(app.ClientExchangeInput{Code: code, Verifier: proof})
		return svc.Call(ctx, "", "exchangeClientAuthorization", body)
	}
	code := getCode()
	if _, err := exchange(code, clientauth.Secret()); err == nil {
		t.Fatal("exchange accepted the wrong verifier")
	}
	value, err := exchange(code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	desktop := value.(app.LoginResult)
	if desktop.Token == browser || desktop.Account.ID != "admin" {
		t.Fatal("desktop authorization did not issue a separate session for the approved account")
	}
	if _, err = exchange(code, verifier); err == nil {
		t.Fatal("grant replay was accepted")
	}
	waiting := getCode()
	call(t, svc, browser, "logout", struct{}{})
	if _, err = exchange(waiting, verifier); err == nil {
		t.Fatal("revoked browser session authorized a new desktop session")
	}
	if _, err = svc.Call(ctx, desktop.Token, "getSession", []byte(`{}`)); err != nil {
		t.Fatal("browser logout revoked an already independent desktop session")
	}
}

func TestMenuStatusCountsOnlyVisibleWork(t *testing.T) {
	ctx := context.Background()
	svc, token, db := fixtureWithStore(t)
	project := call(t, svc, token, "createProject", app.CreateProjectInput{Name: "Menu proof", RequestID: "menu-project"}).(model.Project)
	ticket := call(t, svc, token, "createTicket", app.CreateTicketInput{ProjectID: project.ID, Title: "In progress", RequestID: "menu-ticket"}).(model.Ticket)
	if err := db.Update(ctx, func(st *model.State) (bool, error) {
		ticket.Status, ticket.Attention = "Doing", "Needs review"
		st.Tickets[ticket.ID] = ticket
		st.Projects[900] = model.Project{ID: 900, Name: "Other person's project", PersonalOwner: "other"}
		st.Tickets[901] = model.Ticket{ID: 901, ProjectID: 900, Kind: "task", Status: "Doing", Attention: "Private"}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	status := call(t, svc, token, "getMenuStatus", struct{}{}).(app.MenuStatus)
	for _, row := range status.Projects {
		switch row.Name {
		case "Menu proof":
			if row.Doing != 1 || row.Attention != 1 {
				t.Fatalf("wrong visible counts: %+v", row)
			}
		case "Other person's project":
			t.Fatal("private project disclosed")
		}
	}
	if status.Username != "admin" || len(status.Projects) != 2 {
		t.Fatalf("unexpected signed-in menu: %+v", status)
	}
}
