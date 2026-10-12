package master

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"nut-server/internal/config"
)

func TestDecodeIntVariable(t *testing.T) {
	t.Run("normal value", func(t *testing.T) {
		got, err := decodeIntVariable(".1.2.3", gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 42})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 42 {
			t.Fatalf("got %d, want 42", got)
		}
	})

	t.Run("rejects NoSuchObject instead of reading it as zero", func(t *testing.T) {
		_, err := decodeIntVariable(".1.2.3", gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject})
		if err == nil {
			t.Fatal("expected error for NoSuchObject, got nil")
		}
	})

	t.Run("rejects NoSuchInstance", func(t *testing.T) {
		_, err := decodeIntVariable(".1.2.3", gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance})
		if err == nil {
			t.Fatal("expected error for NoSuchInstance, got nil")
		}
	})

	t.Run("rejects EndOfMibView", func(t *testing.T) {
		_, err := decodeIntVariable(".1.2.3", gosnmp.SnmpPDU{Type: gosnmp.EndOfMibView})
		if err == nil {
			t.Fatal("expected error for EndOfMibView, got nil")
		}
	})
}

func TestDecodeIntVariableRejectsNonIntegerTypes(t *testing.T) {
	cases := []gosnmp.SnmpPDU{
		{Type: gosnmp.Null},
		{Type: gosnmp.OctetString, Value: []byte("42")},
	}
	for _, pdu := range cases {
		if _, err := decodeIntVariable(".1.2.3", pdu); err == nil {
			t.Fatalf("expected error for %v, got nil", pdu.Type)
		}
	}
}

func TestDecodeUPSPacketRejectsAgentErrorStatus(t *testing.T) {
	// The error status alone must fail the poll, whatever the varbinds hold.
	packet := &gosnmp.SnmpPacket{
		Error:      gosnmp.GenErr,
		ErrorIndex: 2,
		Variables: []gosnmp.SnmpPDU{
			{Type: gosnmp.Integer, Value: 5},
			{Type: gosnmp.Integer, Value: 0},
			{Type: gosnmp.Integer, Value: 0},
		},
	}

	_, err := decodeUPSPacket(defaultSNMPConfig(t), packet)

	if err == nil {
		t.Fatal("expected agent error status to fail the poll")
	}
}

func TestDecodeUPSPacketReadsBatteryState(t *testing.T) {
	packet := &gosnmp.SnmpPacket{
		Variables: []gosnmp.SnmpPDU{
			{Type: gosnmp.Integer, Value: 5},
			{Type: gosnmp.Integer, Value: 80},
			{Type: gosnmp.Integer, Value: 25},
		},
	}

	status, err := decodeUPSPacket(defaultSNMPConfig(t), packet)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.OnBattery || status.BatteryCharge != 80 || status.RuntimeMinutes != 25 {
		t.Fatalf("unexpected status %+v", status)
	}
}

func defaultSNMPConfig(t *testing.T) config.SNMPConfig {
	t.Helper()
	var cfg config.SNMPConfig
	if err := config.ResolveSNMPVars(&cfg); err != nil {
		t.Fatalf("resolve snmp vars: %v", err)
	}
	return cfg
}

func TestNewSNMPClientUsesCommunityForV2c(t *testing.T) {
	cfg := config.SNMPConfig{Target: "10.0.0.31", Port: 161, Version: "2c", Community: "public", TimeoutSeconds: 2}

	client, err := newSNMPClient(cfg)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.Version != gosnmp.Version2c || client.Community != "public" || client.SecurityParameters != nil {
		t.Fatalf("unexpected v2c client %+v", client)
	}
	if client.Target != "10.0.0.31" || client.Port != 161 || client.Timeout != 2*time.Second {
		t.Fatalf("unexpected transport settings %+v", client)
	}
}

