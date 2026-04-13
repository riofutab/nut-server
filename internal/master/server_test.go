package master

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func TestRegistryRemoveIfMatchKeepsNewerConnection(t *testing.T) {
	registry := NewRegistry()

	oldConn, oldPeer := net.Pipe()
	defer oldPeer.Close()
	oldClient := NewClient(oldConn)
	oldClient.NodeID = "node-1"

	newConn, newPeer := net.Pipe()
	defer newPeer.Close()
	newClient := NewClient(newConn)
	newClient.NodeID = "node-1"
	defer newClient.Close()

	registry.Set(oldClient)
	registry.Set(newClient)
	registry.RemoveIfMatch("node-1", oldClient)

	got, ok := registry.Get("node-1")
	if !ok {
		t.Fatalf("expected node to remain registered")
	}
	if got != newClient {
		t.Fatalf("expected newer client to remain registered")
	}
}

func TestReplayPendingShutdownSendsActiveCommand(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	message := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "battery low",
		IssuedAt:  time.Now().UTC(),
		Target:    protocol.ShutdownTarget{All: true},
	}
	server.rememberShutdownCommand(message, []*Client{{NodeID: "node-1"}})
	server.shutdownIssued.Store(true)

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.replayPendingShutdown(client)
	}()

	env := readEnvelopeFromConn(t, peer)
	if err := <-errCh; err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}
	if env.Type != protocol.TypeShutdown {
		t.Fatalf("expected shutdown envelope, got %s", env.Type)
	}

	var replayed protocol.ShutdownMessage
	decodePayloadForTest(t, env.Data, &replayed)
	if replayed.CommandID != message.CommandID {
		t.Fatalf("expected command id %s, got %s", message.CommandID, replayed.CommandID)
	}
}

func TestReplayPendingShutdownSkipsTerminalNodes(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	message := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "battery low",
		IssuedAt:  time.Now().UTC(),
		Target:    protocol.ShutdownTarget{All: true},
	}
	server.rememberShutdownCommand(message, []*Client{{NodeID: "node-1"}})
	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID,
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuted,
		AckedAt:   time.Now().UTC(),
	})

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"

	if err := server.replayPendingShutdown(client); err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}

	if err := peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatalf("expected no replay for terminal node")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no replay is sent, got %v", err)
	}
}

func TestReplayPendingShutdownSkipsUntargetedNodes(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	message := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "battery low",
		IssuedAt:  time.Now().UTC(),
		Target:    protocol.ShutdownTarget{All: true},
	}
	server.rememberShutdownCommand(message, []*Client{{NodeID: "node-1"}})
	server.shutdownIssued.Store(true)

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-2"

	if err := server.replayPendingShutdown(client); err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}

	if err := peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatalf("expected no replay for untargeted node")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no replay is sent, got %v", err)
	}
}

func TestTriggerShutdownTargetsNodesByNodeIDAndTag(t *testing.T) {
	server := NewServer(config.MasterConfig{DryRun: true, CommandTimeout: config.Duration{Duration: 30 * time.Second}})

	conn1, peer1 := net.Pipe()
	defer conn1.Close()
	defer peer1.Close()
	client1 := NewClient(conn1)
	client1.NodeID = "node-1"
	client1.Tags = []string{"db"}
	server.registry.Set(client1)

	conn2, peer2 := net.Pipe()
	defer conn2.Close()
	defer peer2.Close()
	client2 := NewClient(conn2)
	client2.NodeID = "node-2"
	client2.Tags = []string{"web"}
	server.registry.Set(client2)

	var (
		message protocol.ShutdownMessage
		summary protocol.CommandSummary
	)
	errCh := make(chan error, 1)
	go func() {
		var err error
		message, summary, err = server.TriggerShutdown(protocol.ShutdownRequest{
			Reason: "manual",
			Tags:   []string{"web"},
		})
		errCh <- err
	}()

	env := readEnvelopeFromConn(t, peer2)
	if err := <-errCh; err != nil {
		t.Fatalf("trigger shutdown: %v", err)
	}
	if env.Type != protocol.TypeShutdown {
		t.Fatalf("expected shutdown envelope, got %s", env.Type)
	}
	if !server.shutdownIssued.Load() {
		t.Fatalf("expected shutdownIssued to be true")
	}
	if summary.Targeted != 1 {
		t.Fatalf("expected 1 target, got %d", summary.Targeted)
	}
	if len(message.Target.Tags) != 1 || message.Target.Tags[0] != "web" {
		t.Fatalf("expected tag target to be kept")
	}
	if err := peer1.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer1.Read(buffer); err == nil {
		t.Fatalf("expected non-matching node to receive no shutdown")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no shutdown is sent, got %v", err)
	}
}

