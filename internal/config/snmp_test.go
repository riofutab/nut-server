package config

import (
	"reflect"
	"strings"
	"testing"
)

func loadMasterWithSNMP(t *testing.T, snmp string) (MasterConfig, error) {
	t.Helper()
	path := writeConfigFile(t, `
admin_token: "admin-secret"
enrollment_tokens: ["secret-token"]
snmp:
  target: "127.0.0.1"
`+snmp+defaultPolicy)
	return LoadMasterConfig(path)
}

func TestLoadMasterConfigSNMPVersion(t *testing.T) {
	cases := []struct {
		name        string
		snmp        string
		wantVersion string
		wantErr     string
	}{
		{name: "empty defaults to 2c", snmp: `  version: ""`, wantVersion: "2c"},
		{name: "v1", snmp: `  version: "1"`, wantVersion: "1"},
		{name: "v2c", snmp: `  version: "2c"`, wantVersion: "2c"},
		{name: "3", snmp: "  version: \"3\"\n  username: ups\n  security_level: noAuthNoPriv", wantVersion: "3"},
		{name: "v3 normalizes to 3", snmp: "  version: v3\n  username: ups\n  security_level: noAuthNoPriv", wantVersion: "3"},
		{name: "unknown", snmp: `  version: "2"`, wantErr: "snmp.version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadMasterWithSNMP(t, tc.snmp+"\n")

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

func TestLoadMasterConfigAcceptsSNMPv3AuthPriv(t *testing.T) {
	cfg, err := loadMasterWithSNMP(t, `
  version: "3"
  username: ups-reader
  security_level: authPriv
  auth_protocol: SHA256
  auth_passphrase: auth-pass-123
  priv_protocol: AES
  priv_passphrase: priv-pass-123
`)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SNMP.Username != "ups-reader" || cfg.SNMP.SecurityLevel != SNMPSecurityAuthPriv {
		t.Fatalf("unexpected usm settings %+v", cfg.SNMP)
	}
}

func TestLoadMasterConfigValidatesSNMPv3(t *testing.T) {
	cases := []struct {
		name    string
		snmp    string
		wantErr string
	}{
		{
			name:    "username required",
			snmp:    "version: \"3\"\nsecurity_level: noAuthNoPriv",
			wantErr: "snmp.username",
		},
		{
			name:    "security level required",
			snmp:    "version: \"3\"\nusername: u",
			wantErr: "snmp.security_level",
		},
		{
			name:    "unknown security level",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authpriv",
			wantErr: "snmp.security_level",
		},
		{
			name:    "auth protocol required for authNoPriv",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authNoPriv\nauth_passphrase: 12345678",
			wantErr: "snmp.auth_protocol",
		},
		{
			name:    "unknown auth protocol",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authNoPriv\nauth_protocol: SHA1\nauth_passphrase: 12345678",
			wantErr: "snmp.auth_protocol",
		},
		{
			name:    "short auth passphrase",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authNoPriv\nauth_protocol: SHA\nauth_passphrase: 1234567",
			wantErr: "snmp.auth_passphrase",
		},
		{
			name:    "priv protocol required for authPriv",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authPriv\nauth_protocol: SHA\nauth_passphrase: 12345678\npriv_passphrase: 12345678",
			wantErr: "snmp.priv_protocol",
		},
		{
			name:    "unknown priv protocol",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authPriv\nauth_protocol: SHA\nauth_passphrase: 12345678\npriv_protocol: AES128\npriv_passphrase: 12345678",
			wantErr: "snmp.priv_protocol",
		},
		{
			name:    "short priv passphrase",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authPriv\nauth_protocol: SHA\nauth_passphrase: 12345678\npriv_protocol: AES\npriv_passphrase: short",
			wantErr: "snmp.priv_passphrase",
		},
		{
			name:    "auth settings unused by noAuthNoPriv",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: noAuthNoPriv\nauth_protocol: SHA\nauth_passphrase: 12345678",
			wantErr: "snmp.auth_protocol",
		},
		{
			name:    "priv settings unused by authNoPriv",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: authNoPriv\nauth_protocol: SHA\nauth_passphrase: 12345678\npriv_protocol: AES\npriv_passphrase: 12345678",
			wantErr: "snmp.priv_protocol",
		},
		{
			name:    "community on v3",
			snmp:    "version: \"3\"\nusername: u\nsecurity_level: noAuthNoPriv\ncommunity: public",
			wantErr: "snmp.community",
		},
		{
			name:    "usm settings on v2c",
			snmp:    "version: \"2c\"\nusername: u",
			wantErr: "snmp.username",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadMasterWithSNMP(t, indent(tc.snmp))

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestResolveSNMPVarsAppliesProfilePresets(t *testing.T) {
	cases := []struct {
		profile       string
		wantOnBattery SNMPStateVar
		wantCharge    SNMPValueVar
		wantRuntime   SNMPValueVar
	}{
		{
			profile:       "",
			wantOnBattery: SNMPStateVar{OID: ".1.3.6.1.2.1.33.1.4.1.0", Values: []int{5}},
			wantCharge:    SNMPValueVar{OID: ".1.3.6.1.2.1.33.1.2.4.0"},
			wantRuntime:   SNMPValueVar{OID: ".1.3.6.1.2.1.33.1.2.3.0", Unit: RuntimeUnitMinutes},
		},
		{
			profile:       "apc",
			wantOnBattery: SNMPStateVar{OID: ".1.3.6.1.4.1.318.1.1.1.4.1.1.0", Values: []int{3}},
			wantCharge:    SNMPValueVar{OID: ".1.3.6.1.4.1.318.1.1.1.2.2.1.0"},
			wantRuntime:   SNMPValueVar{OID: ".1.3.6.1.4.1.318.1.1.1.2.2.3.0", Unit: RuntimeUnitTimeTicks},
		},
		{
			profile:       "cyberpower",
			wantOnBattery: SNMPStateVar{OID: ".1.3.6.1.4.1.3808.1.1.1.4.1.1.0", Values: []int{3}},
			wantCharge:    SNMPValueVar{OID: ".1.3.6.1.4.1.3808.1.1.1.2.2.1.0"},
			wantRuntime:   SNMPValueVar{OID: ".1.3.6.1.4.1.3808.1.1.1.2.2.4.0", Unit: RuntimeUnitTimeTicks},
		},
		{
			profile:       "eaton",
			wantOnBattery: SNMPStateVar{OID: ".1.3.6.1.4.1.534.1.4.5.0", Values: []int{5}},
			wantCharge:    SNMPValueVar{OID: ".1.3.6.1.4.1.534.1.2.4.0"},
			wantRuntime:   SNMPValueVar{OID: ".1.3.6.1.4.1.534.1.2.1.0", Unit: RuntimeUnitSeconds},
		},
	}
	for _, tc := range cases {
		t.Run("profile "+tc.profile, func(t *testing.T) {
			snmp := SNMPConfig{Profile: tc.profile}

			err := ResolveSNMPVars(&snmp)

			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if !reflect.DeepEqual(*snmp.OnBattery, tc.wantOnBattery) {
				t.Errorf("on_battery=%+v want %+v", *snmp.OnBattery, tc.wantOnBattery)
			}
			if *snmp.Charge != tc.wantCharge {
				t.Errorf("charge=%+v want %+v", *snmp.Charge, tc.wantCharge)
			}
			if *snmp.Runtime != tc.wantRuntime {
				t.Errorf("runtime=%+v want %+v", *snmp.Runtime, tc.wantRuntime)
			}
		})
	}
}

func TestResolveSNMPVarsDefaultsProfileToUPSMIB(t *testing.T) {
	var snmp SNMPConfig

	if err := ResolveSNMPVars(&snmp); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if snmp.Profile != SNMPProfileUPSMIB {
		t.Fatalf("profile=%q want %q", snmp.Profile, SNMPProfileUPSMIB)
	}
}

func TestResolveSNMPVarsRejectsUnknownProfile(t *testing.T) {
	snmp := SNMPConfig{Profile: "acme"}

	err := ResolveSNMPVars(&snmp)

	if err == nil || !strings.Contains(err.Error(), "snmp.profile") {
		t.Fatalf("expected unknown profile error, got %v", err)
	}
}

func TestResolveSNMPVarsOverridesSingleEntry(t *testing.T) {
	snmp := SNMPConfig{
		Profile:   "apc",
		OnBattery: &SNMPStateVar{OID: ".1.2.3", Values: []int{2, 3}},
	}

	if err := ResolveSNMPVars(&snmp); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if !reflect.DeepEqual(*snmp.OnBattery, SNMPStateVar{OID: ".1.2.3", Values: []int{2, 3}}) {
		t.Errorf("on_battery=%+v want the override", *snmp.OnBattery)
	}
	if snmp.Charge.OID != ".1.3.6.1.4.1.318.1.1.1.2.2.1.0" || snmp.Runtime.Unit != RuntimeUnitTimeTicks {
		t.Errorf("charge=%+v runtime=%+v want apc presets", *snmp.Charge, *snmp.Runtime)
	}
}

func TestResolveSNMPVarsDoesNotShareProfileState(t *testing.T) {
	first := SNMPConfig{}
	if err := ResolveSNMPVars(&first); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	first.OnBattery.Values[0] = 99

	second := SNMPConfig{}
	if err := ResolveSNMPVars(&second); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if second.OnBattery.Values[0] != 5 {
		t.Fatalf("profile preset was mutated through a resolved config: %v", second.OnBattery.Values)
	}
}

func TestResolveSNMPVarsDefaultsRuntimeOverrideUnitToMinutes(t *testing.T) {
	snmp := SNMPConfig{Runtime: &SNMPValueVar{OID: ".1.2.3"}}

	if err := ResolveSNMPVars(&snmp); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if snmp.Runtime.Unit != RuntimeUnitMinutes {
		t.Fatalf("unit=%q want minutes", snmp.Runtime.Unit)
	}
}

func TestResolveSNMPVarsValidatesOverrides(t *testing.T) {
	cases := []struct {
		name    string
		snmp    SNMPConfig
		wantErr string
	}{
		{name: "on_battery without oid", snmp: SNMPConfig{OnBattery: &SNMPStateVar{Values: []int{3}}}, wantErr: "snmp.on_battery.oid"},
		{name: "on_battery without values", snmp: SNMPConfig{OnBattery: &SNMPStateVar{OID: ".1.2.3"}}, wantErr: "snmp.on_battery.values"},
		{name: "charge without oid", snmp: SNMPConfig{Charge: &SNMPValueVar{}}, wantErr: "snmp.charge.oid"},
		{name: "charge with unit", snmp: SNMPConfig{Charge: &SNMPValueVar{OID: ".1.2.3", Unit: "percent"}}, wantErr: "snmp.charge.unit"},
		{name: "runtime without oid", snmp: SNMPConfig{Runtime: &SNMPValueVar{Unit: RuntimeUnitSeconds}}, wantErr: "snmp.runtime.oid"},
		{name: "runtime unknown unit", snmp: SNMPConfig{Runtime: &SNMPValueVar{OID: ".1.2.3", Unit: "hours"}}, wantErr: "snmp.runtime.unit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ResolveSNMPVars(&tc.snmp)

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadMasterConfigParsesSNMPOverrides(t *testing.T) {
	cfg, err := loadMasterWithSNMP(t, `
  profile: eaton
  runtime: { oid: ".1.2.3", unit: timeticks }
`)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *cfg.SNMP.Runtime != (SNMPValueVar{OID: ".1.2.3", Unit: RuntimeUnitTimeTicks}) {
		t.Fatalf("runtime=%+v want override", *cfg.SNMP.Runtime)
	}
	if cfg.SNMP.OnBattery.OID != ".1.3.6.1.4.1.534.1.4.5.0" {
		t.Fatalf("on_battery=%+v want eaton preset", *cfg.SNMP.OnBattery)
	}
}

// indent nests a flat YAML block under the snmp key.
func indent(block string) string {
	return "  " + strings.ReplaceAll(block, "\n", "\n  ") + "\n"
}
