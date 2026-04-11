package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMasterConfigValidatesTLS(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
tls:
  enabled: true
  cert_file: "server.crt"
`)

	_, err := LoadMasterConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "tls.key_file") {
		t.Fatalf("expected tls.key_file validation error, got %v", err)
	}
}

func TestLoadSlaveConfigValidatesTLS(t *testing.T) {
	configPath := writeConfigFile(t, `
node_id: "slave-01"
master_addr: "127.0.0.1:9000"
token: "secret-token"
tls:
  cert_file: "client.crt"
`)

	_, err := LoadSlaveConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "tls.key_file") {
		t.Fatalf("expected tls.key_file validation error, got %v", err)
	}
}

func TestLoadSlaveConfigRejectsCAWithInsecureSkipVerify(t *testing.T) {
	configPath := writeConfigFile(t, `
node_id: "slave-01"
master_addr: "127.0.0.1:9000"
token: "secret-token"
tls:
  ca_file: "ca.pem"
  insecure_skip_verify: true
`)

	_, err := LoadSlaveConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected incompatible tls config error, got %v", err)
	}
}

func TestLoadMasterConfigDisabledTLSIgnoresCertificateFields(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
tls:
  enabled: true
  disabled: true
  cert_file: "server.crt"
  require_client_cert: true
`)

	cfg, err := LoadMasterConfig(configPath)
	if err != nil {
		t.Fatalf("expected disabled TLS to bypass certificate validation, got %v", err)
	}
	if cfg.TLS.EnabledForServer() {
		t.Fatalf("expected disabled TLS to force plain listener")
	}
}

func TestLoadSlaveConfigDisabledTLSIgnoresCertificateFields(t *testing.T) {
	configPath := writeConfigFile(t, `
node_id: "slave-01"
master_addr: "127.0.0.1:9000"
token: "secret-token"
tls:
  enabled: true
  disabled: true
  cert_file: "client.crt"
  ca_file: "ca.pem"
  insecure_skip_verify: true
`)

	cfg, err := LoadSlaveConfig(configPath)
	if err != nil {
		t.Fatalf("expected disabled TLS to bypass client TLS validation, got %v", err)
	}
	if cfg.TLS.EnabledForClient() {
		t.Fatalf("expected disabled TLS to force plain dialing")
	}
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
