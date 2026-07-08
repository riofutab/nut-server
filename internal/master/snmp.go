package master

import (
	"fmt"
	"time"

	"github.com/gosnmp/gosnmp"

	"nut-server/internal/config"
)

type UPSStatus struct {
	OnBattery      bool
	BatteryCharge  int
	RuntimeMinutes int
}

func ReadUPSStatus(cfg config.SNMPConfig) (UPSStatus, error) {
	client := &gosnmp.GoSNMP{
		Target:    cfg.Target,
		Port:      cfg.Port,
		Community: cfg.Community,
		Timeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
		Retries:   1,
		Version:   parseVersion(cfg.Version),
	}
	if err := client.Connect(); err != nil {
		return UPSStatus{}, fmt.Errorf("connect snmp %s:%d: %w", cfg.Target, cfg.Port, err)
	}
	defer client.Conn.Close()

	// One batched GET for all three OIDs instead of three round-trips: SNMP GET
	// natively carries multiple varbinds, and a compliant agent (RFC 3416)
	// always returns them in request order, so positional indexing below is safe.
	packet, err := client.Get([]string{cfg.OutputSourceOID, cfg.ChargeOID, cfg.RuntimeMinutesOID})
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read ups oids: %w", err)
	}
	if len(packet.Variables) != 3 {
		return UPSStatus{}, fmt.Errorf("unexpected variable count: got %d want 3", len(packet.Variables))
	}
	outputSource, err := decodeIntVariable(cfg.OutputSourceOID, packet.Variables[0])
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read output source oid: %w", err)
	}
	charge, err := decodeIntVariable(cfg.ChargeOID, packet.Variables[1])
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read charge oid: %w", err)
	}
	runtimeMinutes, err := decodeIntVariable(cfg.RuntimeMinutesOID, packet.Variables[2])
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read runtime oid: %w", err)
	}

	return UPSStatus{
		OnBattery:      outputSource == 5,
		BatteryCharge:  charge,
		RuntimeMinutes: runtimeMinutes,
	}, nil
}

// decodeIntVariable rejects an SNMP exception varbind (unsupported/typo'd OID),
// which otherwise decodes to a non-nil big.Int(0) via ToBigInt and would be
// misread as a genuine 0 — a critically low charge/runtime. Surfacing it as an
// error turns a misconfigured OID into a poll failure instead of a false reading.
func decodeIntVariable(oid string, variable gosnmp.SnmpPDU) (int, error) {
	switch variable.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		return 0, fmt.Errorf("oid %s not available on device (%v)", oid, variable.Type)
	}
	value := gosnmp.ToBigInt(variable.Value)
	if value == nil {
		return 0, fmt.Errorf("oid %s has empty value", oid)
	}
	return int(value.Int64()), nil
}

func parseVersion(version string) gosnmp.SnmpVersion {
	switch version {
	case "1":
		return gosnmp.Version1
	case "3":
		return gosnmp.Version3
	default:
		return gosnmp.Version2c
	}
}
