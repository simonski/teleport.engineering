package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"teleport/internal/app"
)

func configSSO(args []string, input *os.File, out, diagnostics io.Writer) error {
	const usage = "Usage: tp config setup-sso|enable-sso|disable-sso [-server URL] [-config FILE] [-json]"
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(out, usage)
		return nil
	}
	action := args[0]
	operation := map[string]string{"setup-sso": "setupGoogleSSO", "enable-sso": "enableGoogleSSO", "disable-sso": "disableGoogleSSO"}[action]
	if operation == "" {
		return errors.New(usage)
	}
	flags := flag.NewFlagSet("config "+action, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	home, _ := os.UserHomeDir()
	endpoint := flags.String("server", env("TELEPORT_SERVER", "http://localhost:9999"), "server URL")
	config := flags.String("config", env("TELEPORT_CONFIG", filepath.Join(home, ".teleport", "client.json")), "client credentials")
	machine := flags.Bool("json", false, "print Google sign-in configuration metadata as JSON")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(usage)
	}
	*endpoint = strings.TrimRight(*endpoint, "/")
	creds, err := readCredentials(*endpoint, *config)
	if err != nil {
		return err
	}
	client := apiClient{endpoint: *endpoint, token: creds.Token, diagnostics: diagnostics}
	body, err := client.operation("getGoogleSSO", []byte(`{}`))
	if err != nil {
		return err
	}
	var current app.GoogleSSOSettings
	if err := json.Unmarshal(body, &current); err != nil {
		return err
	}
	var data any = app.GoogleSSOVersion{ExpectedVersion: current.Version}
	if action == "setup-sso" {
		setup, err := promptGoogleSSO(input, diagnostics, current, *endpoint)
		if err != nil {
			return err
		}
		data = setup
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	body, err = client.operation(operation, raw)
	if err != nil {
		return err
	}
	return printResponse(out, body, *machine)
}

func promptGoogleSSO(input *os.File, diagnostics io.Writer, current app.GoogleSSOSettings, endpoint string) (app.GoogleSSOSetup, error) {
	console := term.IsTerminal(int(input.Fd()))
	scanner := bufio.NewScanner(io.LimitReader(input, 8192))
	ask := func(label, fallback string) (string, error) {
		if fallback != "" {
			fmt.Fprintf(diagnostics, "%s [%s]: ", label, fallback)
		} else {
			fmt.Fprintf(diagnostics, "%s: ", label)
		}
		if !scanner.Scan() {
			return "", errors.New("Google sign-in setup input ended")
		}
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			value = fallback
		}
		return value, nil
	}
	result := app.GoogleSSOSetup{ExpectedVersion: current.Version}
	var err error
	if result.ClientID, err = ask("Google OAuth client ID", current.ClientID); err != nil {
		return result, err
	}
	redirect := current.RedirectURI
	if redirect == "" {
		redirect = endpoint + app.GoogleCallbackPath
	}
	if result.RedirectURI, err = ask("Authorized redirect URI", redirect); err != nil {
		return result, err
	}
	if result.Mode, err = ask("Enrollment mode (auto-enrol, waitlist, registered)", current.Mode); err != nil {
		return result, err
	}
	prompt := "Google OAuth client secret"
	if current.Configured && result.ClientID == current.ClientID {
		prompt += " (blank keeps saved secret)"
	}
	if !console {
		result.ClientSecret, err = ask(prompt, "")
		return result, err
	}
	fmt.Fprint(diagnostics, prompt+": ")
	secret, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(diagnostics)
	if err != nil {
		return result, errors.New("could not read Google OAuth client secret")
	}
	result.ClientSecret = strings.TrimSpace(string(secret))
	return result, nil
}
