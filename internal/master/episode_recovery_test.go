package master

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

// completeDryRunDrill runs a manual dry-run "all" command to completion, which
// leaves a completed dry-run local shutdown behind.
func completeDryRunDrill(t *testing.T, server *Server, slave *fakeSlave, calls *localShutdownCalls) {
	t.Helper()
	done := make(chan protocol.ShutdownMessage, 1)
	go func() {
		message, _, _ := server.TriggerShutdown(protocol.ShutdownRequest{Reason: "drill", DryRun: boolPtr(true)})
		done <- message
	}()
	slave.expectShutdown(t)
	message := <-done
	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID, NodeID: slave.client.NodeID, Status: protocol.ShutdownStatusSimulated, AckedAt: time.Now().UTC(),
	})
	if got := calls.snapshot(); len(got) != 1 || !got[0].dryRun {
		t.Fatalf("expected the drill to run one dry-run local shutdown, got %+v", got)
	}
}

func TestCompletedDryRunDrillDoesNotBlockLocalShutdownInLaterOutage(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	slave := newFakeSlave(t, server, "node-1")
	completeDryRunDrill(t, server, slave, calls)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	pollAt(t, server, time.Now().UTC())
	message := slave.expectShutdown(t)
	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID, NodeID: "node-1", Status: protocol.ShutdownStatusExecuted, AckedAt: time.Now().UTC(),
	})

	got := calls.snapshot()
	if len(got) != 2 || got[1].dryRun || got[1].trigger != protocol.LocalShutdownTriggerRemoteComplete {
		t.Fatalf("expected a real remote_complete local shutdown after the drill, got %+v", got)
	}
}

func TestEmergencyRunsAfterCompletedDryRunDrill(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "never", When: config.ShutdownPolicyWhen{RuntimeBelow: 1}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	slave := newFakeSlave(t, server, "node-1")
	completeDryRunDrill(t, server, slave, calls)

	reading := UPSStatus{OnBattery: true, BatteryCharge: 15, RuntimeMinutes: 3}
	ups.set(reading)
	now := time.Now().UTC()
	pollAt(t, server, now)
	server.evaluateLocalShutdown(now, &reading)

	got := calls.snapshot()
	if len(got) != 2 || got[1].dryRun || got[1].trigger != protocol.LocalShutdownTriggerEmergencyRuntime {
		t.Fatalf("expected a real emergency local shutdown after the drill, got %+v", got)
	}
}

func TestEmergencyDuringPendingDryRunDrillRunsRealShutdown(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "never", When: config.ShutdownPolicyWhen{RuntimeBelow: 1}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	slave := newFakeSlave(t, server, "node-1")
	go func() {
		_, _, _ = server.TriggerShutdown(protocol.ShutdownRequest{Reason: "drill", DryRun: boolPtr(true)})
	}()
	slave.expectShutdown(t)

	reading := UPSStatus{OnBattery: true, BatteryCharge: 15, RuntimeMinutes: 3}
	ups.set(reading)
	now := time.Now().UTC()
	pollAt(t, server, now)
	server.evaluateLocalShutdown(now, &reading)

	got := calls.snapshot()
	if len(got) != 1 || got[0].dryRun || got[0].trigger != protocol.LocalShutdownTriggerEmergencyRuntime {
		t.Fatalf("expected the emergency to run a real local shutdown, got %+v", got)
	}
}

func TestResetStopsRetriesOfFailedLocalShutdown(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{LocalShutdown: enabledLocalShutdown()})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	now := time.Now().UTC()
	server.commands["shutdown-a"] = &shutdownCommandState{
		Message:     protocol.ShutdownMessage{CommandID: "shutdown-a", IssuedAt: now, Target: protocol.ShutdownTarget{All: true}},
		TargetNodes: map[string]struct{}{},
		NodeUpdates: map[string]protocol.ShutdownAckMessage{},
	}
	server.localShutdown = &localShutdownState{
		Phase:         protocol.LocalShutdownPhaseFailed,
		CommandID:     "shutdown-a",
		Trigger:       protocol.LocalShutdownTriggerRemoteComplete,
		LastAttemptAt: &now,
	}

	server.ResetActiveCommand()
	server.evaluateLocalShutdown(now.Add(shutdownRetryInterval+time.Second), nil)

	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("expected no retry after reset, got %+v", got)
	}
	if phase := server.Status().LocalShutdown.Phase; phase != protocol.LocalShutdownPhaseIdle {
		t.Fatalf("expected idle after reset, got %q", phase)
	}
}

