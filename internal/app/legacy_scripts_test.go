package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"teleport/internal/app"
	"teleport/internal/model"
)

func TestLegacyScriptCredentialCannotAuthenticate(t *testing.T) {
	ctx := context.Background()
	svc, admin, db := fixtureWithStore(t)
	const token = "retired-script-credential"
	hash := sha256.Sum256([]byte(token))
	row := model.ScriptRun{
		ID: "J1", Creator: "admin", State: "running", TokenHash: hex.EncodeToString(hash[:]),
		ExpiresAt: time.Now().Add(time.Minute), Source: "return 1",
	}
	if err := db.Update(ctx, func(st *model.State) (bool, error) {
		st.ScriptRuns = map[string]model.ScriptRun{row.ID: row}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Call(ctx, token, "getSession", nil); err == nil || app.ErrorResponse(err).Status != 401 {
		t.Fatalf("retired credential should be unauthorized, got %v", err)
	}
	if _, err := svc.Call(ctx, admin, "startScriptRun", []byte(`{"source":"return 1","mode":"script","requestId":"retired"}`)); err == nil || app.ErrorResponse(err).Status != 404 {
		t.Fatalf("retired operation should be unavailable, got %v", err)
	}
	st, err := db.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ScriptRuns) != 1 || st.ScriptRuns[row.ID].Source != row.Source {
		t.Fatal("legacy history changed after rejected requests")
	}
}
