package master

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nut-server/internal/config"
	"nut-server/internal/security"
	"nut-server/internal/version"
)

func TestResolveMasterAddrPrefersPublicAddr(t *testing.T) {
	got := resolveMasterAddr("nut.example.com:9000", ":9000", "127.0.0.1:9001")
	if got != "nut.example.com:9000" {
		t.Fatalf("public_addr should win: got %q", got)
	}
}

func TestResolveMasterAddrFallsBackToHostHeaderWithListenPort(t *testing.T) {
	got := resolveMasterAddr("", ":9000", "10.0.0.10:9001")
	if got != "10.0.0.10:9000" {
		t.Fatalf("expected host from Host header + listen_addr port, got %q", got)
	}
}

func TestResolveMasterAddrHandlesHostHeaderWithoutPort(t *testing.T) {
	got := resolveMasterAddr("", "0.0.0.0:9000", "nut-master.internal")
	if got != "nut-master.internal:9000" {
		t.Fatalf("expected joined host:port, got %q", got)
	}
}

func TestResolveMasterAddrEmptyWhenNoSignal(t *testing.T) {
	if got := resolveMasterAddr("", "", ""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := resolveMasterAddr("", ":9000", ""); got != "" {
		t.Fatalf("expected empty when Host header missing, got %q", got)
	}
}

func TestIsValidInstallNodeID(t *testing.T) {
	good := []string{"slave-01", "db.alpha_1", "ABC", "9", strings.Repeat("a", maxInstallNodeIDLen)}
	for _, s := range good {
		if !isValidInstallNodeID(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	bad := []string{
		"",
		strings.Repeat("a", maxInstallNodeIDLen+1),
		"with space",
		"semi;rm -rf /",
		"$(whoami)",
		"a/b",
		"newline\n",
	}
	for _, s := range bad {
		if isValidInstallNodeID(s) {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func TestShellQuoteEscapesSingleQuote(t *testing.T) {
	got := shellQuote("ab'cd")
	want := `'ab'\''cd'`
	if got != want {
		t.Fatalf("shellQuote: got %q want %q", got, want)
	}
}

func setReleaseVersion(t *testing.T, v string) {
	t.Helper()
	orig := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = orig })
}

func newInstallServer(cfg config.MasterConfig) *Server {
	cfg.AdminToken = "a"
	cfg.EnrollmentTokens = []string{"shared-enroll-token"}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":9000"
	}
	return NewServer(cfg)
}

func postInstallScript(server *Server, nodeID, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/install-script", nil)
	req.Header.Set("Authorization", "Bearer a")
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	server.adminMux().ServeHTTP(rec, req)
	return rec
}

// heredocBody returns the lines between "<<'delimiter'" and "delimiter".
func heredocBody(t *testing.T, script, delimiter string) string {
	t.Helper()
	start := strings.Index(script, "<<'"+delimiter+"'\n")
	if start < 0 {
		t.Fatalf("script has no %s heredoc\nscript:\n%s", delimiter, script)
	}
	rest := script[start+len("<<'"+delimiter+"'\n"):]
	end := strings.Index(rest, "\n"+delimiter+"\n")
	if end < 0 {
		t.Fatalf("unterminated %s heredoc", delimiter)
	}
	return rest[:end]
}

func TestInstallScriptIssuesNodeTokenOnlyInsideHeredoc(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	server := newInstallServer(config.MasterConfig{PublicAddr: "nut.example:9000"})

	rec := postInstallScript(server, "db01", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type=%q want text/plain*", ct)
	}
	script := rec.Body.String()
	token := heredocBody(t, script, installTokenDelimiter)
	if len(token) != 64 {
		t.Fatalf("issued token length=%d want 64", len(token))
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, token) && line != token {
			t.Errorf("token must only appear as the heredoc body, found in line %q", line)
		}
	}
	if strings.Contains(script, "shared-enroll-token") {
		t.Error("script must not contain an enrollment token")
	}
	if strings.Contains(script, "--token ") {
		t.Error("script must not pass the token as an argument")
	}
	if !strings.Contains(script, "--token-file") {
		t.Errorf("script should pass --token-file\nscript:\n%s", script)
	}
	server.tokensMu.Lock()
	hash := server.nodeTokens["db01"]
	server.tokensMu.Unlock()
	if !security.MatchToken([]string{hash}, token) {
		t.Fatal("issued token should be stored as the node's token hash")
	}
	if meta, ok := server.directory.Get("db01"); !ok || !meta.Expected {
		t.Fatalf("node should be marked expected, got %+v", meta)
	}
}

func TestInstallScriptPinsReleaseTag(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	server := newInstallServer(config.MasterConfig{PublicAddr: "nut.example:9000", InstallRepo: "acme/nut"})

	script := postInstallScript(server, "db01", "").Body.String()

	wants := []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"'https://raw.githubusercontent.com/acme/nut/v0.6.0/scripts/install-online.sh'",
		"--role slave",
		"--version 'v0.6.0'",
		"--repo 'acme/nut'",
		"--node-id 'db01'",
		"--master-addr 'nut.example:9000'",
	}
	for _, w := range wants {
		if !strings.Contains(script, w) {
			t.Errorf("script missing %q\nscript:\n%s", w, script)
		}
	}
	if strings.Contains(script, "latest") || strings.Contains(script, "/master/") {
		t.Errorf("script must not reference latest or the master branch\nscript:\n%s", script)
	}
}

func TestInstallScriptRefusesDevBuild(t *testing.T) {
	setReleaseVersion(t, "dev")
	server := newInstallServer(config.MasterConfig{PublicAddr: "nut.example:9000"})

	rec := postInstallScript(server, "db01", "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "release") {
		t.Errorf("error should explain the missing release tag, got %q", rec.Body.String())
	}
	if server.isEnrolled("db01") {
		t.Fatal("a refused request must not issue a token")
	}
}

func TestInstallScriptWithoutTLSDisablesTLS(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	server := newInstallServer(config.MasterConfig{PublicAddr: "nut.example:9000"})

	script := postInstallScript(server, "db01", "").Body.String()

	if !strings.Contains(script, "--disable-tls") {
		t.Errorf("plaintext master should emit --disable-tls\nscript:\n%s", script)
	}
	if strings.Contains(script, "--tls-") {
		t.Errorf("plaintext master should not emit TLS options\nscript:\n%s", script)
	}
}

func TestInstallScriptWithTLSEmbedsCAAndServerName(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	certPEM := []byte("-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----\n")
	certFile := filepath.Join(t.TempDir(), "master.crt")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	server := newInstallServer(config.MasterConfig{
		PublicAddr: "nut.example:9000",
		TLS:        config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: "unused.key"},
	})

	script := postInstallScript(server, "db01", "").Body.String()

	if strings.Contains(script, "--disable-tls") {
		t.Fatalf("TLS master must never emit --disable-tls\nscript:\n%s", script)
	}
	if !strings.Contains(script, "--tls-server-name 'nut.example'") {
		t.Errorf("script should pin the server name\nscript:\n%s", script)
	}
	if !strings.Contains(script, "--tls-ca-file") {
		t.Errorf("script should pass --tls-ca-file\nscript:\n%s", script)
	}
	encoded := strings.ReplaceAll(heredocBody(t, script, installCADelimiter), "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode embedded CA: %v", err)
	}
	if string(decoded) != string(certPEM) {
		t.Fatalf("embedded CA=%q want %q", decoded, certPEM)
	}
}