func TestResetDuringOutageIsNotUndoneByNextPoll(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "node-1")
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	pollAt(t, server, t0)
	slave.expectShutdown(t)

	server.ResetActiveCommand()
	pollAt(t, server, t0.Add(10*time.Second))

	slave.expectNothing(t)
	if status := server.Status(); status.ActiveCommand != nil {
		t.Fatalf("expected no command after reset, got %+v", status.ActiveCommand)
	}
}

func TestPolicyNotYetFiredStillFiresAfterResetDuringOutage(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
			{Name: "low-charge", When: config.ShutdownPolicyWhen{ChargeBelow: 20}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "dev-1", "dev")
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	pollAt(t, server, t0)
	slave.expectShutdown(t)
	server.ResetActiveCommand()

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 10, RuntimeMinutes: 5})
	pollAt(t, server, t0.Add(time.Minute))

	message := slave.expectShutdown(t)
	if !message.Target.All {
		t.Fatalf("expected the low-charge policy to issue an all-node command, got %+v", message.Target)
	}
}

func TestLinePowerCancelsFailedEmergencyArmedBeforeEpisodeCommand(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "dev-late", When: config.ShutdownPolicyWhen{ChargeBelow: 10}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
		},
	})
	calls := &localShutdownCalls{errs: []error{errors.New("polkit refused")}}
	server.localShutdownRunner = calls.run
	reading := UPSStatus{OnBattery: true, BatteryCharge: 15, RuntimeMinutes: 3}
	ups.set(reading)
	now := time.Now().UTC()
	pollAt(t, server, now)
	server.evaluateLocalShutdown(now, &reading)
	if phase := server.Status().LocalShutdown.Phase; phase != protocol.LocalShutdownPhaseFailed {
		t.Fatalf("expected the emergency to fail, got %q", phase)
	}
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 5, RuntimeMinutes: 3})
	pollAt(t, server, now.Add(time.Second))
	if server.Status().Episode.CommandID == "" {
		t.Fatal("expected dev-late to create the episode command")
	}

	ups.set(UPSStatus{OnBattery: false, BatteryCharge: 5, RuntimeMinutes: 3})
	pollAt(t, server, now.Add(2*time.Second))
	server.evaluateLocalShutdown(now.Add(shutdownRetryInterval+time.Minute), server.latestSuccessfulUPSStatus())

	if got := calls.snapshot(); len(got) != 1 {
		t.Fatalf("expected no retry on line power, got %+v", got)
	}
	if phase := server.Status().LocalShutdown.Phase; phase != protocol.LocalShutdownPhaseIdle {
		t.Fatalf("expected line power to cancel the failed emergency, got %q", phase)
	}
}

func TestEmergencyRuntimeIgnoresLinePowerReading(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{LocalShutdown: enabledLocalShutdown()})

	trigger := server.emergencyTrigger(&UPSStatus{OnBattery: false, BatteryCharge: 15, RuntimeMinutes: 3}, time.Now().UTC())

	if trigger != "" {
		t.Fatalf("expected no emergency on line power, got %q", trigger)
	}
}

func TestRestoredEpisodeAdoptsNodeThatReconnectedBeforeFirstReading(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	server, ups := restartedServer(t, config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	}, t0, t0.Add(10*time.Minute))
	slave := newFakeSlave(t, server, "node-1")
	if err := server.replayPendingShutdown(slave.client); err != nil {
		t.Fatalf("replay: %v", err)
	}
	slave.expectNothing(t)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 80, RuntimeMinutes: 50})
	pollAt(t, server, t0.Add(11*time.Minute))
	server.retryEpisodeShutdown(t0.Add(11*time.Minute + time.Second))

	slave.expectShutdown(t)
	if targeted := server.Status().ActiveCommand.Targeted; targeted != 1 {
		t.Fatalf("expected the reconnected node to be tracked, targeted=%d", targeted)
	}
}

func TestRetriedNodeThatStallsTimesOutAgain(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "node-1")
	t0 := time.Now().UTC()
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	pollAt(t, server, t0)
	message := slave.expectShutdown(t)
	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID, NodeID: "node-1", Status: protocol.ShutdownStatusFailed, AckedAt: t0,
	})
	retryAt := t0.Add(shutdownRetryInterval + time.Second)
	server.retryEpisodeShutdown(retryAt)
	slave.expectShutdown(t)
	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID, NodeID: "node-1", Status: protocol.ShutdownStatusAccepted, AckedAt: retryAt,
	})

	server.markTimedOutCommands(retryAt.Add(server.cfg.CommandTimeout.Duration + time.Second))

	if timedOut := server.Status().ActiveCommand.Timeout; timedOut != 1 {
		t.Fatalf("expected the stalled retry to time out again, got %+v", server.Status().ActiveCommand)
	}
}

