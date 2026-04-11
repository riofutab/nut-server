package master

import (
	"bufio"
	"encoding/json"
	"net"
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
	if server.shutdownIssued.Load() {
		t.Fatalf("expected shutdownIssued false after timeout completion")
	}
	if server.activeCommand != "" {
		t.Fatalf("expected active command cleared after timeout completion")
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
