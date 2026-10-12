package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const masterConfigBase = `
admin_token: "admin-secret"
enrollment_tokens: ["secret-token"]
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
`

func TestCheckConfigAcceptsValidConfigWithoutLocalShutdown(t *testing.T) {
	path := writeConfig(t, masterConfigBase)

	err := checkConfig(path)

	if err != nil {
		t.Fatalf("expected valid config to pass, got %v", err)
	}
}

func TestCheckConfigRejectsInvalidConfig(t *testing.T) {
	path := writeConfig(t, masterConfigBase+"auth_tokens: [\"x\"]\n")

	err := checkConfig(path)

	if err == nil || !strings.Contains(err.Error(), "auth_tokens") {
		t.Fatalf("expected config error, got %v", err)
	}
}

func TestCheckConfigRunsLocalShutdownSelfCheck(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shutdown")
	path := writeConfig(t, masterConfigBase+`
local_shutdown:
  enabled: true
  command: ["`+missing+`"]
`)

	err := checkConfig(path)

	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("expected self-check failure naming %s, got %v", missing, err)
	}
}

func TestCheckConfigSkipsSelfCheckInDryRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shutdown")
	path := writeConfig(t, masterConfigBase+`
dry_run: true
local_shutdown:
  enabled: true
  command: ["`+missing+`"]
`)

	if err := checkConfig(path); err != nil {
		t.Fatalf("expected dry-run to skip the self-check, got %v", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
