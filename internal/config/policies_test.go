package config

import (
	"strings"
	"testing"
)

const minimalMasterFields = `
listen_addr: ":9000"
admin_token: "admin-secret"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
`

func TestLoadMasterConfigSynthesizesDefaultPolicyFromLegacyFields(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policy:
  require_on_battery: true
  min_battery_charge: 25
  min_runtime_minutes: 10
  shutdown_reason: "legacy"
`)
	cfg, err := LoadMasterConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.ShutdownPolicies) != 1 {
		t.Fatalf("expected 1 synthesized policy, got %d", len(cfg.ShutdownPolicies))
	}
	got := cfg.ShutdownPolicies[0]
	if got.Name != "default" {
		t.Errorf("name=%q want default", got.Name)
	}
	if got.When.OnBattery == nil || !*got.When.OnBattery {
		t.Errorf("OnBattery=%v want true", got.When.OnBattery)
	}
	if got.When.ChargeBelow != 25 || got.When.RuntimeBelow != 10 {
		t.Errorf("when=%+v", got.When)
	}
	if !got.Target.All {
		t.Errorf("target should default to All")
	}
	if got.Reason != "legacy" {
		t.Errorf("reason=%q", got.Reason)
	}
}

func TestLoadMasterConfigUsesExplicitPoliciesWhenProvided(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: dev-early
    when:
      charge_below: 50
    target:
      tags: [dev]
    reason: "dev early"
  - name: critical-all
    when:
      runtime_below: 5
    target:
      all: true
`)
	cfg, err := LoadMasterConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.ShutdownPolicies) != 2 {
		t.Fatalf("expected 2 policies, got %d", len(cfg.ShutdownPolicies))
	}
	if cfg.ShutdownPolicies[1].Reason != "policy critical-all triggered" {
		t.Errorf("missing reason should default; got %q", cfg.ShutdownPolicies[1].Reason)
	}
}

func TestLoadMasterConfigRejectsEmptyPolicyName(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - when:
      charge_below: 10
`)
	_, err := LoadMasterConfig(path)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected name error, got %v", err)
	}
}

func TestLoadMasterConfigRejectsDuplicatePolicyName(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: a
    when: { charge_below: 10 }
  - name: a
    when: { charge_below: 20 }
`)
	_, err := LoadMasterConfig(path)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestLoadMasterConfigRejectsEmptyWhen(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: empty
    when: {}
    target: { all: true }
`)
	_, err := LoadMasterConfig(path)
	if err == nil || !strings.Contains(err.Error(), "when condition") {
		t.Fatalf("expected when validation error, got %v", err)
	}
}

func TestLoadMasterConfigDefaultsTargetAllWhenUnset(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: a
    when: { charge_below: 10 }
`)
	cfg, err := LoadMasterConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.ShutdownPolicies[0].Target.All {
		t.Errorf("missing target should default to All=true")
	}
}

func TestLoadMasterConfigLegacyPolicyWithoutBatteryRequirementLeavesOnBatteryUnset(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policy:
  require_on_battery: false
  min_battery_charge: 25
`)

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.ShutdownPolicies[0].When.OnBattery; got != nil {
		t.Fatalf("OnBattery=%v want nil (require_on_battery:false must not mean 'on mains')", *got)
	}
	if cfg.ShutdownPolicies[0].When.ChargeBelow != 25 {
		t.Fatalf("ChargeBelow=%d want 25", cfg.ShutdownPolicies[0].When.ChargeBelow)
	}
}

func TestLoadMasterConfigLegacyPolicyOnBatteryAloneIsValid(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policy:
  require_on_battery: true
`)

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.ShutdownPolicies[0].When.OnBattery; got == nil || !*got {
		t.Fatalf("OnBattery=%v want true", got)
	}
}

func TestLoadMasterConfigWithoutAnyPolicyConditionFails(t *testing.T) {
	cases := map[string]string{
		"no policy at all": minimalMasterFields,
		"legacy policy without conditions": minimalMasterFields + `
shutdown_policy:
  require_on_battery: false
  shutdown_reason: "x"
`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfigFile(t, content)

			_, err := LoadMasterConfig(path)

			if err == nil || !strings.Contains(err.Error(), "shutdown_policies") {
				t.Fatalf("expected error pointing at shutdown_policies, got %v", err)
			}
		})
	}
}

func TestLoadMasterConfigRejectsOnBatteryFalse(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: mains
    when:
      on_battery: false
      charge_below: 50
`)

	_, err := LoadMasterConfig(path)

	if err == nil || !strings.Contains(err.Error(), "on_battery") || !strings.Contains(err.Error(), "on battery") {
		t.Fatalf("expected on_battery:false rejection, got %v", err)
	}
}

func TestLoadMasterConfigAcceptsOnBatteryTrue(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: battery
    when:
      on_battery: true
`)

	if _, err := LoadMasterConfig(path); err != nil {
		t.Fatalf("expected on_battery:true to be valid, got %v", err)
	}
}

func TestLoadMasterConfigSNMPVersion(t *testing.T) {
	cases := []struct {
		name        string
		version     string
		wantVersion string
		wantErr     string
	}{
		{name: "empty defaults to 2c", version: `""`, wantVersion: "2c"},
		{name: "v1", version: `"1"`, wantVersion: "1"},
		{name: "v2c", version: `"2c"`, wantVersion: "2c"},
		{name: "v3 not supported", version: `"3"`, wantErr: "SNMPv3 is not supported yet"},
		{name: "unknown", version: `"2"`, wantErr: "snmp.version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, `
admin_token: "admin-secret"
auth_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
  version: `+tc.version+`
shutdown_policy:
  require_on_battery: true
`)

			cfg, err := LoadMasterConfig(path)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.SNMP.Version != tc.wantVersion {
				t.Fatalf("version=%q want %q", cfg.SNMP.Version, tc.wantVersion)
			}
		})
	}
}
