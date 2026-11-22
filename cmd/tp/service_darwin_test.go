package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"teleport/internal/server"
)

func TestServiceUpgradeRestoresPreviousLaunchAgentOnWrongProcess(t *testing.T) {
	root := t.TempDir()
	m := serviceManager{
		root: root, agent: filepath.Join(root, "server.plist"), domain: "gui/test",
		free:    func(string) error { return nil },
		timeout: time.Millisecond,
	}
	makeRelease := func(build string) string {
		t.Helper()
		source := filepath.Join(root, build)
		body := "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"1.2.0\",\"buildNumber\":\"" + build + "\"}'\n"
		if err := os.WriteFile(source, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		return source
	}
	previousRelease, err := m.stage(makeRelease("previous"))
	if err != nil {
		t.Fatal(err)
	}
	previous := serviceJob{ProgramArguments: []string{
		previousRelease, "serve", "-addr", "127.0.0.1:10000", "-db-config", filepath.Join(root, "db & config.json"), "-public-host", "",
	}}
	if err := m.writeJob(previous); err != nil {
		t.Fatal(err)
	}
	program := previousRelease
	var launches []string
	m.launch = func(args ...string) (string, error) {
		switch args[0] {
		case "print":
			if program == "" {
				return fmt.Sprintf("Could not find service %q", serviceLabel), errors.New("missing job")
			}
			return fmt.Sprintf("program = %s\npath = %s\npid = 2147483647", program, m.agent), nil
		case "bootout":
			program = ""
			return "", nil
		case "bootstrap":
			job, err := m.readJob()
			if err != nil {
				return "", err
			}
			program = job.program()
			launches = append(launches, program)
			return "", nil
		default:
			t.Fatalf("unexpected launchctl command %q", args)
			return "", nil
		}
	}
	m.probe = func(string) server.Readiness {
		if program == previousRelease {
			return server.Readiness{PID: 2147483647, Status: "ready", Version: "1.2.0", BuildNumber: "previous"}
		}
		return server.Readiness{PID: 42, Status: "ready", Version: "1.2.0", BuildNumber: "candidate"}
	}
	err = m.upgrade(previous, makeRelease("candidate"))
	if err == nil || !strings.Contains(err.Error(), "previous release was restored") {
		t.Fatalf("unexpected upgrade outcome: %v", err)
	}
	if len(launches) != 2 || launches[0] == previousRelease || launches[1] != previousRelease {
		t.Fatalf("unexpected launch sequence: %v", launches)
	}
	job, err := m.readJob()
	if err != nil || job.program() != previousRelease {
		t.Fatalf("rollback plist: %+v, %v", job, err)
	}
}

func TestServiceDoesNotStopUnrelatedLaunchAgent(t *testing.T) {
	m := serviceManager{
		agent: "/test/server.plist",
		launch: func(args ...string) (string, error) {
			if args[0] != "print" {
				t.Fatalf("unexpected launchctl command %q", args)
			}
			return "program = /test/unrelated\npath = /test/server.plist\npid = 100", nil
		},
	}
	job := serviceJob{ProgramArguments: []string{"/test/tp"}}
	if err := m.stop(job); err == nil || !strings.Contains(err.Error(), "different program") {
		t.Fatalf("stop outcome: %v", err)
	}
}
