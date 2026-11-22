package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed menu.swift
var menuSource []byte

const menuLabel = "engineering.teleport.menu"

func setupMenu(endpoint string, input io.Reader, out, diagnostics io.Writer) error {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("enter a Teleport server origin such as https://teleport.engineering")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return errors.New("the menu bar requires HTTPS or local loopback")
	}
	fmt.Fprint(diagnostics, "Install Teleport Menu for this macOS user and start it at login? [y/N] ")
	in := bufio.NewScanner(io.LimitReader(input, 256))
	if !in.Scan() || (!strings.EqualFold(strings.TrimSpace(in.Text()), "y") && !strings.EqualFold(strings.TrimSpace(in.Text()), "yes")) {
		fmt.Fprintln(out, "Menu bar installation skipped.")
		return in.Err()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p").Run() != nil {
		return errors.New("the Apple Command Line Tools must already be installed to build Teleport Menu")
	}
	return installMenu(ctx, home, executable, endpoint, out)
}

func installMenu(ctx context.Context, home, executable, endpoint string, out io.Writer) error {
	apps := filepath.Join(home, "Applications")
	agents := filepath.Join(home, "Library", "LaunchAgents")
	for _, path := range []string{apps, agents} {
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(filepath.Join(agents, "."+menuLabel+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another Teleport Menu installation is in progress")
	}
	bundle := filepath.Join(apps, "Teleport Menu.app")
	staging, err := os.MkdirTemp(apps, ".teleport-menu-")
	if err != nil {
		return err
	}
	clean := true
	defer func() {
		if clean {
			_ = os.RemoveAll(staging)
		}
	}()
	stageBundle := filepath.Join(staging, "Teleport Menu.app")
	stageContents := filepath.Join(stageBundle, "Contents")
	if err := os.MkdirAll(filepath.Join(stageContents, "MacOS"), 0755); err != nil {
		return err
	}
	sourceFile := filepath.Join(staging, "Menu.swift")
	if err := os.WriteFile(sourceFile, menuSource, 0600); err != nil {
		return err
	}
	macOS := filepath.Join(stageContents, "MacOS")
	if output, err := exec.CommandContext(ctx, "/usr/bin/xcrun", "swiftc", "-O", sourceFile, "-o", filepath.Join(macOS, "TeleportMenu")).CombinedOutput(); err != nil {
		return fmt.Errorf("build Teleport Menu: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	if err := copyExecutable(executable, filepath.Join(macOS, "tp")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stageContents, "Info.plist"), plist(menuInfo), 0644); err != nil {
		return err
	}
	// Swap the app bundle on the same volume and retain the previous bundle
	// until the replacement has reached its final path.
	if info, err := os.Lstat(bundle); err == nil {
		oldInfo, readErr := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
		if !info.IsDir() || readErr != nil || !bytes.Equal(oldInfo, plist(menuInfo)) {
			return errors.New("another application occupies the Teleport Menu install location")
		}
		backup := filepath.Join(staging, "previous.app")
		if err := os.Rename(bundle, backup); err != nil {
			return err
		}
		if err := os.Rename(stageBundle, bundle); err != nil {
			if restoreErr := os.Rename(backup, bundle); restoreErr != nil {
				clean = false
				return fmt.Errorf("replace Teleport Menu: %w; previous bundle retained at %s", errors.Join(err, restoreErr), backup)
			}
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := os.Rename(stageBundle, bundle); err != nil {
		return err
	}

	plistPath := filepath.Join(agents, menuLabel+".plist")
	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/" + menuLabel
	launchArgs := []string{filepath.Join(bundle, "Contents", "MacOS", "TeleportMenu"), endpoint}
	agentFile, err := os.CreateTemp(agents, ".teleport-menu-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(agentFile.Name())
	_, err = agentFile.Write(menuAgentPlist(launchArgs))
	closeErr := agentFile.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err := os.Rename(agentFile.Name(), plistPath); err != nil {
		return err
	}
	if exec.CommandContext(ctx, "/bin/launchctl", "print", target).Run() == nil {
		if output, err := exec.CommandContext(ctx, "/bin/launchctl", "bootout", target).CombinedOutput(); err != nil {
			return fmt.Errorf("restart Teleport Menu: %w (%s)", err, strings.TrimSpace(string(output)))
		}
	}
	if output, err := exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("Teleport Menu is installed at %s. Start it from a macOS GUI login session: %w (%s)", bundle, err, strings.TrimSpace(string(output)))
	}
	fmt.Fprintln(out, "Teleport Menu installed. Select Sign In in the menu bar.")
	return nil
}

func startMenu() error {
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + menuLabel
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/bin/launchctl", "kickstart", target).CombinedOutput(); err != nil {
		return fmt.Errorf("start the installed menu bar from a GUI login, or run tp setup -menu: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func copyExecutable(source, path string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	_, writeErr := io.Copy(output, input)
	return errors.Join(writeErr, output.Close())
}

func plist(contents string) []byte {
	return []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n" + contents + "</dict></plist>\n")
}

const menuInfo = `<key>CFBundleIdentifier</key><string>engineering.teleport.menu</string>
<key>CFBundleName</key><string>Teleport Menu</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleExecutable</key><string>TeleportMenu</string>
<key>LSUIElement</key><true/>
`

func menuAgentPlist(arguments []string) []byte {
	var result bytes.Buffer
	result.WriteString("<key>Label</key><string>" + menuLabel + "</string>\n<key>RunAtLoad</key><true/>\n<key>LimitLoadToSessionType</key><string>Aqua</string>\n<key>ProgramArguments</key><array>\n")
	for _, argument := range arguments {
		result.WriteString("<string>")
		xml.EscapeText(&result, []byte(argument))
		result.WriteString("</string>\n")
	}
	result.WriteString("</array>\n")
	return plist(result.String())
}
