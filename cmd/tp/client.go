package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"teleport/internal/app"
	"teleport/internal/version"
)

func readCredentials(endpoint, path string) (credentials, error) {
	creds := credentials{Server: endpoint}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return creds, nil
	}
	if err != nil {
		return creds, errors.New("cannot read client credentials; check -config")
	}
	if err := json.Unmarshal(body, &creds); err != nil {
		return creds, errors.New("invalid client credential JSON; inspect the file selected by -config")
	}
	if creds.Server != endpoint {
		return creds, errors.New("credential server differs; sign in for this server")
	}
	return creds, nil
}

type apiClient struct {
	endpoint      string
	token         string
	diagnostics   io.Writer
	serverVersion string
	checked       bool
}

func (api *apiClient) operation(operation string, data []byte) ([]byte, error) {
	if len(data) > 1<<20 {
		return nil, errors.New("complete JSON request exceeds 1 MiB")
	}
	if !json.Valid(data) {
		return nil, errors.New("-data must be valid JSON")
	}
	req, err := http.NewRequest(http.MethodPost, api.endpoint+"/api/tools/"+operation, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if api.token != "" {
		req.Header.Set("Authorization", "Bearer "+api.token)
	}
	return api.send(req, operation == "setupGoogleSSO")
}

func (api *apiClient) get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, api.endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	return api.send(req, false)
}

func (api *apiClient) send(req *http.Request, secret bool) ([]byte, error) {
	client := http.Client{Timeout: 30 * time.Second}
	if secret {
		host := req.URL.Hostname()
		ip := net.ParseIP(host)
		loopback := strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
		if req.URL.Scheme != "https" && (req.URL.Scheme != "http" || !loopback) {
			return nil, errors.New("Google sign-in setup requires HTTPS or local loopback")
		}
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("server unavailable; run tp serve (no database fallback): %w", err)
	}
	defer resp.Body.Close()
	if secret && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, errors.New("Google sign-in setup requires the server's final URL")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 {
		return nil, errors.New("response exceeds 4 MiB; reduce the page limit and retry")
	}
	api.checkVersion(resp.Header.Get(version.Header))
	if resp.StatusCode >= 400 {
		var result struct {
			Error app.Error `json:"error"`
		}
		json.Unmarshal(body, &result)
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
			if api.serverVersion == "" {
				return nil, fmt.Errorf("%s: %s (server version unavailable)", resp.Status, result.Error.Message)
			}
			return nil, fmt.Errorf("%s: %s (server version %s)", resp.Status, result.Error.Message, api.serverVersion)
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, result.Error.Message)
	}
	return body, nil
}

func (api *apiClient) checkVersion(advertisement string) {
	if api.checked {
		return
	}
	api.checked = true
	serverMajor, serverMinor, ok := version.MajorMinor(advertisement)
	if !ok {
		advertisement = api.legacyVersion()
		serverMajor, serverMinor, ok = version.MajorMinor(advertisement)
	}
	if !ok {
		return
	}
	api.serverVersion = advertisement
	cliMajor, cliMinor, _ := version.MajorMinor(version.CLI)
	if cliMajor == serverMajor && cliMinor == serverMinor {
		return
	}
	older := "CLI"
	if serverMajor < cliMajor || serverMajor == cliMajor && serverMinor < cliMinor {
		older = "server"
	}
	fmt.Fprintf(api.diagnostics, "Warning: tp CLI %s and server %s differ in major/minor version. Upgrade the %s.\n", version.CLI, advertisement, older)
}

func (api *apiClient) legacyVersion() string {
	client := http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(api.endpoint + "/healthz")
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	var health struct {
		Version string `json:"version"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health) != nil {
		return ""
	}
	return health.Version
}
