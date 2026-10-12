package master

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

// fakeSlave is a registered client whose peer side records every envelope the
// master sends to it.
type fakeSlave struct {
	client *Client
	msgs   chan protocol.Envelope
}

func newFakeSlave(t *testing.T, server *Server, nodeID string, tags ...string) *fakeSlave {
	t.Helper()
	slave := connectFakeSlave(t, nodeID, tags...)
	server.registry.Set(slave.client)
	return slave
}

// connectFakeSlave builds a connected client without registering it, for
// exercising the reconnect / replay path.
func connectFakeSlave(t *testing.T, nodeID string, tags ...string) *fakeSlave {
	t.Helper()
	conn, peer := net.Pipe()
	client := NewClient(conn)
	client.NodeID = nodeID
	client.Tags = tags
	slave := &fakeSlave{client: client, msgs: make(chan protocol.Envelope, 16)}
	go func() {
		defer close(slave.msgs)
		reader := bufio.NewReader(peer)
		for {
			env, err := protocol.ReadEnvelope(reader)
			if err != nil {
				return
			}
			slave.msgs <- env
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		_ = peer.Close()
	})
	return slave
}

func (f *fakeSlave) expectShutdown(t *testing.T) protocol.ShutdownMessage {
	t.Helper()
	select {
	case env := <-f.msgs:
		if env.Type != protocol.TypeShutdown {
			t.Fatalf("%s: expected shutdown, got %s", f.client.NodeID, env.Type)
		}
		var message protocol.ShutdownMessage
		decodePayloadForTest(t, env.Data, &message)
		return message
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: expected a shutdown message", f.client.NodeID)
	}
	return protocol.ShutdownMessage{}
}

func (f *fakeSlave) expectNothing(t *testing.T) {
	t.Helper()
	select {
	case env := <-f.msgs:
		t.Fatalf("%s: expected no message, got %s", f.client.NodeID, env.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

// fakeUPS feeds evaluateUPS with a scripted reading.
type fakeUPS struct {
	mu     sync.Mutex
	status UPSStatus
}

func (u *fakeUPS) set(status UPSStatus) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = status
}

func (u *fakeUPS) read(config.SNMPConfig) (UPSStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status, nil
}

func newEpisodeServer(cfg config.MasterConfig) (*Server, *fakeUPS) {
	if cfg.CommandTimeout.Duration == 0 {
		cfg.CommandTimeout = config.Duration{Duration: 30 * time.Second}
	}
	server := NewServer(cfg)
	ups := &fakeUPS{}
	server.upsReader = ups.read
	return server, ups
}

func enabledLocalShutdown() config.LocalShutdownConfig {
	return config.LocalShutdownConfig{
		Enabled:                 true,
		Command:                 []string{"/sbin/shutdown", "-h", "now"},
		MaxWait:                 config.Duration{Duration: 15 * time.Minute},
		EmergencyRuntimeMinutes: 5,
	}
}

type localShutdownCall struct {
	trigger string
	dryRun  bool
}

type localShutdownCalls struct {
	mu    sync.Mutex
	calls []localShutdownCall
	errs  []error
}

func (c *localShutdownCalls) run(_ []string, trigger string, dryRun bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, localShutdownCall{trigger: trigger, dryRun: dryRun})
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		return err
	}
	return nil
}

func (c *localShutdownCalls) snapshot() []localShutdownCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]localShutdownCall(nil), c.calls...)
}

func TestStagedPoliciesWidenTheEpisodeCommand(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "dev-early", When: config.ShutdownPolicyWhen{ChargeBelow: 50}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
			{Name: "critical-all", When: config.ShutdownPolicyWhen{RuntimeBelow: 5}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	dev := newFakeSlave(t, server, "dev-1", "dev")
	web := newFakeSlave(t, server, "web-1", "web")

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 40, RuntimeMinutes: 30})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	first := dev.expectShutdown(t)
	web.expectNothing(t)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 30, RuntimeMinutes: 3})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	second := web.expectShutdown(t)
	dev.expectNothing(t)

	if second.CommandID != first.CommandID {
		t.Fatalf("expected the later policy to widen command %s, got %s", first.CommandID, second.CommandID)
	}
	if !second.Target.All {
		t.Fatalf("expected widened target to be all, got %+v", second.Target)
	}
	status := server.Status()
	if status.Episode == nil || len(status.Episode.FiredPolicies) != 2 {
		t.Fatalf("expected both policies recorded as fired, got %+v", status.Episode)
	}
}