func TestMarkTimedOutCommandsMarksOutstandingNodes(t *testing.T) {
	server := NewServer(config.MasterConfig{DryRun: true, CommandTimeout: config.Duration{Duration: 1 * time.Second}})
	issuedAt := time.Now().UTC().Add(-5 * time.Second)
	timeoutAt := issuedAt.Add(1 * time.Second)
	command := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "manual",
		IssuedAt:  issuedAt,
		Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1", "node-2"}},
	}
	server.commands[command.CommandID] = &shutdownCommandState{
		Message: command,
		TargetNodes: map[string]struct{}{
			"node-1": {},
			"node-2": {},
		},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: command.CommandID,
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusExecuted,
				AckedAt:   issuedAt.Add(500 * time.Millisecond),
			},
		},
		TimeoutAt: &timeoutAt,
	}
	server.activeCommand = command.CommandID
	server.shutdownIssued.Store(true)

	now := issuedAt.Add(2 * time.Second)
	server.markTimedOutCommands(now)

	summary := server.commandSummary(command.CommandID)
	if summary.Timeout != 1 {
		t.Fatalf("expected 1 timeout, got %d", summary.Timeout)
	}
	if !summary.Complete {
		t.Fatalf("expected command to be complete after timeout")
	}
	if !server.shutdownIssued.Load() {
		t.Fatalf("expected shutdownIssued to remain true while timed-out nodes can still repair state")
	}
	if server.activeCommand != command.CommandID {
		t.Fatalf("expected active command to remain replayable after timeout, got %q", server.activeCommand)
	}
	update := server.commands[command.CommandID].NodeUpdates["node-2"]
	if update.Status != protocol.ShutdownStatusTimeout {
		t.Fatalf("expected node-2 timeout status, got %s", update.Status)
	}
}

func TestRecordShutdownUpdateReplacesTimeoutWithExecuted(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	issuedAt := time.Now().UTC().Add(-5 * time.Second)
	timeoutAt := issuedAt.Add(1 * time.Second)
	command := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "manual",
		IssuedAt:  issuedAt,
		Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
	}
	completedAt := issuedAt.Add(2 * time.Second)
	server.commands[command.CommandID] = &shutdownCommandState{
		Message: command,
		TargetNodes: map[string]struct{}{
			"node-1": {},
		},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: command.CommandID,
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusTimeout,
				Message:   "command timed out waiting for terminal status",
				AckedAt:   completedAt,
			},
		},
		TimeoutAt:   &timeoutAt,
		CompletedAt: &completedAt,
	}

	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: command.CommandID,
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuted,
		Message:   "shutdown completed after reconnect",
		AckedAt:   completedAt.Add(3 * time.Second),
	})

	update := server.commands[command.CommandID].NodeUpdates["node-1"]
	if update.Status != protocol.ShutdownStatusExecuted {
		t.Fatalf("expected timeout to be replaced with executed, got %s", update.Status)
	}
	summary := server.commandSummary(command.CommandID)
	if summary.Timeout != 0 {
		t.Fatalf("expected timeout count cleared, got %d", summary.Timeout)
	}
	if summary.Executed != 1 {
		t.Fatalf("expected executed count to be 1, got %d", summary.Executed)
	}
	if server.activeCommand != "" {
		t.Fatalf("expected active command to remain cleared after late final ack")
	}
	if server.shutdownIssued.Load() {
		t.Fatalf("expected shutdownIssued to remain false after late final ack")
	}
}

