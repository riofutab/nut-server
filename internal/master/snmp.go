package master

import (
	"fmt"
	"slices"
	"time"

	"github.com/gosnmp/gosnmp"

	"nut-server/internal/config"
)

type UPSStatus struct {
	OnBattery      bool
	BatteryCharge  int
	RuntimeMinutes int
}

var snmpAuthProtocols = map[string]gosnmp.SnmpV3AuthProtocol{
	"MD5": gosnmp.MD5, "SHA": gosnmp.SHA, "SHA224": gosnmp.SHA224,
	"SHA256": gosnmp.SHA256, "SHA384": gosnmp.SHA384, "SHA512": gosnmp.SHA512,
}

var snmpPrivProtocols = map[string]gosnmp.SnmpV3PrivProtocol{
	"DES": gosnmp.DES, "AES": gosnmp.AES, "AES192": gosnmp.AES192,
	"AES256": gosnmp.AES256, "AES192C": gosnmp.AES192C, "AES256C": gosnmp.AES256C,
}

func ReadUPSStatus(cfg config.SNMPConfig) (UPSStatus, error) {
	client, err := newSNMPClient(cfg)
	if err != nil {
		return UPSStatus{}, err
	}
	if err := client.Connect(); err != nil {
		return UPSStatus{}, fmt.Errorf("connect snmp %s:%d: %w", cfg.Target, cfg.Port, err)
	}
	defer client.Conn.Close()

	// One batched GET for all three OIDs instead of three round-trips: SNMP GET
	// natively carries multiple varbinds, and a compliant agent (RFC 3416)
	// always returns them in request order, so positional indexing below is safe.
	packet, err := client.Get([]string{cfg.OnBattery.OID, cfg.Charge.OID, cfg.Runtime.OID})
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read ups oids: %w", err)
	}
	return decodeUPSPacket(cfg, packet)
}

func decodeUPSPacket(cfg config.SNMPConfig, packet *gosnmp.SnmpPacket) (UPSStatus, error) {
	// An agent reports a failed request (e.g. SNMPv1 noSuchName) through the
	// PDU error status while echoing Null varbinds; reading those would yield
	// zeros that look like an empty battery.
	if packet.Error != gosnmp.NoError {
		return UPSStatus{}, fmt.Errorf("snmp agent error %v at index %d", packet.Error, packet.ErrorIndex)
	}
	if len(packet.Variables) != 3 {
		return UPSStatus{}, fmt.Errorf("unexpected variable count: got %d want 3", len(packet.Variables))
	}
	batteryState, err := decodeIntVariable(cfg.OnBattery.OID, packet.Variables[0])
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read on-battery oid: %w", err)
	}
	charge, err := decodeIntVariable(cfg.Charge.OID, packet.Variables[1])
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read charge oid: %w", err)
	}
	runtime, err := decodeIntVariable(cfg.Runtime.OID, packet.Variables[2])
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read runtime oid: %w", err)
	}

	return UPSStatus{
		OnBattery:      slices.Contains(cfg.OnBattery.Values, batteryState),
		BatteryCharge:  charge,
		RuntimeMinutes: runtimeToMinutes(runtime, cfg.Runtime.Unit),
	}, nil
}

// runtimeToMinutes rounds down, so a runtime threshold fires no later than the
// device's own reading would.
func runtimeToMinutes(value int, unit string) int {
	switch unit {
	case config.RuntimeUnitSeconds:
		return value / 60
	case config.RuntimeUnitTimeTicks:
		return value / (100 * 60)
	default:
		return value
	}
}

// decodeIntVariable accepts only integer-typed varbinds. gosnmp.ToBigInt maps
// every other type (Null, OctetString, exception varbinds) to 0, which would be
// misread as a critically low charge/runtime; surfacing it as an error turns a
// misconfigured OID into a poll failure instead of a false reading.
func decodeIntVariable(oid string, variable gosnmp.SnmpPDU) (int, error) {
	switch variable.Type {
	case gosnmp.Integer, gosnmp.Gauge32, gosnmp.Counter32, gosnmp.Uinteger32, gosnmp.TimeTicks:
		return int(gosnmp.ToBigInt(variable.Value).Int64()), nil
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		return 0, fmt.Errorf("oid %s not available on device (%v)", oid, variable.Type)
	default:
		return 0, fmt.Errorf("oid %s has non-integer type %v", oid, variable.Type)
	}
}

// newSNMPClient builds an unconnected client from a validated config.
func newSNMPClient(cfg config.SNMPConfig) (*gosnmp.GoSNMP, error) {
	client := &gosnmp.GoSNMP{
		Target:  cfg.Target,
		Port:    cfg.Port,
		Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		Retries: 1,
	}
	if cfg.Version != "3" {
		client.Version = gosnmp.Version2c
		if cfg.Version == "1" {
			client.Version = gosnmp.Version1
		}
		client.Community = cfg.Community
		return client, nil
	}

	usm := &gosnmp.UsmSecurityParameters{
		UserName:               cfg.Username,
		AuthenticationProtocol: gosnmp.NoAuth,
		PrivacyProtocol:        gosnmp.NoPriv,
	}
	var flags gosnmp.SnmpV3MsgFlags
	switch cfg.SecurityLevel {
	case config.SNMPSecurityNoAuthNoPriv:
		flags = gosnmp.NoAuthNoPriv
	case config.SNMPSecurityAuthNoPriv:
		flags = gosnmp.AuthNoPriv
	case config.SNMPSecurityAuthPriv:
		flags = gosnmp.AuthPriv
	default:
		return nil, fmt.Errorf("snmp security level %q is unsupported", cfg.SecurityLevel)
	}
	if flags&gosnmp.AuthNoPriv != 0 {
		auth, ok := snmpAuthProtocols[cfg.AuthProtocol]
		if !ok {
			return nil, fmt.Errorf("snmp auth protocol %q is unsupported", cfg.AuthProtocol)
		}
		usm.AuthenticationProtocol = auth
		usm.AuthenticationPassphrase = cfg.AuthPassphrase
	}
	if flags == gosnmp.AuthPriv {
		priv, ok := snmpPrivProtocols[cfg.PrivProtocol]
		if !ok {
			return nil, fmt.Errorf("snmp priv protocol %q is unsupported", cfg.PrivProtocol)
		}
		usm.PrivacyProtocol = priv
		usm.PrivacyPassphrase = cfg.PrivPassphrase
	}
	client.Version = gosnmp.Version3
	client.SecurityModel = gosnmp.UserSecurityModel
	client.MsgFlags = flags
	client.SecurityParameters = usm
	return client, nil
}
