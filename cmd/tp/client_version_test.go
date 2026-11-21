package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"teleport/internal/version"
)

func TestAPIVersionCompatibility(t *testing.T) {
	major, minor, _ := version.MajorMinor(version.CLI)
	patchOnly := fmt.Sprintf("%d.%d.99", major, minor)
	newer := fmt.Sprintf("%d.%d.0", major, minor+1)
	for _, test := range []struct {
		name, advertised, health, warning, versionError string
	}{
		{name: "patch only", advertised: patchOnly, versionError: "server version " + patchOnly},
		{name: "newer server", advertised: newer, warning: "Upgrade the CLI.", versionError: "server version " + newer},
		{name: "legacy fallback", health: "0.1.0-deploy+abcdef", warning: "Upgrade the server.", versionError: "server version 0.1.0-deploy+abcdef"},
		{name: "no advertised version", health: "invalid", versionError: "server version unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			probes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthz" {
					probes++
					fmt.Fprintf(w, `{"version":%q}`, test.health)
					return
				}
				if test.advertised != "" {
					w.Header().Set(version.Header, test.advertised)
				}
				if r.URL.Path == "/api/tools/unavailable" {
					w.WriteHeader(http.StatusNotFound)
					io.WriteString(w, `{"error":{"message":"Unknown operation"}}`)
					return
				}
				io.WriteString(w, `{"items":[]}`)
			}))
			defer server.Close()
			var diagnostics bytes.Buffer
			client := apiClient{endpoint: server.URL, diagnostics: &diagnostics}
			body, err := client.get("/api/actions")
			if err != nil || string(body) != `{"items":[]}` {
				t.Fatalf("discovery body changed: %s (%v)", body, err)
			}
			if _, err := client.operation("unavailable", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), test.versionError) {
				t.Fatalf("unavailable command omitted version: %v", err)
			}
			wantProbes := 0
			if test.advertised == "" {
				wantProbes = 1
			}
			if probes != wantProbes {
				t.Fatalf("version probes: got %d, want %d", probes, wantProbes)
			}
			if test.warning == "" && diagnostics.Len() != 0 {
				t.Fatalf("unexpected warning: %s", diagnostics.String())
			}
			if test.warning != "" && (strings.Count(diagnostics.String(), test.warning) != 1 || !strings.Contains(diagnostics.String(), version.CLI)) {
				t.Fatalf("incorrect version warning: %s", diagnostics.String())
			}
		})
	}
}