func TestRecordShutdownUpdateDoesNotClearActiveCommandForOlderCompletion(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	issuedAt := time.Now().UTC().Add(-10 * time.Second)
	timeoutAt := issuedAt.Add(1 * time.Second)
	completedAt := issuedAt.Add(2 * time.Second)
	server.commands["shutdown-old"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-old",
			Reason:    "manual",
			IssuedAt:  issuedAt,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{
			"node-1": {},
		},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: "shutdown-old",
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusTimeout,
				Message:   "command timed out waiting for terminal status",
				AckedAt:   completedAt,
			},
		},
		TimeoutAt:   &timeoutAt,
		CompletedAt: &completedAt,
	}
	server.commands["shutdown-new"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-new",
			Reason:    "manual",
			IssuedAt:  time.Now().UTC(),
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-2"}},
		},
		TargetNodes: map[string]struct{}{
			"node-2": {},
		},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-2": {
				CommandID: "shutdown-new",
				NodeID:    "node-2",
				Status:    protocol.ShutdownStatusAccepted,
				AckedAt:   time.Now().UTC(),
			},
		},
	}
	server.activeCommand = "shutdown-new"
	server.shutdownIssued.Store(true)

	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: "shutdown-old",
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuted,
		Message:   "shutdown completed after reconnect",
		AckedAt:   completedAt.Add(3 * time.Second),
	})

	if server.activeCommand != "shutdown-new" {
		t.Fatalf("expected active command to remain shutdown-new, got %q", server.activeCommand)
	}
	if !server.shutdownIssued.Load() {
		t.Fatalf("expected shutdownIssued to remain true while newer command is active")
	}
}

func TestAutoShutdownLatchPreventsRepeatedUPSTriggersUntilReset(t *testing.T) {
	server := NewServer(config.MasterConfig{
		DryRun:         true,
		CommandTimeout: config.Duration{Duration: 30 * time.Second},
		ShutdownPolicy: config.ShutdownPolicy{ShutdownReason: "ups low"},
	})
	server.shutdownIssued.Store(true)
	server.activeCommand = "shutdown-1"
	server.autoShutdownLatched = true
	server.commands["shutdown-1"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-1",
			Reason:    "ups low",
			IssuedAt:  time.Now().UTC(),
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: "shutdown-1",
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusAccepted,
				AckedAt:   time.Now().UTC(),
			},
		},
	}

	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: "shutdown-1",
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuted,
		AckedAt:   time.Now().UTC(),
	})

	server.commandMu.Lock()
	latched := server.autoShutdownLatched
	server.commandMu.Unlock()
	if !latched {
		t.Fatalf("expected auto shutdown latch to remain set after completion")
	}
}

func TestReplayPendingShutdownAllowsTimedOutNodeWithoutFinalState(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	issuedAt := time.Now().UTC().Add(-5 * time.Second)
	timeoutAt := issuedAt.Add(1 * time.Second)
	server.commands["shutdown-timeout"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-timeout",
			Reason:    "ups low",
			IssuedAt:  issuedAt,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: "shutdown-timeout",
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusTimeout,
				AckedAt:   issuedAt.Add(2 * time.Second),
			},
		},
		TimeoutAt: &timeoutAt,
	}
	server.activeCommand = "shutdown-timeout"
	server.shutdownIssued.Store(true)

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.replayPendingShutdown(client)
	}()

	env := readEnvelopeFromConn(t, peer)
	if err := <-errCh; err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}
	if env.Type != protocol.TypeShutdown {
		t.Fatalf("expected shutdown replay, got %s", env.Type)
	}
}

