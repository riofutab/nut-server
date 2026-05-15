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
