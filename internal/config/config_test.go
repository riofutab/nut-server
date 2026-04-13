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

func TestLoadMasterConfigDefaultsUPSLoggingSwitchOff(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
`)

	cfg, err := LoadMasterConfig(configPath)
	if err != nil {
		t.Fatalf("load master config: %v", err)
	}
	if cfg.LogUPSStatus {
		t.Fatalf("expected log_ups_status to default to false")
	}
}

func TestLoadMasterConfigParsesUPSLoggingSwitch(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
log_ups_status: true
snmp:
  target: "127.0.0.1"
`)

	cfg, err := LoadMasterConfig(configPath)
	if err != nil {
		t.Fatalf("load master config: %v", err)
	}
	if !cfg.LogUPSStatus {
		t.Fatalf("expected log_ups_status to be true")
	}
}

func TestLoadMasterConfigDefaultsLocalShutdown(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
`)

	cfg, err := LoadMasterConfig(configPath)
	if err != nil {
		t.Fatalf("load master config: %v", err)
	}
	if cfg.LocalShutdown.Enabled {
		t.Fatalf("expected local_shutdown.enabled to default to false")
	}
	if len(cfg.LocalShutdown.Command) != 3 {
		t.Fatalf("expected default local shutdown command, got %v", cfg.LocalShutdown.Command)
	}
	if cfg.LocalShutdown.Command[0] != "/sbin/shutdown" || cfg.LocalShutdown.Command[1] != "-h" || cfg.LocalShutdown.Command[2] != "now" {
		t.Fatalf("unexpected default local shutdown command: %v", cfg.LocalShutdown.Command)
	}
	if cfg.LocalShutdown.MaxWait.Duration.String() != "15m0s" {
		t.Fatalf("expected default local_shutdown.max_wait to be 15m, got %v", cfg.LocalShutdown.MaxWait.Duration)
	}
	if cfg.LocalShutdown.EmergencyRuntimeMinutes != 15 {
		t.Fatalf("expected default local_shutdown.emergency_runtime_minutes to be 15, got %d", cfg.LocalShutdown.EmergencyRuntimeMinutes)
	}
}

func TestLoadMasterConfigValidatesLocalShutdown(t *testing.T) {
	testCases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "enabled requires command",
			content: `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
local_shutdown:
  enabled: true
  command: []
`,
			wantErr: "local_shutdown.command",
		},
		{
			name: "max_wait must be positive",
			content: `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
local_shutdown:
  enabled: true
  max_wait: "0s"
`,
			wantErr: "local_shutdown.max_wait",
		},
		{
			name: "emergency runtime must be positive",
			content: `
listen_addr: ":9000"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
local_shutdown:
  enabled: true
  emergency_runtime_minutes: 0
`,
			wantErr: "local_shutdown.emergency_runtime_minutes",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := writeConfigFile(t, tc.content)
			_, err := LoadMasterConfig(configPath)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q validation error, got %v", tc.wantErr, err)
			}
		})
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