func TestRestoredFiredUnreachablePolicyConfirmsEpisodeWhileUPSStaysSilent(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	cfg := config.MasterConfig{
		StateFile: filepath.Join(t.TempDir(), "master-state.json"),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "lost-ups", When: config.ShutdownPolicyWhen{UPSUnreachableFor: config.Duration{Duration: 2 * time.Minute}}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	}
	before, ups := newEpisodeServer(cfg)
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	pollAt(t, before, t0)
	failPollAt(t, before, ups, t0.Add(2*time.Minute))
	if fired := before.Status().Episode.FiredPolicies; len(fired) != 1 {
		t.Fatalf("expected lost-ups to fire before the restart, got %v", fired)
	}
	after, ups := newEpisodeServer(cfg)
	after.startedAt = t0.Add(10 * time.Minute)
	reconnected := connectFakeSlave(t, "node-1")

	failPollAt(t, after, ups, t0.Add(12*time.Minute))
	go func() { _ = after.replayPendingShutdown(reconnected.client) }()

	reconnected.expectShutdown(t)
}

// poweredOffMaster runs an outage in which the master powers itself off for
// real, and returns a second master restored from that state file.
func poweredOffMaster(t *testing.T, t0, restartAt time.Time) (*Server, *fakeUPS, *localShutdownCalls) {
	t.Helper()
	local := enabledLocalShutdown()
	local.UnreachableAfter = config.Duration{Duration: 5 * time.Minute}
	cfg := config.MasterConfig{
		StateFile:     filepath.Join(t.TempDir(), "master-state.json"),
		LocalShutdown: local,
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
			{Name: "lost-ups", When: config.ShutdownPolicyWhen{UPSUnreachableFor: config.Duration{Duration: 2 * time.Minute}}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	}
	before, ups := newEpisodeServer(cfg)
	beforeCalls := &localShutdownCalls{}
	before.localShutdownRunner = beforeCalls.run
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 90, RuntimeMinutes: 60})
	pollAt(t, before, t0)
	before.evaluateLocalShutdown(t0.Add(16*time.Minute), before.latestSuccessfulUPSStatus())
	if got := beforeCalls.snapshot(); len(got) != 1 || got[0].dryRun {
		t.Fatalf("expected the first master to power itself off, got %+v", got)
	}

	after, ups := newEpisodeServer(cfg)
	after.startedAt = restartAt
	calls := &localShutdownCalls{}
	after.localShutdownRunner = calls.run
	return after, ups, calls
}

func TestMasterThatPoweredItselfOffWaitsForUPSReadingAfterBoot(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	restartAt := t0.Add(time.Hour)
	server, ups, calls := poweredOffMaster(t, t0, restartAt)
	slave := connectFakeSlave(t, "node-1")

	failPollAt(t, server, ups, restartAt.Add(10*time.Minute))
	server.evaluateLocalShutdown(restartAt.Add(10*time.Minute), server.latestSuccessfulUPSStatus())
	go func() { _ = server.replayPendingShutdown(slave.client) }()

	slave.expectNothing(t)
	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("expected no local shutdown before the UPS answers, got %+v", got)
	}
	ups.set(UPSStatus{OnBattery: false, BatteryCharge: 40, RuntimeMinutes: 20})
	pollAt(t, server, restartAt.Add(11*time.Minute))
	if episode := server.Status().Episode; episode != nil {
		t.Fatalf("expected the first reading on line power to end the episode, got %+v", episode)
	}
}

func TestMasterThatPoweredItselfOffResumesFallbackAfterReadingOnBattery(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	restartAt := t0.Add(time.Hour)
	server, ups, calls := poweredOffMaster(t, t0, restartAt)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: 30, RuntimeMinutes: 20})
	pollAt(t, server, restartAt.Add(time.Minute))
	failPollAt(t, server, ups, restartAt.Add(7*time.Minute))
	server.evaluateLocalShutdown(restartAt.Add(7*time.Minute), server.latestSuccessfulUPSStatus())

	got := calls.snapshot()
	if len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerUPSUnreachable {
		t.Fatalf("expected the unreachable fallback to work again after a reading, got %+v", got)
	}
}