func TestPoliciesAreNotEvaluatedOnLinePower(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "low-charge", When: config.ShutdownPolicyWhen{ChargeBelow: 50}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "node-1")

	ups.set(UPSStatus{OnBattery: false, BatteryCharge: 20, RuntimeMinutes: 10})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	slave.expectNothing(t)
	status := server.Status()
	if status.ActiveCommand != nil || status.Episode != nil {
		t.Fatalf("expected no shutdown activity on line power, got command=%+v episode=%+v", status.ActiveCommand, status.Episode)
	}
}

func TestManualCommandForSomeNodesDoesNotArmLocalShutdown(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{LocalShutdown: enabledLocalShutdown()})
	slave := newFakeSlave(t, server, "node-1")

	done := make(chan error, 1)
	go func() {
		_, _, err := server.TriggerShutdown(protocol.ShutdownRequest{Reason: "test", NodeIDs: []string{"node-1"}})
		done <- err
	}()
	slave.expectShutdown(t)
	if err := <-done; err != nil {
		t.Fatalf("trigger: %v", err)
	}

	if phase := server.Status().LocalShutdown.Phase; phase != protocol.LocalShutdownPhaseIdle {
		t.Fatalf("expected local shutdown to stay idle for a scoped command, got %q", phase)
	}
}

func TestDryRunCommandNeverRunsRealLocalShutdown(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{DryRun: false, LocalShutdown: enabledLocalShutdown()})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	slave := newFakeSlave(t, server, "node-1")

	done := make(chan protocol.ShutdownMessage, 1)
	go func() {
		message, _, _ := server.TriggerShutdown(protocol.ShutdownRequest{Reason: "drill", DryRun: boolPtr(true)})
		done <- message
	}()
	slave.expectShutdown(t)
	message := <-done

	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID,
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusSimulated,
		AckedAt:   time.Now().UTC(),
	})

	got := calls.snapshot()
	if len(got) != 1 || !got[0].dryRun {
		t.Fatalf("expected one dry-run local shutdown, got %+v", got)
	}
}

func TestAutoTriggerWithoutConnectedSlavesStillPowersOffMasterAtMaxWait(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{OnBattery: boolPtr(true)}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	status := server.Status()
	if status.ActiveCommand == nil {
		t.Fatal("expected an episode command even with no connected slaves")
	}
	if status.LocalShutdown.Phase != protocol.LocalShutdownPhaseWaitingRemote {
		t.Fatalf("expected waiting_remote, got %q", status.LocalShutdown.Phase)
	}

	server.evaluateLocalShutdown(time.Now().UTC().Add(16*time.Minute), &UPSStatus{OnBattery: true, RuntimeMinutes: 60})

	got := calls.snapshot()
	if len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerWaitExpired {
		t.Fatalf("expected wait_expired local shutdown, got %+v", got)
	}
}

func TestFailedNodeIsRetriedAndLaterSuccessReplacesFailure(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{OnBattery: boolPtr(true)}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "node-1")
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	message := slave.expectShutdown(t)
	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID, NodeID: "node-1", Status: protocol.ShutdownStatusFailed, AckedAt: time.Now().UTC(),
	})

	server.retryEpisodeShutdown(time.Now().UTC().Add(10 * time.Second))
	slave.expectNothing(t)
	server.retryEpisodeShutdown(time.Now().UTC().Add(shutdownRetryInterval + time.Second))
	retried := slave.expectShutdown(t)

	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: retried.CommandID, NodeID: "node-1", Status: protocol.ShutdownStatusExecuted, AckedAt: time.Now().UTC(),
	})
	status := server.Status()
	if status.ActiveCommand == nil || status.ActiveCommand.Executed != 1 || status.ActiveCommand.Failed != 0 {
		t.Fatalf("expected executed to replace failed, got %+v", status.ActiveCommand)
	}
}

func TestTagTargetedCommandAdoptsNodeThatReconnectsLater(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "dev", When: config.ShutdownPolicyWhen{OnBattery: boolPtr(true)}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
		},
	})
	first := newFakeSlave(t, server, "dev-1", "dev")
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	first.expectShutdown(t)

	late := connectFakeSlave(t, "dev-2", "dev")
	other := connectFakeSlave(t, "web-1", "web")
	go func() { _ = server.replayPendingShutdown(late.client) }()
	go func() { _ = server.replayPendingShutdown(other.client) }()

	late.expectShutdown(t)
	other.expectNothing(t)
	if targeted := server.Status().ActiveCommand.Targeted; targeted != 2 {
		t.Fatalf("expected the reconnecting dev node to be adopted, targeted=%d", targeted)
	}
}

