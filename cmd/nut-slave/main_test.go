package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const slaveConfigBase = `
node_id: "slave-01"
master_addr: "127.0.0.1:9000"
token: "secret-token"
`

func TestCheckConfigRunsShutdownCommandSelfCheck(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shutdown")
	path := writeConfig(t, slaveConfigBase+`shutdown_command: ["`+missing+`"]`+"\n")

	err := checkConfig(path)

	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("expected self-check failure naming %s, got %v", missing, err)
	}
}

func TestCheckConfigSkipsSelfCheckInDryRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shutdown")
	path := writeConfig(t, slaveConfigBase+"dry_run: true\n"+`shutdown_command: ["`+missing+`"]`+"\n")

	if err := checkConfig(path); err != nil {
		t.Fatalf("expected dry-run to skip the self-check, got %v", err)
	}
}

func TestCheckConfigPassesWithExecutableShutdownCommand(t *testing.T) {
	command := filepath.Join(t.TempDir(), "poweroff")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write command: %v", err)
	}
	path := writeConfig(t, slaveConfigBase+`shutdown_command: ["`+command+`"]`+"\n")

	if err := checkConfig(path); err != nil {
		t.Fatalf("expected executable command to pass, got %v", err)
	}
}

func TestCheckConfigRejectsInvalidConfig(t *testing.T) {
	path := writeConfig(t, slaveConfigBase+"tokn: \"x\"\n")

	err := checkConfig(path)

	if err == nil || !strings.Contains(err.Error(), "tokn") {
		t.Fatalf("expected config error, got %v", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slave.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
