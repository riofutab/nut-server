package master

import (
	"testing"

	"github.com/gosnmp/gosnmp"
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