func TestRestoredEpisodeIsNotReplayedBeforeFirstUPSReading(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{})
	now := time.Now().UTC()
	server.commands["shutdown-a"] = &shutdownCommandState{
		Message:     protocol.ShutdownMessage{CommandID: "shutdown-a", IssuedAt: now, Target: protocol.ShutdownTarget{All: true}},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
		Auto:        true,
	}
	server.activeCommand = "shutdown-a"
	server.episode = &autoEpisode{StartedAt: now, CommandID: "shutdown-a"}
	slave := connectFakeSlave(t, "node-1")

	go func() { _ = server.replayPendingShutdown(slave.client) }()
	slave.expectNothing(t)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 50, RuntimeMinutes: 20})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	go func() { _ = server.replayPendingShutdown(slave.client) }()
	slave.expectShutdown(t)
}

func TestLinePowerEndsEpisodeAndCancelsPendingLocalShutdown(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{OnBattery: boolPtr(true)}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "node-1")
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	slave.expectShutdown(t)

	ups.set(UPSStatus{OnBattery: false, BatteryCharge: 85, RuntimeMinutes: 55})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	status := server.Status()
	if status.Episode != nil || status.ActiveCommand != nil {
		t.Fatalf("expected episode and command released, got episode=%+v command=%+v", status.Episode, status.ActiveCommand)
	}
	if status.LocalShutdown.Phase != protocol.LocalShutdownPhaseIdle {
		t.Fatalf("expected local shutdown cancelled, got %q", status.LocalShutdown.Phase)
	}
	go func() { _ = server.replayPendingShutdown(slave.client) }()
	slave.expectNothing(t)
}

func TestEmergencyRuntimePowersOffMasterWithoutAnyCommand(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "never", When: config.ShutdownPolicyWhen{RuntimeBelow: 1}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	reading := UPSStatus{OnBattery: true, BatteryCharge: 15, RuntimeMinutes: 3}
	ups.set(reading)
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	server.evaluateLocalShutdown(time.Now().UTC(), &reading)

	got := calls.snapshot()
	if len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerEmergencyRuntime {
		t.Fatalf("expected emergency local shutdown, got %+v", got)
	}
}

func TestFailedLocalShutdownIsRetriedAfterInterval(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{LocalShutdown: enabledLocalShutdown()})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	now := time.Now().UTC()
	lastAttempt := now.Add(-10 * time.Second)
	server.commands["shutdown-a"] = &shutdownCommandState{
		Message:     protocol.ShutdownMessage{CommandID: "shutdown-a", IssuedAt: now, Target: protocol.ShutdownTarget{All: true}},
		TargetNodes: map[string]struct{}{},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
	}
	server.localShutdown = &localShutdownState{
		Phase:         protocol.LocalShutdownPhaseFailed,
		CommandID:     "shutdown-a",
		Trigger:       protocol.LocalShutdownTriggerWaitExpired,
		LastAttemptAt: &lastAttempt,
	}

	server.evaluateLocalShutdown(now, nil)
	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("expected no retry before the interval, got %+v", got)
	}
	server.evaluateLocalShutdown(lastAttempt.Add(shutdownRetryInterval+time.Second), nil)

	if got := calls.snapshot(); len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerWaitExpired {
		t.Fatalf("expected one retry, got %+v", got)
	}
	if phase := server.Status().LocalShutdown.Phase; phase != protocol.LocalShutdownPhaseCompleted {
		t.Fatalf("expected completed after successful retry, got %q", phase)
	}
}

func TestAutoShutdownSupersedesActiveManualCommand(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{OnBattery: boolPtr(true)}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "node-1")
	other := newFakeSlave(t, server, "node-2")
	done := make(chan protocol.ShutdownMessage, 1)
	go func() {
		message, _, _ := server.TriggerShutdown(protocol.ShutdownRequest{Reason: "test", NodeIDs: []string{"node-1"}})
		done <- message
	}()
	slave.expectShutdown(t)
	manual := <-done

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	if err := server.evaluateUPS(); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	auto := other.expectShutdown(t)
	if auto.CommandID == manual.CommandID {
		t.Fatal("expected a new auto command")
	}
	server.commandMu.Lock()
	defer server.commandMu.Unlock()
	if server.activeCommand != auto.CommandID || !server.commands[manual.CommandID].ReplayDisabled {
		t.Fatalf("expected manual command superseded, active=%q", server.activeCommand)
	}
}

