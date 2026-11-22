package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"teleport/internal/server"
	"teleport/internal/version"
)

const serviceLabel = "engineering.teleport.server"

type serviceManager struct {
	root, agent, domain string
	launch              func(...string) (string, error)
	probe               func(string) server.Readiness
	free                func(string) error
	timeout             time.Duration
}

type serviceJob struct {
	ProgramArguments []string
}

func (j serviceJob) program() string { return j.ProgramArguments[0] }
func (j serviceJob) address() string { return j.ProgramArguments[3] }

type managedStatus struct {
	State       string `json:"state"`
	Address     string `json:"address,omitempty"`
	PID         int    `json:"pid,omitempty"`
	Version     string `json:"version,omitempty"`
	BuildNumber string `json:"buildNumber,omitempty"`
	Log         string `json:"log,omitempty"`
}

func manageService(action string, options serviceOptions, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	m := serviceManager{
		root:    filepath.Join(home, ".teleport", "service"),
		agent:   filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
		domain:  "gui/" + strconv.Itoa(os.Getuid()),
		launch:  func(args ...string) (string, error) { return serviceSystemCommand("/bin/launchctl", args...) },
		probe:   probeService,
		free:    checkServicePort,
		timeout: 25 * time.Second,
	}
	if action == "setup" {
		if err := os.MkdirAll(m.root, 0700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(m.agent), 0755); err != nil {
			return err
		}
	}
	info, err := os.Lstat(m.root)
	if errors.Is(err, os.ErrNotExist) {
		if action == "status" {
			return printServiceStatus(out, options.machine, managedStatus{State: "not installed"})
		}
		return errors.New("install the macOS server with tp service setup")
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		return errors.New("service state must be an owner-only directory")
	}
	lock, err := os.OpenFile(filepath.Join(m.root, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("could not acquire the Teleport service lock: %w", err)
	}
	job, err := m.readJob()
	if errors.Is(err, os.ErrNotExist) {
		switch action {
		case "status":
			return printServiceStatus(out, options.machine, managedStatus{State: "not installed"})
		case "setup":
			job, err = m.install(home, options)
		default:
			return errors.New("install the macOS server with tp service setup")
		}
	}
	if err != nil {
		return err
	}
	switch action {
	case "setup", "start":
		err = m.start(job)
	case "stop":
		err = m.stop(job)
	case "restart":
		if err = m.stop(job); err == nil {
			err = m.start(job)
		}
	case "upgrade":
		err = m.upgrade(job, options.binary)
		if err == nil {
			job, err = m.readJob()
		}
	}
	if err != nil {
		return err
	}
	status, err := m.status(job)
	if err != nil {
		return err
	}
	return printServiceStatus(out, options.machine, status)
}

func (m serviceManager) install(home string, o serviceOptions) (serviceJob, error) {
	var job serviceJob
	if o.port < 1 || o.port > 65535 {
		return job, errors.New("service port must be between 1 and 65535")
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(o.port))
	loaded, _, err := m.registration(job)
	if err != nil || loaded {
		return job, errors.Join(err, errors.New("verify the launchd label is available before installing"))
	}
	if err := m.free(address); err != nil {
		return job, err
	}
	if o.db == "" {
		o.db = filepath.Join(home, ".teleport", "database.json")
	}
	db, err := filepath.Abs(o.db)
	if err != nil {
		return job, err
	}
	if _, err := databaseLocation("", db); err != nil {
		return job, err
	}
	hosts, err := parsePublicHosts(o.hosts)
	if err != nil {
		return job, err
	}
	program, err := m.stage("")
	if err != nil {
		return job, err
	}
	job.ProgramArguments = []string{program, "serve", "-addr", address, "-db-config", db, "-public-host", strings.Join(hosts, ",")}
	if o.runtime != "" {
		runtime, err := filepath.Abs(o.runtime)
		if err != nil {
			return job, err
		}
		job.ProgramArguments = append(job.ProgramArguments, "-runtime-dir", runtime)
	}
	if _, err := m.verifyRelease(program); err != nil {
		return serviceJob{}, err
	}
	return job, m.writeJob(job)
}

func (m serviceManager) readJob() (serviceJob, error) {
	var job serviceJob
	info, err := os.Lstat(m.agent)
	if err != nil {
		return job, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		return job, errors.New("refusing to manage a LaunchAgent owned by another installation")
	}
	body, err := os.ReadFile(m.agent)
	if err != nil {
		return job, err
	}
	data, err := serviceSystemCommand("/usr/bin/plutil", "-convert", "json", "-o", "-", m.agent)
	if err != nil {
		return job, err
	}
	if json.Unmarshal([]byte(data), &job) != nil || !m.validJob(job) || !bytes.Equal(body, m.jobPlist(job)) {
		return serviceJob{}, errors.New("refusing to manage an unrecognized Teleport LaunchAgent")
	}
	return job, nil
}

func (m serviceManager) validJob(job serviceJob) bool {
	a := job.ProgramArguments
	if len(a) != 8 && len(a) != 10 {
		return false
	}
	host, port, err := net.SplitHostPort(job.address())
	n, parseErr := strconv.Atoi(port)
	return err == nil && parseErr == nil && host == "127.0.0.1" && n > 0 && n <= 65535 &&
		m.releaseDigest(job.program()) != "" && a[1] == "serve" && a[2] == "-addr" &&
		a[4] == "-db-config" && filepath.IsAbs(a[5]) && a[6] == "-public-host" &&
		(len(a) == 8 || a[8] == "-runtime-dir" && filepath.IsAbs(a[9]))
}

func (m serviceManager) jobPlist(job serviceJob) []byte {
	var b bytes.Buffer
	putString := func(value string) {
		b.WriteString("<string>")
		xml.EscapeText(&b, []byte(value))
		b.WriteString("</string>\n")
	}
	b.WriteString("<key>Label</key>")
	putString(serviceLabel)
	b.WriteString("<key>Program</key>")
	putString(job.program())
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, arg := range job.ProgramArguments {
		putString(arg)
	}
	b.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n")
	b.WriteString("<key>ThrottleInterval</key><integer>5</integer>\n<key>ExitTimeOut</key><integer>15</integer>\n")
	b.WriteString("<key>EnvironmentVariables</key><dict><key>PATH</key>")
	home := filepath.Dir(filepath.Dir(m.root))
	putString(strings.Join([]string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"))
	b.WriteString("</dict>\n")
	b.WriteString("<key>StandardOutPath</key>")
	putString(filepath.Join(m.root, "server.log"))
	b.WriteString("<key>StandardErrorPath</key>")
	putString(filepath.Join(m.root, "server.log"))
	return plist(b.String())
}

func (m serviceManager) writeJob(job serviceJob) error {
	tmp, err := os.CreateTemp(filepath.Dir(m.agent), ".teleport-server-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(m.jobPlist(job))
	if err := errors.Join(writeErr, tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.agent)
}

func (m serviceManager) registration(job serviceJob) (bool, int, error) {
	output, err := m.launch("print", m.domain+"/"+serviceLabel)
	if err != nil {
		if strings.Contains(output, "Could not find service \""+serviceLabel+"\"") {
			return false, 0, nil
		}
		return false, 0, err
	}
	if len(job.ProgramArguments) == 0 || launchField(output, "program") != job.program() || !sameAgentPath(launchField(output, "path"), m.agent) {
		return true, 0, errors.New("refusing to control a launchd job with a different program or plist")
	}
	pid, _ := strconv.Atoi(launchField(output, "pid"))
	return true, pid, nil
}

func sameAgentPath(loaded, expected string) bool {
	if loaded == expected {
		return true
	}
	resolved, err := filepath.EvalSymlinks(expected)
	return err == nil && loaded == resolved
}

func launchField(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), key+" = "); ok {
			return value
		}
	}
	return ""
}

func (m serviceManager) status(job serviceJob) (managedStatus, error) {
	status := managedStatus{State: "stopped", Address: job.address(), Log: filepath.Join(m.root, "server.log")}
	loaded, pid, err := m.registration(job)
	if err != nil || !loaded {
		return status, err
	}
	status.State, status.PID = "unready", pid
	ready := m.probe(job.address())
	if pid > 0 && ready.PID == pid {
		status.Version, status.BuildNumber = ready.Version, ready.BuildNumber
		if ready.Status == "ready" {
			status.State = ready.Status
		}
	}
	return status, nil
}

func (m serviceManager) start(job serviceJob) error {
	built, err := m.verifyRelease(job.program())
	if err != nil {
		return err
	}
	loaded, _, err := m.registration(job)
	if err != nil {
		return err
	}
	if !loaded {
		if err := m.free(job.address()); err != nil {
			return err
		}
		if _, err := m.launch("bootstrap", m.domain, m.agent); err != nil {
			return err
		}
	}
	if err := m.wait(func() (bool, error) {
		status, err := m.status(job)
		return status.State == "ready" && status.Version == built.Version && status.BuildNumber == built.BuildNumber, err
	}); err != nil {
		return fmt.Errorf("server did not become ready at %s; see %s: %w", job.address(), filepath.Join(m.root, "server.log"), err)
	}
	return nil
}

func (m serviceManager) stop(job serviceJob) error {
	loaded, pid, err := m.registration(job)
	if err != nil || !loaded {
		return err
	}
	_, stopErr := m.launch("bootout", m.domain+"/"+serviceLabel)
	err = m.wait(func() (bool, error) {
		loaded, _, err := m.registration(job)
		return !loaded && (pid <= 0 || errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)), err
	})
	if err != nil {
		return errors.Join(stopErr, err)
	}
	return nil
}