func TestNewSNMPClientConfiguresUSMForV3(t *testing.T) {
	cases := []struct {
		name      string
		cfg       config.SNMPConfig
		wantFlags gosnmp.SnmpV3MsgFlags
		wantUSM   usmFields
	}{
		{
			name:      "noAuthNoPriv",
			cfg:       config.SNMPConfig{Version: "3", Username: "ups", SecurityLevel: config.SNMPSecurityNoAuthNoPriv},
			wantFlags: gosnmp.NoAuthNoPriv,
			wantUSM:   usmFields{user: "ups", auth: gosnmp.NoAuth, priv: gosnmp.NoPriv},
		},
		{
			name: "authNoPriv",
			cfg: config.SNMPConfig{Version: "3", Username: "ups", SecurityLevel: config.SNMPSecurityAuthNoPriv,
				AuthProtocol: "SHA256", AuthPassphrase: "auth-pass"},
			wantFlags: gosnmp.AuthNoPriv,
			wantUSM:   usmFields{user: "ups", auth: gosnmp.SHA256, authPass: "auth-pass", priv: gosnmp.NoPriv},
		},
		{
			name: "authPriv",
			cfg: config.SNMPConfig{Version: "3", Username: "ups", SecurityLevel: config.SNMPSecurityAuthPriv,
				AuthProtocol: "SHA", AuthPassphrase: "auth-pass", PrivProtocol: "AES256C", PrivPassphrase: "priv-pass"},
			wantFlags: gosnmp.AuthPriv,
			wantUSM:   usmFields{user: "ups", auth: gosnmp.SHA, authPass: "auth-pass", priv: gosnmp.AES256C, privPass: "priv-pass"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := newSNMPClient(tc.cfg)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if client.Version != gosnmp.Version3 || client.SecurityModel != gosnmp.UserSecurityModel || client.MsgFlags != tc.wantFlags {
				t.Fatalf("version=%v model=%v flags=%v", client.Version, client.SecurityModel, client.MsgFlags)
			}
			usm, ok := client.SecurityParameters.(*gosnmp.UsmSecurityParameters)
			if !ok {
				t.Fatalf("security parameters %T, want *gosnmp.UsmSecurityParameters", client.SecurityParameters)
			}
			got := usmFields{
				user: usm.UserName, auth: usm.AuthenticationProtocol, authPass: usm.AuthenticationPassphrase,
				priv: usm.PrivacyProtocol, privPass: usm.PrivacyPassphrase,
			}
			if got != tc.wantUSM {
				t.Fatalf("usm=%+v want %+v", got, tc.wantUSM)
			}
		})
	}
}

// usmFields mirrors the configurable part of gosnmp.UsmSecurityParameters,
// which holds a mutex and so cannot be copied into test tables.
type usmFields struct {
	user     string
	auth     gosnmp.SnmpV3AuthProtocol
	authPass string
	priv     gosnmp.SnmpV3PrivProtocol
	privPass string
}

func TestNewSNMPClientRejectsUnknownUSMProtocol(t *testing.T) {
	cfg := config.SNMPConfig{Version: "3", Username: "ups", SecurityLevel: config.SNMPSecurityAuthNoPriv, AuthProtocol: "SHA1"}

	_, err := newSNMPClient(cfg)

	if err == nil {
		t.Fatal("expected unknown auth protocol to fail")
	}
}

func TestDecodeUPSPacketAppliesProfileValuesAndRuntimeUnits(t *testing.T) {
	cases := []struct {
		profile     string
		state       int
		runtime     gosnmp.SnmpPDU
		wantMinutes int
	}{
		{profile: "ups-mib", state: 5, runtime: gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 25}, wantMinutes: 25},
		// TimeTicks are 1/100 s: 90000 ticks = 900 s = 15 min.
		{profile: "apc", state: 3, runtime: gosnmp.SnmpPDU{Type: gosnmp.TimeTicks, Value: uint32(90000)}, wantMinutes: 15},
		{profile: "cyberpower", state: 3, runtime: gosnmp.SnmpPDU{Type: gosnmp.TimeTicks, Value: uint32(119999)}, wantMinutes: 19},
		{profile: "eaton", state: 5, runtime: gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 659}, wantMinutes: 10},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			cfg := profileSNMPConfig(t, tc.profile)
			packet := &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{
				{Type: gosnmp.Integer, Value: tc.state},
				{Type: gosnmp.Gauge32, Value: uint(80)},
				tc.runtime,
			}}

			status, err := decodeUPSPacket(cfg, packet)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !status.OnBattery || status.BatteryCharge != 80 || status.RuntimeMinutes != tc.wantMinutes {
				t.Fatalf("status=%+v want on battery, charge 80, runtime %d", status, tc.wantMinutes)
			}
		})
	}
}

func TestDecodeUPSPacketTreatsOtherStatesAsOnMains(t *testing.T) {
	cfg := profileSNMPConfig(t, "apc")
	packet := &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{
		{Type: gosnmp.Integer, Value: 2}, // apc onLine(2)
		{Type: gosnmp.Gauge32, Value: uint(100)},
		{Type: gosnmp.TimeTicks, Value: uint32(360000)},
	}}

	status, err := decodeUPSPacket(cfg, packet)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.OnBattery {
		t.Fatalf("status=%+v want on mains", status)
	}
}

func profileSNMPConfig(t *testing.T, profile string) config.SNMPConfig {
	t.Helper()
	cfg := config.SNMPConfig{Profile: profile}
	if err := config.ResolveSNMPVars(&cfg); err != nil {
		t.Fatalf("resolve snmp vars: %v", err)
	}
	return cfg
}
