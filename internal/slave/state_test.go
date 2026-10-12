package slave

import (
	"fmt"
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

func TestClientLoadStateMovesCorruptFileAside(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "slave-state.json")
	corrupt := []byte(`{"issued_token": "node-token", "commands": `)
	if err := os.WriteFile(stateFile, corrupt, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	client := NewClient(config.SlaveConfig{NodeID: "node-1", StateFile: stateFile})

	if client.issuedToken != "" || len(client.commandStates) != 0 {
		t.Fatalf("expected empty state, got token %q commands %v", client.issuedToken, client.commandStates)
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("expected corrupt state file to be moved away, stat err=%v", err)
	}
	moved, err := filepath.Glob(stateFile + ".corrupt-*")
	if err != nil || len(moved) != 1 {
		t.Fatalf("expected one %s.corrupt-* file, got %v (err %v)", stateFile, moved, err)
	}
	content, err := os.ReadFile(moved[0])
	if err != nil || string(content) != string(corrupt) {
		t.Fatalf("expected moved file to keep the original bytes, got %q (err %v)", content, err)
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

func TestSetCommandStatePrunesOldestBeyondCapButKeepsActiveExecutions(t *testing.T) {
	client := NewClient(config.SlaveConfig{NodeID: "node-1"})
	base := time.Now().UTC().Add(-time.Hour)
	const protectedID = "cmd-000"
	client.commandMu.Lock()
	client.executions[protectedID] = &shutdownExecution{}
	client.commandMu.Unlock()

	total := maxRetainedCommandStates + 5
	for i := 0; i < total; i++ {
		client.setCommandState(protocol.ShutdownAckMessage{
			CommandID: fmt.Sprintf("cmd-%03d", i),
			Status:    protocol.ShutdownStatusExecuted,
			AckedAt:   base.Add(time.Duration(i) * time.Second),
		})
	}

	client.commandMu.Lock()
	defer client.commandMu.Unlock()
	if len(client.commandStates) != maxRetainedCommandStates {
		t.Fatalf("len=%d want %d", len(client.commandStates), maxRetainedCommandStates)
	}
	if _, ok := client.commandStates[protectedID]; !ok {
		t.Fatalf("expected command with active execution to be retained")
	}
	for i := 1; i <= 5; i++ {
		if _, ok := client.commandStates[fmt.Sprintf("cmd-%03d", i)]; ok {
			t.Fatalf("expected oldest cmd-%03d to be pruned", i)
		}
	}
	if _, ok := client.commandStates[fmt.Sprintf("cmd-%03d", total-1)]; !ok {
		t.Fatalf("expected newest command retained")
	}
}