func TestReplayPendingShutdownRequiresActiveCommand(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	issuedAt := time.Now().UTC().Add(-5 * time.Second)
	timeoutAt := issuedAt.Add(1 * time.Second)
	server.commands["shutdown-old"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-old",
			Reason:    "ups low",
			IssuedAt:  issuedAt,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: "shutdown-old",
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusTimeout,
				AckedAt:   issuedAt.Add(2 * time.Second),
			},
		},
		TimeoutAt: &timeoutAt,
	}

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.replayPendingShutdown(client)
	}()

	if err := peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatalf("expected no replay when command is no longer active")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no replay is sent, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}
}

func TestResetActiveCommandStopsReplayOfClearedShutdown(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	issuedAt := time.Now().UTC().Add(-5 * time.Second)
	server.activeCommand = "shutdown-reset"
	server.shutdownIssued.Store(true)
	server.commands["shutdown-reset"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-reset",
			Reason:    "manual",
			IssuedAt:  issuedAt,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{
			"node-1": {
				CommandID: "shutdown-reset",
				NodeID:    "node-1",
				Status:    protocol.ShutdownStatusTimeout,
				AckedAt:   issuedAt.Add(2 * time.Second),
			},
		},
	}

	server.ResetActiveCommand()

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.replayPendingShutdown(client)
	}()

	if err := peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatalf("expected no replay after reset clears the command")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no replay is sent, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("replay pending shutdown after reset: %v", err)
	}
}

func TestResetActiveCommandClearsIssuedFlag(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	server.shutdownIssued.Store(true)
	server.activeCommand = "shutdown-1"
	server.autoShutdownLatched = true

	server.ResetActiveCommand()

	if server.shutdownIssued.Load() {
		t.Fatalf("expected shutdownIssued false after reset")
	}
	if server.activeCommand != "" {
		t.Fatalf("expected active command cleared")
	}
	if server.autoShutdownLatched {
		t.Fatalf("expected auto shutdown latch cleared")
	}
}

func TestStatusIncludesLatestUPSState(t *testing.T) {
	server := NewServer(config.MasterConfig{SNMP: config.SNMPConfig{Target: "10.0.0.31"}})
	successAt := time.Now().UTC().Add(-10 * time.Second)
	errorAt := successAt.Add(5 * time.Second)

	server.recordUPSSuccess(UPSStatus{
		OnBattery:      true,
		BatteryCharge:  24,
		RuntimeMinutes: 11,
	}, successAt)
	server.recordUPSError(errors.New("snmp timeout"), errorAt)

	status := server.Status()
	if status.UPS == nil {
		t.Fatalf("expected UPS status to be present")
	}
	if status.UPS.Target != "10.0.0.31" {
		t.Fatalf("expected target 10.0.0.31, got %q", status.UPS.Target)
	}
	if status.UPS.OnBattery == nil || !*status.UPS.OnBattery {
		t.Fatalf("expected on_battery to remain true")
	}
	if status.UPS.BatteryCharge == nil || *status.UPS.BatteryCharge != 24 {
		t.Fatalf("expected battery_charge 24, got %v", status.UPS.BatteryCharge)
	}
	if status.UPS.RuntimeMinutes == nil || *status.UPS.RuntimeMinutes != 11 {
		t.Fatalf("expected runtime_minutes 11, got %v", status.UPS.RuntimeMinutes)
	}
	if status.UPS.LastSuccessAt == nil || !status.UPS.LastSuccessAt.Equal(successAt) {
		t.Fatalf("expected last_success_at %v, got %v", successAt, status.UPS.LastSuccessAt)
	}
	if status.UPS.LastError != "snmp timeout" {
		t.Fatalf("expected last_error to be recorded, got %q", status.UPS.LastError)
	}
	if status.UPS.LastErrorAt == nil || !status.UPS.LastErrorAt.Equal(errorAt) {
		t.Fatalf("expected last_error_at %v, got %v", errorAt, status.UPS.LastErrorAt)
	}
}

