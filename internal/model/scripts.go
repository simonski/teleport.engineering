package model

import (
	"encoding/json"
	"time"
)

// ScriptCall and ScriptRun retain historical records from releases with a server-side interpreter.
type ScriptCall struct {
	ID                string          `json:"id"`
	Operation         string          `json:"operation"`
	Arguments         json.RawMessage `json:"arguments"`
	Fingerprint       string          `json:"fingerprint"`
	Basis             string          `json:"basis,omitempty"`
	State             string          `json:"state"`
	Approved          bool            `json:"approved"`
	ConfirmationActor string          `json:"confirmationActor,omitempty"`
	ConfirmedAt       *time.Time      `json:"confirmedAt,omitempty"`
	Decision          string          `json:"decision,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	ErrorCode         string          `json:"errorCode,omitempty"`
	ErrorMessage      string          `json:"errorMessage,omitempty"`
	At                time.Time       `json:"at"`
}
type ScriptRun struct {
	StopActor     string       `json:"stopActor,omitempty"`
	StopAt        *time.Time   `json:"stopAt,omitempty"`
	ID            string       `json:"id"`
	Number        int64        `json:"number"`
	Creator       string       `json:"creator"`
	RequestID     string       `json:"requestId"`
	Source        string       `json:"source"`
	Mode          string       `json:"mode"`
	Fingerprint   string       `json:"fingerprint"`
	TokenHash     string       `json:"tokenHash"`
	LaunchID      string       `json:"launchId"`
	Generation    string       `json:"generation"`
	State         string       `json:"state"`
	RuntimeState  string       `json:"runtimeState"`
	StopRequested bool         `json:"stopRequested"`
	ErrorCode     string       `json:"errorCode,omitempty"`
	Projects      []int64      `json:"projects"`
	Calls         []ScriptCall `json:"calls"`
	Output        string       `json:"output"`
	Cursor        int64        `json:"cursor"`
	CreatedAt     time.Time    `json:"createdAt"`
	ExpiresAt     time.Time    `json:"expiresAt"`
	FinishedAt    *time.Time   `json:"finishedAt,omitempty"`
}

func (row ScriptRun) Unresolved() bool {
	switch row.State {
	case "completed", "failed", "stopped", "interrupted":
		return false
	default:
		return true
	}
}

// WithdrawSuspendedPrivateExecutions runs in the same server transaction as
// account changes. Reactivation cannot undo a committed execution withdrawal.
func (st *State) WithdrawSuspendedPrivateExecutions() {
	for id, row := range st.LocalSessions {
		if row.TicketID != 0 || row.State == "exited" || row.State == "failed" {
			continue
		}
		a, ok := st.Accounts[row.Creator]
		if ok && !a.Suspended {
			continue
		}
		row.StopRequested = true
		st.LocalSessions[id] = row
	}
}
