package master

import (
	"fmt"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func completedCommand(completedAt time.Time) *shutdownCommandState {
	return &shutdownCommandState{
		Message:     protocol.ShutdownMessage{},
		TargetNodes: map[string]struct{}{},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
		CompletedAt: &completedAt,
	}
}

func TestPruneCompletedCommandsLocked(t *testing.T) {
	t.Run("no-op under the retention cap", func(t *testing.T) {
		server := NewServer(config.MasterConfig{})
		server.commands["cmd-1"] = completedCommand(time.Now())
		if server.pruneCompletedCommandsLocked() {
			t.Fatal("expected no pruning below the cap")
		}
		if len(server.commands) != 1 {
			t.Fatalf("got %d commands, want 1", len(server.commands))
		}
	})

	t.Run("evicts oldest completed commands first once over the cap", func(t *testing.T) {
		server := NewServer(config.MasterConfig{})
		base := time.Now()
		for i := 0; i < maxRetainedCompletedCommands+5; i++ {
			id := fmt.Sprintf("cmd-%d", i)
			server.commands[id] = completedCommand(base.Add(time.Duration(i) * time.Second))
		}
		if !server.pruneCompletedCommandsLocked() {
			t.Fatal("expected pruning over the cap")
		}
		if len(server.commands) != maxRetainedCompletedCommands {
			t.Fatalf("got %d commands, want %d", len(server.commands), maxRetainedCompletedCommands)
		}
		if _, ok := server.commands["cmd-0"]; ok {
			t.Fatal("oldest completed command should have been evicted")
		}
	})

	t.Run("never evicts the active command or the one local_shutdown is waiting on", func(t *testing.T) {
		server := NewServer(config.MasterConfig{})
		server.activeCommand = "active-cmd"
		server.localShutdown = &localShutdownState{CommandID: "waiting-cmd"}
		base := time.Now()
		server.commands["active-cmd"] = completedCommand(base)
		server.commands["waiting-cmd"] = completedCommand(base)
		for i := 0; i < maxRetainedCompletedCommands+5; i++ {
			id := fmt.Sprintf("cmd-%d", i)
			server.commands[id] = completedCommand(base.Add(time.Duration(i+1) * time.Second))
		}
		server.pruneCompletedCommandsLocked()
		if _, ok := server.commands["active-cmd"]; !ok {
			t.Fatal("active command must survive pruning")
		}
		if _, ok := server.commands["waiting-cmd"]; !ok {
			t.Fatal("command referenced by local_shutdown must survive pruning")
		}
	})

	t.Run("in-flight (not yet completed) commands are never counted as evictable", func(t *testing.T) {
		server := NewServer(config.MasterConfig{})
		server.commands["in-flight"] = &shutdownCommandState{
			Message:     protocol.ShutdownMessage{},
			TargetNodes: map[string]struct{}{},
			NodeUpdates: map[string]protocol.ShutdownAckMessage{},
		}
		base := time.Now()
		for i := 0; i < maxRetainedCompletedCommands+5; i++ {
			id := fmt.Sprintf("cmd-%d", i)
			server.commands[id] = completedCommand(base.Add(time.Duration(i) * time.Second))
		}
		server.pruneCompletedCommandsLocked()
		if _, ok := server.commands["in-flight"]; !ok {
			t.Fatal("in-flight command must survive pruning")
		}
	})
}
