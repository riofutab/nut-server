package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMasterConfigValidatesTLS(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
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

func TestLoadMasterConfigRejectsDisabledTLSWithCertFields(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
tls:
  enabled: true
  disabled: true
  cert_file: "server.crt"
  require_client_cert: true
`)

	if _, err := LoadMasterConfig(configPath); err == nil {
		t.Fatalf("expected tls.disabled combined with cert fields to be rejected")
	}
}

func TestLoadMasterConfigDisabledTLSAloneForcesPlaintext(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
tls:
  disabled: true
`)

	cfg, err := LoadMasterConfig(configPath)
	if err != nil {
		t.Fatalf("expected lone tls.disabled to load, got %v", err)
	}
	if cfg.TLS.EnabledForServer() {
		t.Fatalf("expected disabled TLS to force plain listener")
	}
}

func TestLoadMasterConfigDefaultsUPSLoggingSwitchOff(t *testing.T) {
	configPath := writeConfigFile(t, `
listen_addr: ":9000"
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
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
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
log_ups_status: true
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
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
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
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
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
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
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
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
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
shutdown_policies:
  - name: on-battery
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

func TestLoadMasterConfigUnreachableAfterDefaultsToMaxWait(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+defaultPolicy+`
local_shutdown:
  max_wait: "7m"
`)

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LocalShutdown.UnreachableAfter.Duration != 7*time.Minute {
		t.Fatalf("unreachable_after=%v want 7m", cfg.LocalShutdown.UnreachableAfter.Duration)
	}
}

func TestLoadMasterConfigKeepsExplicitUnreachableAfter(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+defaultPolicy+`
local_shutdown:
  unreachable_after: "3m"
`)

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LocalShutdown.UnreachableAfter.Duration != 3*time.Minute {
		t.Fatalf("unreachable_after=%v want 3m", cfg.LocalShutdown.UnreachableAfter.Duration)
	}
}

func TestLoadMasterConfigRejectsNegativeUnreachableAfter(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+defaultPolicy+`
local_shutdown:
  unreachable_after: "-1m"
`)

	_, err := LoadMasterConfig(path)

	if err == nil || !strings.Contains(err.Error(), "local_shutdown.unreachable_after") {
		t.Fatalf("expected unreachable_after validation error, got %v", err)
	}
}

func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	cases := []struct {
		name    string
		load    func(string) error
		content string
		wantKey string
	}{
		{
			name:    "master top level",
			load:    loadMaster,
			content: minimalMasterFields + defaultPolicy + "poll_intervall: \"5s\"\n",
			wantKey: "poll_intervall",
		},
		{
			name: "master snmp",
			load: loadMaster,
			content: `
admin_token: "admin-secret"
enrollment_tokens: ["secret-token"]
snmp:
  target: "127.0.0.1"
  comunity: "public"
` + defaultPolicy,
			wantKey: "comunity",
		},
		{
			name: "master policy when",
			load: loadMaster,
			content: minimalMasterFields + `
shutdown_policies:
  - name: low
    when:
      charge_bellow: 20
`,
			wantKey: "charge_bellow",
		},
		{
			name: "master local_shutdown",
			load: loadMaster,
			content: minimalMasterFields + defaultPolicy + `
local_shutdown:
  enabled: true
  max_wiat: "5m"
`,
			wantKey: "max_wiat",
		},
		{
			name: "slave top level",
			load: loadSlave,
			content: `
node_id: "slave-01"
master_addr: "127.0.0.1:9000"
token: "secret-token"
shutdown_comand: ["/sbin/poweroff"]
`,
			wantKey: "shutdown_comand",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, tc.content)

			err := tc.load(path)

			if err == nil || !strings.Contains(err.Error(), tc.wantKey) {
				t.Fatalf("expected error naming %q, got %v", tc.wantKey, err)
			}
		})
	}
}

func TestLoadMasterConfigRejectsRemovedKeysWithUpgradeHint(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantKey string
	}{
		{
			name:    "auth_tokens",
			content: minimalMasterFields + defaultPolicy + "auth_tokens: [\"secret-token\"]\n",
			wantKey: "auth_tokens",
		},
		{
			name:    "shutdown_policy",
			content: minimalMasterFields + "shutdown_policy:\n  min_battery_charge: 30\n",
			wantKey: "shutdown_policy",
		},
		{
			name: "when.on_battery",
			content: minimalMasterFields + `
shutdown_policies:
  - name: ok
  - name: legacy
    when:
      on_battery: true
`,
			wantKey: "shutdown_policies[1].when.on_battery",
		},
		{
			name: "snmp.output_source_oid",
			content: `
admin_token: "admin-secret"
enrollment_tokens: ["secret-token"]
snmp:
  target: "127.0.0.1"
  output_source_oid: ".1.3.6.1.2.1.33.1.4.1.0"
` + defaultPolicy,
			wantKey: "snmp.output_source_oid",
		},
		{
			name: "snmp.charge_oid",
			content: `
admin_token: "admin-secret"
enrollment_tokens: ["secret-token"]
snmp:
  target: "127.0.0.1"
  charge_oid: ".1.3.6.1.2.1.33.1.2.4.0"
` + defaultPolicy,
			wantKey: "snmp.charge_oid",
		},
		{
			name: "snmp.runtime_minutes_oid",
			content: `
admin_token: "admin-secret"
enrollment_tokens: ["secret-token"]
snmp:
  target: "127.0.0.1"
  runtime_minutes_oid: ".1.3.6.1.2.1.33.1.2.3.0"
` + defaultPolicy,
			wantKey: "snmp.runtime_minutes_oid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, tc.content)

			_, err := LoadMasterConfig(path)

			if err == nil || !strings.Contains(err.Error(), tc.wantKey) || !strings.Contains(err.Error(), "docs/upgrade.md") {
				t.Fatalf("expected error naming %q and docs/upgrade.md, got %v", tc.wantKey, err)
			}
		})
	}
}

func TestLoadSlaveConfigRejectsDisabledTLSWithCertFields(t *testing.T) {
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

	if _, err := LoadSlaveConfig(configPath); err == nil {
		t.Fatalf("expected tls.disabled combined with cert fields to be rejected")
	}
}

func TestLoadSlaveConfigDisabledTLSAloneForcesPlaintext(t *testing.T) {
	configPath := writeConfigFile(t, `
node_id: "slave-01"
master_addr: "127.0.0.1:9000"
token: "secret-token"
tls:
  disabled: true
`)

	cfg, err := LoadSlaveConfig(configPath)
	if err != nil {
		t.Fatalf("expected lone tls.disabled to load, got %v", err)
	}
	if cfg.TLS.EnabledForClient() {
		t.Fatalf("expected disabled TLS to force plain dialing")
	}
}

func loadMaster(path string) error {
	_, err := LoadMasterConfig(path)
	return err
}

func loadSlave(path string) error {
	_, err := LoadSlaveConfig(path)
	return err
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadMasterConfigRequiresAdminTLSCertAndKeyTogether(t *testing.T) {
	cases := map[string]string{
		"cert only": "admin_tls:\n  cert_file: \"admin.crt\"\n",
		"key only":  "admin_tls:\n  key_file: \"admin.key\"\n",
	}
	for name, adminTLS := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfigFile(t, minimalMasterFields+defaultPolicy+adminTLS)

			_, err := LoadMasterConfig(path)

			if err == nil || !strings.Contains(err.Error(), "admin_tls.cert_file and admin_tls.key_file must be set together") {
				t.Fatalf("expected admin_tls pairing error, got %v", err)
			}
		})
	}
}

func TestLoadMasterConfigAcceptsAdminTLSPairOrNeither(t *testing.T) {
	cases := map[string]string{
		"neither": "",
		"both":    "admin_tls:\n  cert_file: \"admin.crt\"\n  key_file: \"admin.key\"\n",
	}
	for name, adminTLS := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfigFile(t, minimalMasterFields+defaultPolicy+adminTLS)

			if _, err := LoadMasterConfig(path); err != nil {
				t.Fatalf("expected admin_tls %s to load, got %v", name, err)
			}
		})
	}
}

func TestExampleConfigsLoadWithPlaceholdersReplaced(t *testing.T) {
	replacer := strings.NewReplacer(
		"replace-with-strong-admin-token", "example-admin-secret",
		"replace-with-strong-token", "example-fleet-secret",
	)
	cases := []struct {
		file string
		load func(string) error
	}{
		{file: "master.example.yaml", load: loadMaster},
		{file: "slave.example.yaml", load: loadSlave},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join("..", "..", "configs", tc.file))
			if err != nil {
				t.Fatalf("read example: %v", err)
			}
			path := filepath.Join(t.TempDir(), tc.file)
			if err := os.WriteFile(path, []byte(replacer.Replace(string(content))), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			err = tc.load(path)

			if err != nil {
				t.Fatalf("example config %s does not load: %v", tc.file, err)
			}
		})
	}
}