func TestTriggerShutdownStartsLocalShutdownWait(t *testing.T) {
	server := NewServer(config.MasterConfig{
		DryRun:         true,
		CommandTimeout: config.Duration{Duration: 30 * time.Second},
		LocalShutdown: config.LocalShutdownConfig{
			Enabled:                 true,
			Command:                 []string{"shutdown", "-h", "now"},
			MaxWait:                 config.Duration{Duration: 15 * time.Minute},
			EmergencyRuntimeMinutes: 15,
		},
	})

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"
	server.registry.Set(client)

	errCh := make(chan error, 1)
	go func() {
		_, _, err := server.TriggerShutdown(protocol.ShutdownRequest{Reason: "manual"})
		errCh <- err
	}()

	readEnvelopeFromConn(t, peer)
	if err := <-errCh; err != nil {
		t.Fatalf("trigger shutdown: %v", err)
	}

	status := server.Status()
	if status.LocalShutdown == nil {
		t.Fatalf("expected local shutdown status")
	}
	if !status.LocalShutdown.Enabled {
		t.Fatalf("expected local shutdown to be enabled in status")
	}
	if status.LocalShutdown.Phase != protocol.LocalShutdownPhaseWaitingRemote {
		t.Fatalf("expected waiting_remote phase, got %q", status.LocalShutdown.Phase)
	}
	if status.LocalShutdown.CommandID == "" {
		t.Fatalf("expected command id to be recorded")
	}
	if status.LocalShutdown.StartedAt == nil {
		t.Fatalf("expected started_at to be set")
	}
	if status.LocalShutdown.DeadlineAt == nil {
		t.Fatalf("expected deadline_at to be set")
	}
}

func TestResetActiveCommandClearsPendingLocalShutdown(t *testing.T) {
	server := NewServer(config.MasterConfig{
		LocalShutdown: config.LocalShutdownConfig{
			Enabled:                 true,
			Command:                 []string{"shutdown", "-h", "now"},
			MaxWait:                 config.Duration{Duration: 15 * time.Minute},
			EmergencyRuntimeMinutes: 15,
		},
	})
	now := time.Now().UTC()
	deadline := now.Add(15 * time.Minute)
	server.localShutdown = &localShutdownState{
		Phase:      protocol.LocalShutdownPhaseWaitingRemote,
		CommandID:  "shutdown-1",
		StartedAt:  &now,
		DeadlineAt: &deadline,
	}
	server.activeCommand = "shutdown-1"
	server.shutdownIssued.Store(true)

	server.ResetActiveCommand()

	status := server.Status()
	if status.LocalShutdown == nil {
		t.Fatalf("expected local shutdown status")
	}
	if status.LocalShutdown.Phase != protocol.LocalShutdownPhaseIdle {
		t.Fatalf("expected idle phase after reset, got %q", status.LocalShutdown.Phase)
	}
	if status.LocalShutdown.CommandID != "" {
		t.Fatalf("expected command id to be cleared, got %q", status.LocalShutdown.CommandID)
	}
}

