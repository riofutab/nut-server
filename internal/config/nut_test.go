package config

import (
	"strings"
	"testing"
)

const masterFieldsWithoutUPS = `
admin_token: "admin-secret"
enrollment_tokens: ["secret-token"]
`

func loadMasterWithNUT(t *testing.T, nut string) (MasterConfig, error) {
	t.Helper()
	path := writeConfigFile(t, masterFieldsWithoutUPS+"nut:\n"+nut+defaultPolicy)
	return LoadMasterConfig(path)
}

func TestLoadMasterConfigNUTSourceAppliesDefaults(t *testing.T) {
	cfg, err := loadMasterWithNUT(t, "  host: 10.0.0.5\n  ups: myups\n")

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.UsesNUT() {
		t.Fatal("expected the nut source to be selected")
	}
	want := NUTConfig{Host: "10.0.0.5", Port: 3493, UPS: "myups", TimeoutSeconds: 2}
	if cfg.NUT != want {
		t.Fatalf("nut=%+v want %+v", cfg.NUT, want)
	}
	if cfg.NUT.Address() != "myups@10.0.0.5:3493" {
		t.Fatalf("address=%q", cfg.NUT.Address())
	}
}

func TestLoadMasterConfigValidatesNUT(t *testing.T) {
	cases := []struct {
		name    string
		nut     string
		wantErr string
	}{
		{name: "empty block", nut: "  {}\n", wantErr: "nut.host must not be empty"},
		{name: "ups required", nut: "  host: 10.0.0.5\n", wantErr: "nut.ups must not be empty"},
		{name: "ups name with space", nut: "  host: 10.0.0.5\n  ups: \"my ups\"\n", wantErr: "nut.ups"},
		{name: "host with newline", nut: "  host: \"a\\nb\"\n  ups: myups\n", wantErr: "nut.host"},
		{name: "timeout too large", nut: "  host: 10.0.0.5\n  ups: myups\n  timeout_seconds: 61\n", wantErr: "nut.timeout_seconds"},
		{name: "negative timeout", nut: "  host: 10.0.0.5\n  ups: myups\n  timeout_seconds: -1\n", wantErr: "nut.timeout_seconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadMasterWithNUT(t, tc.nut)

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadMasterConfigRequiresExactlyOneUPSSource(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		wantErr string
	}{
		{name: "neither", source: "", wantErr: "configure the UPS source"},
		{
			name:    "both",
			source:  "snmp:\n  target: 10.0.0.31\n  community: public\nnut:\n  host: 10.0.0.5\n  ups: myups\n",
			wantErr: "snmp and nut cannot both be configured",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, masterFieldsWithoutUPS+tc.source+defaultPolicy)

			_, err := LoadMasterConfig(path)

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadMasterConfigAcceptsLowBatteryWithNUT(t *testing.T) {
	path := writeConfigFile(t, masterFieldsWithoutUPS+`
nut:
  host: 10.0.0.5
  ups: myups
shutdown_policies:
  - name: lb
    when: { low_battery: true, charge_below: 20 }
`)

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.ShutdownPolicies[0].When.LowBattery {
		t.Fatal("expected when.low_battery to be parsed")
	}
}

func TestLoadMasterConfigValidatesLowBattery(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		when    string
		wantErr string
	}{
		{
			name:    "needs nut source",
			source:  "snmp:\n  target: 10.0.0.31\n  community: public\n",
			when:    "low_battery: true",
			wantErr: "shutdown_policies[0] (p).when.low_battery needs the nut source",
		},
		{
			name:    "explicit false",
			source:  "nut:\n  host: 10.0.0.5\n  ups: myups\n",
			when:    "low_battery: false",
			wantErr: "shutdown_policies[0] (p).when.low_battery must be true",
		},
		{
			name:    "not with ups_unreachable_for",
			source:  "nut:\n  host: 10.0.0.5\n  ups: myups\n",
			when:    "low_battery: true, ups_unreachable_for: 2m",
			wantErr: "when.ups_unreachable_for cannot be combined with charge_below, runtime_below or low_battery",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, masterFieldsWithoutUPS+tc.source+`
shutdown_policies:
  - name: p
    when: { `+tc.when+` }
`)

			_, err := LoadMasterConfig(path)

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
