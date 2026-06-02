package master

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

// writeStateFile persists a state document and returns its path.
func writeStateFile(t *testing.T, state persistedState) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "master-state.json")
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	return path
}

func waitingRemoteState(commandID string) *localShutdownState {
	return &localShutdownState{Phase: protocol.LocalShutdownPhaseWaitingRemote, CommandID: commandID}
}

func TestNormalizeLoadedLocalShutdown(t *testing.T) {
	cmd := func(replayDisabled bool) map[string]*shutdownCommandState {
		return map[string]*shutdownCommandState{
			"cmd-1": {
				Message:        protocol.ShutdownMessage{CommandID: "cmd-1"},
				TargetNodes:    map[string]struct{}{"node-1": {}},
				NodeUpdates:    map[string]protocol.ShutdownAckMessage{},
				ReplayDisabled: replayDisabled,
			},
		}
	}

	tests := []struct {
		name      string
		enabled   bool
		state     persistedState
		wantKept  bool
		wantPhase string
	}{
		{
			name:     "disabled clears any pending local shutdown",
			enabled:  false,
			state:    persistedState{LocalShutdown: waitingRemoteState("cmd-1"), Commands: cmd(false)},
			wantKept: false,
		},
		{
			name:      "valid pending shutdown is preserved",
			enabled:   true,
			state:     persistedState{LocalShutdown: waitingRemoteState("cmd-1"), Commands: cmd(false)},
			wantKept:  true,
			wantPhase: protocol.LocalShutdownPhaseWaitingRemote,
		},
		{
			name:     "empty command id is cleared",
			enabled:  true,
			state:    persistedState{LocalShutdown: waitingRemoteState(""), Commands: cmd(false)},
			wantKept: false,
		},
		{
			name:     "missing command is cleared",
			enabled:  true,
			state:    persistedState{LocalShutdown: waitingRemoteState("ghost"), Commands: cmd(false)},
			wantKept: false,
		},
		{
			name:     "reset (replay-disabled) command is cleared",
			enabled:  true,
			state:    persistedState{LocalShutdown: waitingRemoteState("cmd-1"), Commands: cmd(true)},
			wantKept: false,
		},
		{
			name:     "idle phase does not survive a restart",
			enabled:  true,
			state:    persistedState{LocalShutdown: &localShutdownState{Phase: protocol.LocalShutdownPhaseIdle, CommandID: "cmd-1"}, Commands: cmd(false)},
			wantKept: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeStateFile(t, tc.state)
			server := NewServer(config.MasterConfig{
				StateFile:     path,
				LocalShutdown: config.LocalShutdownConfig{Enabled: tc.enabled},
			})
			if tc.wantKept {
				if server.localShutdown == nil {
					t.Fatalf("expected local shutdown preserved")
				}
				if server.localShutdown.Phase != tc.wantPhase {
					t.Fatalf("phase: want %q, got %q", tc.wantPhase, server.localShutdown.Phase)
				}
			} else if server.localShutdown != nil {
				t.Fatalf("expected local shutdown cleared, got %+v", server.localShutdown)
			}
		})
	}
}

func TestLoadStateToleratesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master-state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	// Must not panic and must start with clean state.
	server := NewServer(config.MasterConfig{StateFile: path})
	if len(server.commands) != 0 {
		t.Fatalf("expected no commands from corrupt state, got %d", len(server.commands))
	}
	if server.localShutdown != nil {
		t.Fatalf("expected nil local shutdown from corrupt state")
	}
	if server.activeCommand != "" {
		t.Fatalf("expected empty active command from corrupt state")
	}
}