func TestRemoteCompletionTriggersLocalShutdown(t *testing.T) {
	server := NewServer(config.MasterConfig{
		DryRun: true,
		LocalShutdown: config.LocalShutdownConfig{
			Enabled:                 true,
			Command:                 []string{"shutdown", "-h", "now"},
			MaxWait:                 config.Duration{Duration: 15 * time.Minute},
			EmergencyRuntimeMinutes: 15,
		},
	})
	recorder := &localShutdownRecorder{}
	server.localShutdownRunner = recorder.run

	issuedAt := time.Now().UTC()
	deadline := issuedAt.Add(15 * time.Minute)
	server.commands["shutdown-1"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-1",
			Reason:    "ups low",
			IssuedAt:  issuedAt,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
	}
	server.activeCommand = "shutdown-1"
	server.shutdownIssued.Store(true)
	server.localShutdown = &localShutdownState{
		Phase:      protocol.LocalShutdownPhaseWaitingRemote,
		CommandID:  "shutdown-1",
		StartedAt:  &issuedAt,
		DeadlineAt: &deadline,
	}

	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: "shutdown-1",
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuted,
		AckedAt:   issuedAt.Add(10 * time.Second),
	})

	if got := recorder.lastTrigger(); got != protocol.LocalShutdownTriggerRemoteComplete {
		t.Fatalf("expected remote_complete trigger, got %q", got)
	}
	status := server.Status()
	if status.LocalShutdown == nil {
		t.Fatalf("expected local shutdown status")
	}
	if status.LocalShutdown.Trigger != protocol.LocalShutdownTriggerRemoteComplete {
		t.Fatalf("expected trigger remote_complete, got %q", status.LocalShutdown.Trigger)
	}
	if status.LocalShutdown.Phase != protocol.LocalShutdownPhaseCompleted {
		t.Fatalf("expected completed phase in dry run, got %q", status.LocalShutdown.Phase)
	}
}

func TestEmergencyRuntimeTriggersFinalReplayAndLocalShutdown(t *testing.T) {
	server := NewServer(config.MasterConfig{
		DryRun: true,
		LocalShutdown: config.LocalShutdownConfig{
			Enabled:                 true,
			Command:                 []string{"shutdown", "-h", "now"},
			MaxWait:                 config.Duration{Duration: 15 * time.Minute},
			EmergencyRuntimeMinutes: 15,
		},
	})
	recorder := &localShutdownRecorder{}
	server.localShutdownRunner = recorder.run

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	client := NewClient(conn)
	client.NodeID = "node-1"
	server.registry.Set(client)

	now := time.Now().UTC()
	deadline := now.Add(15 * time.Minute)
	server.commands["shutdown-1"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-1",
			Reason:    "ups low",
			IssuedAt:  now,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
	}
	server.activeCommand = "shutdown-1"
	server.shutdownIssued.Store(true)
	server.localShutdown = &localShutdownState{
		Phase:      protocol.LocalShutdownPhaseWaitingRemote,
		CommandID:  "shutdown-1",
		StartedAt:  &now,
		DeadlineAt: &deadline,
	}

	server.evaluateLocalShutdown(now.Add(1*time.Minute), &UPSStatus{
		OnBattery:      true,
		BatteryCharge:  10,
		RuntimeMinutes: 5,
	})

	env := readEnvelopeFromConn(t, peer)
	if env.Type != protocol.TypeShutdown {
		t.Fatalf("expected final shutdown replay, got %s", env.Type)
	}
	if got := recorder.lastTrigger(); got != protocol.LocalShutdownTriggerEmergencyRuntime {
		t.Fatalf("expected emergency_runtime trigger, got %q", got)
	}
	status := server.Status()
	if status.LocalShutdown == nil {
		t.Fatalf("expected local shutdown status")
	}
	if status.LocalShutdown.Phase != protocol.LocalShutdownPhaseCompleted {
		t.Fatalf("expected completed phase in dry run, got %q", status.LocalShutdown.Phase)
	}
	if status.LocalShutdown.LastRebroadcastAt == nil {
		t.Fatalf("expected rebroadcast timestamp to be recorded")
	}
}