func (m serviceManager) wait(check func() (bool, error)) error {
	deadline := time.Now().Add(m.timeout)
	for {
		ok, err := check()
		if ok || err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("launchd state or server readiness timed out")
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (m serviceManager) upgrade(previous serviceJob, source string) error {
	previousBuild, err := m.verifyRelease(previous.program())
	if err != nil {
		return err
	}
	program, err := m.stage(source)
	if err != nil {
		return err
	}
	built, err := m.verifyRelease(program)
	if err != nil {
		return err
	}
	major, _, _ := version.MajorMinor(built.Version)
	previousMajor, _, _ := version.MajorMinor(previousBuild.Version)
	if major != previousMajor {
		return errors.New("automatic rollback requires the same server major version")
	}
	if program == previous.program() {
		return m.start(previous)
	}
	loaded, _, err := m.registration(previous)
	if err != nil {
		return err
	}
	if err := m.stop(previous); err != nil {
		return err
	}
	next := serviceJob{ProgramArguments: slices.Clone(previous.ProgramArguments)}
	next.ProgramArguments[0] = program
	if err := m.writeJob(next); err != nil {
		if loaded {
			err = errors.Join(err, m.start(previous))
		}
		return err
	}
	if err := m.start(next); err != nil {
		if stopErr := m.stop(next); stopErr != nil {
			return errors.Join(err, fmt.Errorf("could not unload the candidate for rollback: %w", stopErr))
		}
		if restoreErr := m.writeJob(previous); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("could not restore the previous LaunchAgent: %w", restoreErr))
		}
		if loaded {
			if restoreErr := m.start(previous); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("previous release restored on disk but failed readiness: %w", restoreErr))
			}
		}
		return fmt.Errorf("upgrade failed and the previous release was restored: %w", err)
	}
	return nil
}

