// Package clientauth shares the browser handoff format between server and CLI.
package clientauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
)

const (
	PortField      = "desktop_port"
	StateField     = "desktop_state"
	ChallengeField = "desktop_challenge"
	CallbackPath   = "/teleport/callback"
)

func Secret() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}

func Valid(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32
}

func Challenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// Continuation copies only the fixed, validated browser handoff parameters.
func Continuation(query url.Values) url.Values {
	port, state, challenge := query.Get(PortField), query.Get(StateField), query.Get(ChallengeField)
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port || !Valid(state) || !Valid(challenge) {
		return nil
	}
	return url.Values{PortField: {port}, StateField: {state}, ChallengeField: {challenge}}
}