func TestLocalShutdownWaitExpiryTriggersLocalShutdown(t *testing.T) {
	server := NewServer(config.MasterConfig{
		DryRun: true,
		LocalShutdown: config.LocalShutdownConfig{
			Enabled:                 true,
			Command:                 []string{"shutdown", "-h", "now"},
			MaxWait:                 config.Duration{Duration: 15 * time.Minute},
			EmergencyRuntimeMinutes: 15,
		},
	})
	recorder := &localShutdownRecorder{}
	server.localShutdownRunner = recorder.run

	startedAt := time.Now().UTC().Add(-20 * time.Minute)
	deadline := startedAt.Add(15 * time.Minute)
	server.commands["shutdown-1"] = &shutdownCommandState{
		Message: protocol.ShutdownMessage{
			CommandID: "shutdown-1",
			Reason:    "ups low",
			IssuedAt:  startedAt,
			Target:    protocol.ShutdownTarget{NodeIDs: []string{"node-1"}},
		},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
	}
	server.activeCommand = "shutdown-1"
	server.shutdownIssued.Store(true)
	server.localShutdown = &localShutdownState{
		Phase:      protocol.LocalShutdownPhaseWaitingRemote,
		CommandID:  "shutdown-1",
		StartedAt:  &startedAt,
		DeadlineAt: &deadline,
	}

	server.evaluateLocalShutdown(time.Now().UTC(), nil)

	if got := recorder.lastTrigger(); got != protocol.LocalShutdownTriggerWaitExpired {
		t.Fatalf("expected wait_expired trigger, got %q", got)
	}
	status := server.Status()
	if status.LocalShutdown == nil {
		t.Fatalf("expected local shutdown status")
	}
	if status.LocalShutdown.Trigger != protocol.LocalShutdownTriggerWaitExpired {
		t.Fatalf("expected trigger wait_expired, got %q", status.LocalShutdown.Trigger)
	}
}

func TestRecordUPSSuccessLogsWhenEnabled(t *testing.T) {
	server := NewServer(config.MasterConfig{
		LogUPSStatus: true,
		SNMP:         config.SNMPConfig{Target: "10.0.0.31"},
	})
	logOutput, restoreLog := captureStandardLog(t)
	defer restoreLog()

	server.recordUPSSuccess(UPSStatus{
		OnBattery:      false,
		BatteryCharge:  95,
		RuntimeMinutes: 42,
	}, time.Now().UTC())

	output := logOutput.String()
	if !strings.Contains(output, "ups status target=10.0.0.31 on_battery=false charge=95 runtime_minutes=42") {
		t.Fatalf("expected UPS success log, got %q", output)
	}
}

func TestRecordUPSSuccessSkipsLogWhenDisabled(t *testing.T) {
	server := NewServer(config.MasterConfig{
		SNMP: config.SNMPConfig{Target: "10.0.0.31"},
	})
	logOutput, restoreLog := captureStandardLog(t)
	defer restoreLog()

	server.recordUPSSuccess(UPSStatus{
		OnBattery:      false,
		BatteryCharge:  95,
		RuntimeMinutes: 42,
	}, time.Now().UTC())

	if strings.Contains(logOutput.String(), "ups status") {
		t.Fatalf("expected no UPS success log when switch is disabled, got %q", logOutput.String())
	}
}

func readEnvelope(conn net.Conn) (protocol.Envelope, error) {
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return protocol.Envelope{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return protocol.Envelope{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return protocol.Envelope{}, err
	}
	return env, nil
}

func readEnvelopeFromConn(t *testing.T, conn net.Conn) protocol.Envelope {
	t.Helper()
	env, err := readEnvelope(conn)
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	return env
}

func decodePayloadForTest(t *testing.T, data interface{}, dst interface{}) {
	t.Helper()
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
}

func captureStandardLog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	return &output, func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	}
}

type localShutdownRecorder struct {
	mu       sync.Mutex
	triggers []string
}

func (r *localShutdownRecorder) run(_ []string, trigger string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.triggers = append(r.triggers, trigger)
	return nil
}

func (r *localShutdownRecorder) lastTrigger() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.triggers) == 0 {
		return ""
	}
	return r.triggers[len(r.triggers)-1]
}
