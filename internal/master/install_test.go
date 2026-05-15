package master

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nut-server/internal/config"
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

func TestRenderInstallScriptShape(t *testing.T) {
	body := renderInstallScript("riofutab/nut-server", "v0.2.1", "slave-01", "10.0.0.10:9000", "tok-abc")
	wants := []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"https://github.com/riofutab/nut-server/releases/download/v0.2.1/install-online.sh",
		"--role slave",
		"--version 'v0.2.1'",
		"--node-id 'slave-01'",
		"--master-addr '10.0.0.10:9000'",
		"--token 'tok-abc'",
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("script missing %q\nbody:\n%s", w, body)
		}
	}
}

func TestRenderInstallScriptLatestUsesRawGithub(t *testing.T) {
	body := renderInstallScript("riofutab/nut-server", "latest", "slave-01", "10.0.0.10:9000", "tok")
	if !strings.Contains(body, "https://raw.githubusercontent.com/riofutab/nut-server/master/scripts/install-online.sh") {
		t.Errorf("latest version should use raw github URL\nbody:\n%s", body)
	}
}

func TestRenderInstallScriptQuotesMaliciousNodeID(t *testing.T) {
	body := renderInstallScript("repo", "v1", "ok'; curl evil", "host:9000", "tok")
	if !strings.Contains(body, `'ok'\''; curl evil'`) {
		t.Errorf("malicious node id should be shell-quoted\nbody:\n%s", body)
	}
}

func TestHandleInstallSlaveRequiresNodeID(t *testing.T) {
	server := NewServer(config.MasterConfig{
		AdminToken: "a",
		AuthTokens: []string{"slave-tok"},
		ListenAddr: ":9000",
	})
	req := httptest.NewRequest(http.MethodGet, "/install/slave", nil)
	rec := httptest.NewRecorder()
	server.handleInstallSlave(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleInstallSlaveRejectsInvalidNodeID(t *testing.T) {
	server := NewServer(config.MasterConfig{
		AdminToken: "a",
		AuthTokens: []string{"slave-tok"},
		ListenAddr: ":9000",
	})
	req := httptest.NewRequest(http.MethodGet, "/install/slave?node_id=bad%20id", nil)
	rec := httptest.NewRecorder()
	server.handleInstallSlave(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleInstallSlaveRefusesWithoutAuthTokens(t *testing.T) {
	server := NewServer(config.MasterConfig{
		AdminToken: "a",
		ListenAddr: ":9000",
		PublicAddr: "nut.example:9000",
	})
	req := httptest.NewRequest(http.MethodGet, "/install/slave?node_id=db01", nil)
	rec := httptest.NewRecorder()
	server.handleInstallSlave(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleInstallSlaveReturnsScriptAndPreRegisters(t *testing.T) {
	server := NewServer(config.MasterConfig{
		AdminToken: "a",
		AuthTokens: []string{"slave-tok-AAA", "slave-tok-BBB"},
		ListenAddr: ":9000",
		PublicAddr: "nut.example:9000",
	})
	req := httptest.NewRequest(http.MethodGet, "/install/slave?node_id=db01", nil)
	rec := httptest.NewRecorder()
	server.handleInstallSlave(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type=%q want text/plain*", ct)
	}
	body := rec.Body.String()
	wants := []string{
		"--node-id 'db01'",
		"--master-addr 'nut.example:9000'",
		"--token 'slave-tok-AAA'",
		"--role slave",
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q\nbody:\n%s", w, body)
		}
	}

	meta, ok := server.directory.Get("db01")
	if !ok {
		t.Fatal("node should be pre-registered in directory")
	}
	if !meta.Expected {
		t.Errorf("node should be marked Expected, got: %+v", meta)
	}
}

func TestHandleInstallSlaveUsesHostHeaderFallback(t *testing.T) {
	server := NewServer(config.MasterConfig{
		AdminToken: "a",
		AuthTokens: []string{"tok"},
		ListenAddr: ":9000",
	})
	req := httptest.NewRequest(http.MethodGet, "/install/slave?node_id=db01", nil)
	req.Host = "10.0.0.10:9001"
	rec := httptest.NewRecorder()
	server.handleInstallSlave(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "--master-addr '10.0.0.10:9000'") {
		t.Errorf("expected host fallback to 10.0.0.10:9000\nbody:\n%s", rec.Body.String())
	}
}