func TestInstallScriptQuotesHostHeaderFallback(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	server := newInstallServer(config.MasterConfig{})

	script := postInstallScript(server, "db01", "evil'host:9001").Body.String()

	if !strings.Contains(script, `--master-addr 'evil'\''host:9000'`) {
		t.Errorf("master addr should be shell-quoted\nscript:\n%s", script)
	}
}

func TestInstallScriptUsesHostHeaderFallback(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	server := newInstallServer(config.MasterConfig{})

	script := postInstallScript(server, "db01", "10.0.0.10:9001").Body.String()

	if !strings.Contains(script, "--master-addr '10.0.0.10:9000'") {
		t.Errorf("expected host fallback to 10.0.0.10:9000\nscript:\n%s", script)
	}
}

func TestInstallScriptRejectsInvalidNodeID(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	server := newInstallServer(config.MasterConfig{PublicAddr: "nut.example:9000"})

	rec := postInstallScript(server, "bad$id", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInstallScriptRequiresAdminToken(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	server := newInstallServer(config.MasterConfig{PublicAddr: "nut.example:9000"})
	req := httptest.NewRequest(http.MethodPost, "/nodes/db01/install-script", nil)
	rec := httptest.NewRecorder()

	server.adminMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestInstallScriptEmbedsInstallCAFileInsteadOfMasterCertificate(t *testing.T) {
	setReleaseVersion(t, "v0.6.0")
	dir := t.TempDir()
	caPEM := []byte("-----BEGIN CERTIFICATE-----\nMIIBca\n-----END CERTIFICATE-----\n")
	caFile := filepath.Join(dir, "ca.crt")
	certFile := filepath.Join(dir, "master.crt")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nMIIBleaf\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	server := newInstallServer(config.MasterConfig{
		PublicAddr:    "nut.example:9000",
		InstallCAFile: caFile,
		TLS:           config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: "unused.key"},
	})

	script := postInstallScript(server, "db01", "").Body.String()

	encoded := strings.ReplaceAll(heredocBody(t, script, installCADelimiter), "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode embedded CA: %v", err)
	}
	if string(decoded) != string(caPEM) {
		t.Fatalf("embedded CA=%q want install_ca_file content %q", decoded, caPEM)
	}
}