func (m serviceManager) stage(source string) (string, error) {
	if source == "" {
		var err error
		source, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("candidate must be a regular executable file")
	}
	releases := filepath.Join(m.root, "releases")
	if err := os.MkdirAll(releases, 0700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(releases, ".candidate-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	candidate := filepath.Join(tmp, "tp")
	if err := copyExecutable(source, candidate); err != nil {
		return "", err
	}
	digest, err := executableDigest(candidate)
	if err != nil {
		return "", err
	}
	final := filepath.Join(releases, digest)
	target := filepath.Join(final, "tp")
	if _, err := os.Lstat(final); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(tmp, final); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return target, nil
}

func (m serviceManager) releaseDigest(path string) string {
	parent := filepath.Dir(path)
	digest := filepath.Base(parent)
	if filepath.Base(path) != "tp" || filepath.Dir(parent) != filepath.Join(m.root, "releases") || len(digest) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ""
	}
	return digest
}

func executableDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return "", errors.New("release must be a regular executable without group or world write access")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	return fmt.Sprintf("%x", hash.Sum(nil)), err
}

func (m serviceManager) verifyRelease(path string) (server.Readiness, error) {
	var built server.Readiness
	digest, err := executableDigest(path)
	if err != nil {
		return built, err
	}
	if digest != m.releaseDigest(path) {
		return built, errors.New("staged Teleport executable failed checksum verification")
	}
	data, err := serviceSystemCommand(path, "version", "-component", "server", "-json")
	if err != nil {
		return built, errors.New("staged executable did not report its server version")
	}
	if json.Unmarshal([]byte(data), &built) != nil || built.BuildNumber == "" {
		return built, errors.New("invalid staged executable version")
	}
	major, minor, ok := version.MajorMinor(built.Version)
	if !ok || major < 1 || major == 1 && minor < 2 {
		return built, errors.New("the service requires a tp build with readiness support (1.2.0 or later)")
	}
	return built, nil
}

func serviceSystemCommand(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	output := strings.TrimSpace(string(data))
	if err != nil {
		return output, fmt.Errorf("%s %s: %w (%s)", filepath.Base(path), args[0], err, output)
	}
	return output, nil
}

func checkServicePort(address string) error {
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("cannot reserve %s for Teleport: %w", address, err)
	}
	return listener.Close()
}

func probeService(address string) server.Readiness {
	var ready server.Readiness
	client := http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Get("http://" + address + "/readyz")
	if err != nil {
		return ready
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusServiceUnavailable ||
		json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&ready) != nil || ready.Version != res.Header.Get(version.Header) {
		return server.Readiness{}
	}
	if res.StatusCode != http.StatusOK {
		ready.Status = "unavailable"
	}
	return ready
}

func printServiceStatus(out io.Writer, machine bool, status managedStatus) error {
	if machine {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	}
	fmt.Fprintf(out, "Teleport server %s\n", status.State)
	if status.Address != "" {
		fmt.Fprintf(out, "Address  http://%s\nLog      %s\n", status.Address, status.Log)
	}
	if status.PID != 0 {
		fmt.Fprintf(out, "PID      %d\n", status.PID)
	}
	if status.Version != "" {
		fmt.Fprintf(out, "Version  %s (build %s)\n", status.Version, status.BuildNumber)
	}
	return nil
}
