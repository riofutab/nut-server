package master

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func TestServerPersistsAndRestoresState(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "master-state.json")
	server := NewServer(config.MasterConfig{StateFile: stateFile, CommandTimeout: config.Duration{Duration: 30 * time.Second}})

	issuedAt := time.Now().UTC()
	timeoutAt := issuedAt.Add(30 * time.Second)
	server.commandSeq.Store(4)
	server.commands["shutdown-5"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-5",
			Reason:    "manual",
			IssuedAt:  issuedAt,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: "shutdown-5",
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusAccepted,
				AckedAt:   issuedAt,
			},
		},
		TimeoutAt: &timeoutAt,
	}
	server.activeCommand = "shutdown-5"
	server.shutdownIssued.Store(true)
	server.commandMu.Lock()
	server.saveStateLocked()
	server.commandMu.Unlock()

	restored := NewServer(config.MasterConfig{StateFile: stateFile})
	if restored.activeCommand != "shutdown-5" {
		t.Fatalf("expected active command restored, got %q", restored.activeCommand)
	}
	if !restored.shutdownIssued.Load() {
		t.Fatalf("expected shutdownIssued restored")
	}
	if restored.commandSeq.Load() != 4 {
		t.Fatalf("expected command seq 4, got %d", restored.commandSeq.Load())
	}
	command, ok := restored.commands["shutdown-5"]
	if !ok {
		t.Fatalf("expected command restored")
	}
	if command.NodeUpdates["node-1"].Status != protocol.ShutdownStatusAccepted {
		t.Fatalf("expected node state restored")
	}
}

func TestServerLoadStateIgnoresMissingFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "missing.json")
	server := NewServer(config.MasterConfig{StateFile: stateFile})
	if len(server.commands) != 0 {
		t.Fatalf("expected no commands for missing state file")
	}
	if server.activeCommand != "" {
		t.Fatalf("expected no active command for missing state file")
	}
}

func TestServerSaveStateCreatesParentDirectory(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "nested", "master-state.json")
	server := NewServer(config.MasterConfig{StateFile: stateFile})
	server.commandMu.Lock()
	server.saveStateLocked()
	server.commandMu.Unlock()
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("expected state file written: %v", err)
	}
}
