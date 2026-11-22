package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"teleport/internal/app"
	"teleport/internal/localruntime"
	"teleport/internal/server"
	teleportskill "teleport/internal/skills/teleport"
	"teleport/internal/store"
	"teleport/internal/version"
)

const DEFAULT_PORT = 16000

var defaultListenAddress = net.JoinHostPort("localhost", strconv.Itoa(DEFAULT_PORT))

func defaultServerURL() string {
	return env("TELEPORT_SERVER", "http://"+defaultListenAddress)
}

type credentials struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tp:", err)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func run(args []string) error {
	expanded, done, err := expandCommand(args, os.Stdout)
	if err != nil || done {
		return err
	}
	args = expanded
	command := args[0]
	if command == "_spellcheck-job" {
		if len(args) != 2 {
			return errors.New("spellcheck wrapper requires a harness")
		}
		return runSpellcheck(args[1], os.Getenv("TELEPORT_SPELLCHECK"), os.Stdout)
	}
	if command == "_project-remove" {
		return removeProjectCommand(args[1:], os.Stdin, os.Stdout, os.Stderr)
	}
	if command == "setup" {
		return setupProject(args[1:], os.Stdin, os.Stdout, os.Stderr)
	}
	if command == "service" {
		return serviceCommand(args[1:], os.Stdout, os.Stderr)
	}
	if command == "menu" {
		if len(args) == 2 && (args[1] == "-h" || args[1] == "-help") {
			fmt.Println("Usage: tp menu")
			fmt.Println("Start the optional macOS menu bar installed by tp setup -menu.")
			return nil
		}
		if len(args) != 1 {
			return errors.New("usage: tp menu")
		}
		return startMenu()
	}
	if command == "config" {
		return configSSO(args[1:], os.Stdin, os.Stdout, os.Stderr)
	}
	if command == "import" {
		return captureText(args[1:], os.Stdin, os.Stdout, os.Stderr)
	}
	if command == "_codex-session" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		root := flags.String("runtime-dir", "", "owned runtime")
		id := flags.String("session", "", "owned session")
		launch := flags.String("launch", "", "launch identity")
		generation := flags.String("generation", "", "daemon identity")
		codex := flags.String("codex", "", "installed native harness")
		role := flags.String("role", "Console", "native phase role")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return localruntime.RunNative(ctx, *root, *id, *launch, *generation, *codex, *role)
	}
	if command == "_session-daemon" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		root := flags.String("runtime-dir", "", "owned local runtime directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return localruntime.Run(ctx, *root)
	}
	if command == "_shell-session" {
		if len(args) != 1 {
			return errors.New("shell wrapper takes no arguments")
		}
		shell := exec.Command("/bin/zsh", "-i")
		shell.Stdin, shell.Stdout, shell.Stderr = os.Stdin, os.Stdout, os.Stderr
		shell.Env = append(os.Environ(), "TERM=xterm-256color")
		return shell.Run()
	}
	if command == "_codex-pty-session" {
		if len(args) != 2 || !filepath.IsAbs(args[1]) {
			return errors.New("codex wrapper requires an absolute executable path")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if err = teleportskill.EnsureInstalled(home); err != nil {
			return fmt.Errorf("Teleport skill unavailable: %w", err)
		}
		codex := exec.Command(args[1])
		codex.Stdin, codex.Stdout, codex.Stderr = os.Stdin, os.Stdout, os.Stderr
		codex.Env = append(os.Environ(), "TERM=xterm-256color")
		if executable, err := os.Executable(); err == nil && filepath.Base(executable) == "tp" {
			codex.Env = append(codex.Env, "PATH="+filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		return codex.Run()
	}
	if command == "_harness-pty-session" {
		if len(args) != 2 || !filepath.IsAbs(args[1]) {
			return errors.New("harness wrapper requires an absolute executable path")
		}
		harness := exec.Command(args[1])
		harness.Stdin, harness.Stdout, harness.Stderr = os.Stdin, os.Stdout, os.Stderr
		harness.Env = append(os.Environ(), "TERM=xterm-256color")
		return harness.Run()
	}
	if command == "_persona-pty-session" {
		if len(args) != 2 || !filepath.IsAbs(args[1]) {
			return errors.New("persona wrapper requires an absolute executable path")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if err = teleportskill.EnsureInstalled(home); err != nil {
			return fmt.Errorf("Teleport skill unavailable: %w", err)
		}
		parameters, err := personaArguments(os.Getenv("TELEPORT_AGENT_PERSONA"))
		if err != nil {
			return err
		}
		var persona personaLaunch
		if err = json.Unmarshal([]byte(os.Getenv("TELEPORT_AGENT_PERSONA")), &persona); err != nil {
			return errors.New("invalid persona configuration")
		}
		if persona.ProposalJob != nil {
			return runPersonaProposal(args[1], parameters, persona)
		}
		harness := exec.Command(args[1], parameters...)
		harness.Stdin, harness.Stdout, harness.Stderr = os.Stdin, os.Stdout, os.Stderr
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "TELEPORT_AGENT_PERSONA=") {
				harness.Env = append(harness.Env, value)
			}
		}
		harness.Env = append(harness.Env, "TERM=xterm-256color")
		if executable, err := os.Executable(); err == nil && filepath.Base(executable) == "tp" {
			harness.Env = append(harness.Env, "PATH="+filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		return harness.Run()
	}
	if command == "_terminal-fixture" {
		fmt.Println("teleport terminal fixture · no model")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if scanner.Text() == "exit" {
				return nil
			}
			fmt.Println("received:", scanner.Text())
		}
		return scanner.Err()
	}
	if command == "version" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		machine := flags.Bool("json", false, "print the component version as JSON")
		component := flags.String("component", "cli", "application component: cli or server")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 || *component != "cli" && *component != "server" {
			return errors.New("usage: tp version [-component cli|server] [-json]")
		}
		value := version.CLI
		if *component == "server" {
			value = version.Server
		}
		if *machine {
			body, _ := json.Marshal(map[string]string{"version": value, "buildNumber": version.BuildNumber})
			return printResponse(os.Stdout, body, true)
		}
		fmt.Println(value)
		return nil
	}
	if command == "skill" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		flags.String("server", "", "accepted for compatibility; export is offline")
		flags.String("config", "", "accepted for compatibility; export needs no credentials")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: tp skill")
		}
		_, err := io.WriteString(os.Stdout, teleportskill.Markdown)
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	dbPath := flags.String("db", "", "PostgreSQL URL (init/serve only; prefer a protected -db-config file for secrets)")
	dbConfig := flags.String("db-config", env("TELEPORT_DB_CONFIG", filepath.Join(home, ".teleport", "database.json")), "owner-only database configuration JSON containing url")
	addr := flags.String("addr", defaultListenAddress, "listen address")
	port := 0
	if command == "serve" {
		flags.IntVar(&port, "p", 0, "listen on this loopback port")
	}
	publicHost := flags.String("public-host", "", "comma-separated trusted HTTPS browser hostnames behind a TLS proxy (no scheme or path)")
	runtimeRoot := flags.String("runtime-dir", env("TELEPORT_RUNTIME_DIR", ""), "private local session daemon state directory (serve only)")
	terminalFixture := flags.Bool("enable-terminal-fixture", false, "enable the no-model development terminal fixture (serve only)")
	endpoint := flags.String("server", defaultServerURL(), "server URL")
	config := flags.String("config", env("TELEPORT_CONFIG", filepath.Join(home, ".teleport", "client.json")), "client credential file")
	data := flags.String("data", "{}", "JSON request")
	machine := flags.Bool("json", false, "pretty-print JSON response")
	username := flags.String("username", "", "username (otherwise TELEPORT_USERNAME or prompt)")
	passwordStdin := flags.Bool("password-stdin", false, "read password from standard input")
	browserLogin := false
	if command == "login" {
		flags.BoolVar(&browserLogin, "browser", false, "sign in through the browser with a password or Google")
	}
	var exportState, restoreState bool
	var stateSHA string
	adminPasswordStdin := false
	if command == "init" {
		flags.BoolVar(&adminPasswordStdin, "admin-password-stdin", false, "read a strong initial admin password from standard input; never reset an existing password")
		flags.BoolVar(&exportState, "export-state-stdout", false, "export private installation state for offline backup")
		flags.BoolVar(&restoreState, "restore-state-stdin", false, "restore a checksummed offline snapshot into an empty database")
		flags.StringVar(&stateSHA, "state-sha256", "", "required checksum for offline restoration")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments; use -data JSON or tp help for noun/action commands")
	}
	var explicitDB, explicitConfig, explicitPort, explicitAddr bool
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "db" {
			explicitDB = true
		}
		if value.Name == "db-config" {
			explicitConfig = true
		}
		explicitPort = explicitPort || value.Name == "p"
		explicitAddr = explicitAddr || value.Name == "addr"
	})
	if explicitPort {
		if port < 1 || port > 65535 || explicitAddr {
			return errors.New("use -p PORT (1–65535) or -addr ADDRESS")
		}
		*addr = net.JoinHostPort("localhost", fmt.Sprint(port))
	}
	if !explicitDB && !explicitConfig {
		*dbPath = os.Getenv("TELEPORT_DB")
	}
	var initialPassword string
	if adminPasswordStdin {
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1026))
		if err != nil {
			return errors.New("could not read initial admin password")
		}
		initialPassword = strings.TrimRight(string(input), "\r\n")
		if len(initialPassword) < 12 || len(initialPassword) > 1024 {
			return errors.New("initial admin password must be between 12 and 1,024 bytes")
		}
	}
	ctx := context.Background()
	if command == "init" || command == "serve" {
		if stateSHA != "" && !restoreState {
			return errors.New("-state-sha256 requires -restore-state-stdin")
		}
		if exportState && restoreState || (exportState || restoreState) && adminPasswordStdin {
			return errors.New("choose exactly one initialization or offline snapshot operation")
		}
		location, err := databaseLocation(*dbPath, *dbConfig)
		if err != nil {
			return err
		}
		*dbPath = location
		db, err := store.Open(*dbPath, command == "init" && !exportState)
		if err != nil {
			if errors.Is(err, store.ErrDatabaseUnavailable) {
				return fmt.Errorf("%w\nFor the repository-managed local database, run make db-up from the repository, then retry tp %s. For an external database, start or contact its PostgreSQL service and check the configured connection", err, command)
			}
			return err
		}
		defer db.Close()
		if command == "init" {
			if exportState {
				body, err := db.Snapshot(ctx)
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(body)
				return err
			}
			if restoreState {
				body, err := io.ReadAll(io.LimitReader(os.Stdin, (256<<20)+1))
				if err != nil || len(body) > 256<<20 {
					return errors.New("snapshot must fit within 256 MiB")
				}
				if err = db.RestoreSnapshot(ctx, body, stateSHA); err != nil {
					return err
				}
				fmt.Println("Installation snapshot restored and verified; existing identity and credentials preserved.")
				return nil
			}
			_, _, initErr := app.InitializeWithOptions(ctx, db, app.InitializeOptions{AdminPassword: initialPassword})
			if initErr != nil {
				return initErr
			}
			fmt.Printf("Storage: %s\nInitialization complete. Run: tp serve\n", redactLocation(*dbPath))
			return nil
		}
		initialState, err := db.Read(ctx)
		if err != nil {
			return err
		}
		for _, run := range initialState.ScriptRuns {
			if run.Unresolved() {
				return errors.New("stop and reconcile legacy JavaScript jobs using the previous release before starting this server")
			}
		}
		service := app.New(db)
		if *runtimeRoot == "" {
			*runtimeRoot = filepath.Join(home, ".teleport", "runtime", initialState.InstallationID[:16])
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		localHost, localPort, splitErr := net.SplitHostPort(*addr)
		if splitErr != nil {
			return splitErr
		}
		if localHost == "" || localHost == "0.0.0.0" {
			localHost = "127.0.0.1"
		}
		if localHost == "::" {
			localHost = "::1"
		}
		service.ConfigureLocalRuntime(*runtimeRoot, executable, *terminalFixture, "http://"+net.JoinHostPort(localHost, localPort))
		publicHosts, hostErr := parsePublicHosts(*publicHost)
		if hostErr != nil {
			return hostErr
		}
		httpServer := server.HTTP(*addr, server.New(service, version.Server, publicHosts...))
		listener, err := net.Listen("tcp", *addr)
		if err != nil {
			return err
		}
		stopCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopCtx.Done():
					return
				case <-ticker.C:
					if err := service.PumpLocal(stopCtx); err != nil && stopCtx.Err() == nil {
						slog.Error("local runtime reconciliation failed", "error", err)
					}
					if err := service.PumpSpellchecks(stopCtx); err != nil && stopCtx.Err() == nil {
						slog.Error("spellcheck reconciliation failed", "error", err)
					}
					if err := service.PumpUsagePrices(stopCtx); err != nil && stopCtx.Err() == nil {
						slog.Error("usage price reconciliation failed", "error", err)
					}
					if err := service.PumpNative(stopCtx); err != nil && stopCtx.Err() == nil {
						slog.Error("native workflow reconciliation failed", "error", err)
					}
					if err := service.PumpDemo(stopCtx); err != nil && stopCtx.Err() == nil {
						slog.Error("demo scheduler failed", "error", err)
					}
				}
			}
		}()
		mode := initialState.Settings.ExecutionMode
		if mode == "" {
			mode = "off"
		}
		slog.Info("teleport listening", "address", *addr, "version", version.Server, "agents", mode)
		err = serveUntilStopped(stopCtx, httpServer, listener)
		cancel()
		select {
		case <-workerDone:
		case <-time.After(3 * time.Second):
			return errors.Join(err, errors.New("server workers exceeded the shutdown deadline"))
		}
		return err
	}
	*endpoint = strings.TrimRight(*endpoint, "/")
	client := apiClient{endpoint: *endpoint, diagnostics: os.Stderr}
	if command == "spec" || command == "tools" {
		path := map[string]string{"spec": "/openapi.json", "tools": "/api/actions"}[command]
		body, err := client.get(path)
		if err != nil {
			return err
		}
		return printResponse(os.Stdout, body, *machine)
	}
	creds, err := readCredentials(*endpoint, *config)
	if err != nil {
		return err
	}
	if command == "login" && !browserLogin {
		name, password, err := loginInput(*username, *passwordStdin, os.Stdin, os.Stderr)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(map[string]string{"username": name, "password": password})
		*data = string(encoded)
	}
	client.token = creds.Token
	var body []byte
	if browserLogin {
		if *username != "" || *passwordStdin {
			return errors.New("use -browser on its own or supply password options")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		body, err = browserSignIn(ctx, &client, os.Stderr, openSignInBrowser)
	} else {
		body, err = client.operation(command, []byte(*data))
	}
	if err != nil {
		return err
	}
	if command == "login" {
		var login app.LoginResult
		if err = json.Unmarshal(body, &login); err != nil {
			return err
		}
		creds = credentials{*endpoint, login.Token}
		if err = saveCredentials(*config, creds); err != nil {
			return err
		}
		if *machine {
			body, _ := json.Marshal(map[string]string{"username": login.Account.Username})
			return printResponse(os.Stdout, body, true)
		}
		fmt.Println("Signed in as", login.Account.Username)
		return nil
	}
	if command == "logout" {
		if err = os.Remove(*config); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return printResponse(os.Stdout, body, *machine)
}

func serveUntilStopped(ctx context.Context, server *http.Server, listener net.Listener) error {
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return errors.Join(err, server.Close())
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
func redactLocation(location string) string {
	if strings.HasPrefix(location, "postgres") {
		return "PostgreSQL (configured instance)"
	}
	return location
}
func saveCredentials(path string, c credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".client-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(c)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
