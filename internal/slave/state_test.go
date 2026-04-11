package slave

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func TestClientPersistsAndRestoresState(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "slave-state.json")
	client := NewClient(config.SlaveConfig{NodeID: "node-1", StateFile: stateFile})
	update := protocol.ShutdownAckMessage{
		CommandID: "shutdown-1",
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuted,
		AckedAt:   time.Now().UTC(),
	}
	client.setCommandState(update)

	restored := NewClient(config.SlaveConfig{NodeID: "node-1", StateFile: stateFile})
	got, ok := restored.getCommandState("shutdown-1")
	if !ok {
		t.Fatalf("expected command state restored")
	}
	if got.Status != protocol.ShutdownStatusExecuted {
		t.Fatalf("expected executed state, got %s", got.Status)
	}
}

func TestClientLoadStateIgnoresMissingFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "missing.json")
	client := NewClient(config.SlaveConfig{NodeID: "node-1", StateFile: stateFile})
	if len(client.commandStates) != 0 {
		t.Fatalf("expected empty state for missing file")
	}
}

func TestClientSaveStateCreatesParentDirectory(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "nested", "slave-state.json")
	client := NewClient(config.SlaveConfig{NodeID: "node-1", StateFile: stateFile})
	client.commandMu.Lock()
	client.saveStateLocked()
	client.commandMu.Unlock()
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("expected state file written: %v", err)
	}
}
