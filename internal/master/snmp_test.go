package master

import (
	"testing"

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

	_, err := decodeUPSPacket(config.SNMPConfig{}, packet)

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

	status, err := decodeUPSPacket(config.SNMPConfig{}, packet)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.OnBattery || status.BatteryCharge != 80 || status.RuntimeMinutes != 25 {
		t.Fatalf("unexpected status %+v", status)
	}
}
