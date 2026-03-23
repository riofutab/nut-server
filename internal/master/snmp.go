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

	outputSource, err := getInt(client, cfg.OutputSourceOID)
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read output source oid: %w", err)
	}
	charge, err := getInt(client, cfg.ChargeOID)
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read charge oid: %w", err)
	}
	runtimeMinutes, err := getInt(client, cfg.RuntimeMinutesOID)
	if err != nil {
		return UPSStatus{}, fmt.Errorf("read runtime oid: %w", err)
	}

	return UPSStatus{
		OnBattery:      outputSource == 5,
		BatteryCharge:  charge,
		RuntimeMinutes: runtimeMinutes,
	}, nil
}

func getInt(client *gosnmp.GoSNMP, oid string) (int, error) {
	packet, err := client.Get([]string{oid})
	if err != nil {
		return 0, err
	}
	if len(packet.Variables) != 1 {
		return 0, fmt.Errorf("unexpected variable count for oid %s", oid)
	}
	value := gosnmp.ToBigInt(packet.Variables[0].Value)
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
