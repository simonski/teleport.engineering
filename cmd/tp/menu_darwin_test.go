package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMenuSetupRequiresConsent(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if err := setupMenu("https://teleport.example.test", strings.NewReader("no\n"), &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "[y/N]") || !strings.Contains(output.String(), "skipped") {
		t.Fatalf("decline must skip installation: %s, %s", output.String(), diagnostics.String())
	}
}
