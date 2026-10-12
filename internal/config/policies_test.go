package config

import (
	"strings"
	"testing"
	"time"
)

const minimalMasterFields = `
listen_addr: ":9000"
admin_token: "admin-secret"
enrollment_tokens:
  - "secret-token"
snmp:
  target: "127.0.0.1"
`

const defaultPolicy = `
shutdown_policies:
  - name: on-battery
`

func TestLoadMasterConfigLoadsExplicitPolicies(t *testing.T) {
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
    duration: 30s
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
	if cfg.ShutdownPolicies[0].Reason != "dev early" {
		t.Errorf("reason=%q want dev early", cfg.ShutdownPolicies[0].Reason)
	}
	if cfg.ShutdownPolicies[1].Reason != "policy critical-all triggered" {
		t.Errorf("missing reason should default; got %q", cfg.ShutdownPolicies[1].Reason)
	}
	if cfg.ShutdownPolicies[1].Duration.Duration != 30*time.Second {
		t.Errorf("duration=%v want 30s", cfg.ShutdownPolicies[1].Duration.Duration)
	}
}

func TestLoadMasterConfigRequiresAtLeastOnePolicy(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields)

	_, err := LoadMasterConfig(path)

	if err == nil || !strings.Contains(err.Error(), "shutdown_policies") {
		t.Fatalf("expected error pointing at shutdown_policies, got %v", err)
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

func TestLoadMasterConfigAcceptsEmptyWhenAsOnBattery(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: on-battery
    when: {}
`)

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("expected empty when to be valid, got %v", err)
	}
	if got := cfg.ShutdownPolicies[0].When; got != (ShutdownPolicyWhen{}) {
		t.Fatalf("when=%+v want zero", got)
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

func TestLoadMasterConfigAcceptsUPSUnreachableForAlone(t *testing.T) {
	path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: lost
    when:
      ups_unreachable_for: 2m
`)

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.ShutdownPolicies[0].When.UPSUnreachableFor.Duration; got != 2*time.Minute {
		t.Fatalf("ups_unreachable_for=%v want 2m", got)
	}
}

func TestLoadMasterConfigValidatesPolicyFields(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		wantErr string
	}{
		{name: "charge_below above 100", policy: "when: { charge_below: 101 }", wantErr: "when.charge_below"},
		{name: "charge_below negative", policy: "when: { charge_below: -1 }", wantErr: "when.charge_below"},
		{name: "runtime_below negative", policy: "when: { runtime_below: -1 }", wantErr: "when.runtime_below"},
		{name: "duration negative", policy: "duration: -1s", wantErr: "duration"},
		{name: "ups_unreachable_for negative", policy: "when: { ups_unreachable_for: -1s }", wantErr: "when.ups_unreachable_for"},
		{
			name:    "ups_unreachable_for with charge_below",
			policy:  "when: { ups_unreachable_for: 2m, charge_below: 20 }",
			wantErr: "when.ups_unreachable_for",
		},
		{
			name:    "ups_unreachable_for with runtime_below",
			policy:  "when: { ups_unreachable_for: 2m, runtime_below: 5 }",
			wantErr: "when.ups_unreachable_for",
		},
		{
			name:    "ups_unreachable_for with duration",
			policy:  "when: { ups_unreachable_for: 2m }\n    duration: 30s",
			wantErr: "when.ups_unreachable_for",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: p
    `+tc.policy+"\n")

			_, err := LoadMasterConfig(path)

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadMasterConfigAcceptsChargeBelowBounds(t *testing.T) {
	for _, charge := range []string{"0", "100"} {
		t.Run(charge, func(t *testing.T) {
			path := writeConfigFile(t, minimalMasterFields+`
shutdown_policies:
  - name: p
    when: { charge_below: `+charge+` }
`)

			if _, err := LoadMasterConfig(path); err != nil {
				t.Fatalf("expected charge_below %s to be valid, got %v", charge, err)
			}
		})
	}
}
