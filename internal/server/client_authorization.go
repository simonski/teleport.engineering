package server

import "teleport/internal/app"

func init() {
	entries := []struct {
		name, action, summary string
		input, output         any
	}{
		{"authorizeClient", "ACT-1311", "Approve a one-time desktop sign-in from the browser", app.ClientAuthorizeInput{}, app.ClientAuthorizationCode{}},
		{"exchangeClientAuthorization", "ACT-1311", "Exchange desktop authorization for a separate sign-in", app.ClientExchangeInput{}, app.LoginResult{}},
		{"getMenuStatus", "ACT-1424", "Read authorized project activity counts for the menu bar", struct{}{}, app.MenuStatus{}},
	}
	for _, e := range entries {
		Operations = append(Operations, Operation{e.name, e.action, e.summary})
		contracts[e.name] = contract{e.input, e.output}
	}
}