func TestShutdownAckWithUnsupportedStatusIsIgnored(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{})
	server.commands["shutdown-a"] = &shutdownCommandState{
		Message:     protocol.ShutdownMessage{CommandID: "shutdown-a"},
		TargetNodes: map[string]struct{}{"node-1": {}},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
	}
	client := &Client{NodeID: "node-1"}

	for _, status := range []string{"bogus", protocol.ShutdownStatusTimeout} {
		err := server.handleEnvelope(client, protocol.Envelope{
			Type: protocol.TypeShutdownAck,
			Data: map[string]any{"command_id": "shutdown-a", "status": status},
		})
		if err != nil {
			t.Fatalf("handle ack %q: %v", status, err)
		}
	}

	if updates := server.commands["shutdown-a"].NodeUpdates; len(updates) != 0 {
		t.Fatalf("expected unsupported statuses to be dropped, got %+v", updates)
	}
}

func TestCommandIDsAreUniqueWithoutPersistedCounter(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 15, 0, 0, time.UTC)

	a, b := newCommandID(now), newCommandID(now)

	if a == b {
		t.Fatalf("expected distinct ids, got %q twice", a)
	}
	if !strings.HasPrefix(a, "shutdown-20261012T101500Z-") {
		t.Fatalf("unexpected id format %q", a)
	}
}

func TestManualShutdownRejectsOverlongTimeout(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{})
	newFakeSlave(t, server, "node-1")
	tooLong := maxManualTimeoutSeconds + 1

	_, _, err := server.TriggerShutdown(protocol.ShutdownRequest{TimeoutSeconds: &tooLong})

	if err == nil {
		t.Fatal("expected overlong timeout_seconds to be rejected")
	}
}

func TestStatusShowsSlaveReportedDryRunAndReadiness(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{})
	server.directory.Observe("node-1", "node-1.lan", nil, time.Now().UTC())

	server.directory.SetSlaveInfo("node-1", true, boolPtr(false))

	nodes := server.Status().Nodes
	if len(nodes) != 1 || !nodes[0].DryRun || nodes[0].ShutdownReady == nil || *nodes[0].ShutdownReady {
		t.Fatalf("expected dry_run and shutdown_ready=false, got %+v", nodes)
	}
}

func TestLocalShutdownSelfCheckReportsFailureInStatus(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{LocalShutdown: enabledLocalShutdown()})
	server.checkShutdown = func(context.Context, []string) error { return errors.New("sudo: a password is required") }

	server.runLocalShutdownSelfCheck(context.Background())

	ready := server.Status().LocalShutdown.CommandReady
	if ready == nil || *ready {
		t.Fatalf("expected command_ready=false, got %v", ready)
	}
}

func TestLocalShutdownSelfCheckSkippedInDryRun(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{DryRun: true, LocalShutdown: enabledLocalShutdown()})
	server.checkShutdown = func(context.Context, []string) error {
		t.Fatal("self-check must not run in dry-run")
		return nil
	}

	server.runLocalShutdownSelfCheck(context.Background())

	if ready := server.Status().LocalShutdown.CommandReady; ready != nil {
		t.Fatalf("expected no readiness in dry-run, got %v", *ready)
	}
}

func TestAdminRejectsOversizedRequestBody(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{AdminToken: "secret"})
	handler := limitRequestBody(server.requireAdminToken(server.handleManualShutdown))
	body := `{"reason":"` + strings.Repeat("x", maxAdminRequestBody) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/commands/shutdown", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized body, got %d", rec.Code)
	}
}

func TestAcceptRetryDelayBacksOffToCap(t *testing.T) {
	delay := time.Duration(0)
	for range 20 {
		delay = nextAcceptRetryDelay(delay)
	}
	if delay != maxAcceptRetryDelay {
		t.Fatalf("expected delay capped at %v, got %v", maxAcceptRetryDelay, delay)
	}
	if first := nextAcceptRetryDelay(0); first != minAcceptRetryDelay {
		t.Fatalf("expected first delay %v, got %v", minAcceptRetryDelay, first)
	}
}
