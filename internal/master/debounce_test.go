package master

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

var errUPSUnreachable = errors.New("snmp timeout")

func pollAt(t *testing.T, server *Server, at time.Time) {
	t.Helper()
	if err := server.evaluateUPS(at); err != nil {
		t.Fatalf("evaluate at %s: %v", at, err)
	}
}

func failPollAt(t *testing.T, server *Server, ups *fakeUPS, at time.Time) {
	t.Helper()
	ups.fail(errUPSUnreachable)
	if err := server.evaluateUPS(at); err == nil {
		t.Fatalf("expected poll at %s to fail", at)
	}
}

func debouncedServer(t *testing.T, duration time.Duration) (*Server, *fakeUPS, *fakeSlave) {
	t.Helper()
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{{
			Name:     "low-charge",
			When:     config.ShutdownPolicyWhen{ChargeBelow: 50},
			Duration: config.Duration{Duration: duration},
			Target:   config.ShutdownPolicyTarget{All: true},
		}},
	})
	return server, ups, newFakeSlave(t, server, "node-1")
}

func TestDebouncedPolicyIgnoresGlitchThatRecovers(t *testing.T) {
	server, ups, slave := debouncedServer(t, 30*time.Second)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0)
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(60), RuntimeMinutes: new(30)})
	pollAt(t, server, t0.Add(10*time.Second))
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0.Add(20*time.Second))
	pollAt(t, server, t0.Add(40*time.Second))

	slave.expectNothing(t)
	if status := server.Status(); status.ActiveCommand != nil {
		t.Fatalf("expected no command before the condition held for 30s, got %+v", status.ActiveCommand)
	}
}

func TestDebouncedPolicyFiresAfterConditionHoldsForDuration(t *testing.T) {
	server, ups, slave := debouncedServer(t, 30*time.Second)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0)
	pollAt(t, server, t0.Add(29*time.Second))
	slave.expectNothing(t)
	pollAt(t, server, t0.Add(30*time.Second))

	message := slave.expectShutdown(t)
	if !message.Target.All {
		t.Fatalf("expected an all-node shutdown, got %+v", message.Target)
	}
}

func TestDebounceSurvivesFailedPollButNeedsFreshReadingToFire(t *testing.T) {
	server, ups, slave := debouncedServer(t, 30*time.Second)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0)
	failPollAt(t, server, ups, t0.Add(31*time.Second))
	slave.expectNothing(t)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0.Add(32*time.Second))

	slave.expectShutdown(t)
}

func TestDebounceRestartsInNewEpisode(t *testing.T) {
	server, ups, slave := debouncedServer(t, 30*time.Second)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0)
	ups.set(UPSStatus{OnBattery: false, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0.Add(10*time.Second))
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(30)})
	pollAt(t, server, t0.Add(20*time.Second))
	pollAt(t, server, t0.Add(31*time.Second))

	slave.expectNothing(t)
}

func unreachablePolicyServer(t *testing.T) (*Server, *fakeUPS, *fakeSlave) {
	t.Helper()
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "low-charge", When: config.ShutdownPolicyWhen{ChargeBelow: 10}, Target: config.ShutdownPolicyTarget{All: true}},
			{Name: "lost-ups", When: config.ShutdownPolicyWhen{UPSUnreachableFor: config.Duration{Duration: 2 * time.Minute}}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	return server, ups, newFakeSlave(t, server, "node-1")
}

func TestUnreachablePolicyFiresWhenUPSSilentDuringEpisode(t *testing.T) {
	server, ups, slave := unreachablePolicyServer(t)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(90), RuntimeMinutes: new(60)})
	pollAt(t, server, t0)
	failPollAt(t, server, ups, t0.Add(time.Minute))
	slave.expectNothing(t)
	failPollAt(t, server, ups, t0.Add(2*time.Minute))

	slave.expectShutdown(t)
	if fired := server.Status().Episode.FiredPolicies; len(fired) != 1 || fired[0] != "lost-ups" {
		t.Fatalf("expected lost-ups to fire alone, got %v", fired)
	}
}

func TestUnreachablePolicyIgnoredOnLinePower(t *testing.T) {
	server, ups, slave := unreachablePolicyServer(t)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: false, BatteryCharge: new(100), RuntimeMinutes: new(60)})
	pollAt(t, server, t0)
	failPollAt(t, server, ups, t0.Add(10*time.Minute))

	slave.expectNothing(t)
}

func TestUnreachablePolicyNotMatchedBySuccessfulPoll(t *testing.T) {
	server, ups, slave := unreachablePolicyServer(t)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(90), RuntimeMinutes: new(60)})
	pollAt(t, server, t0)
	pollAt(t, server, t0.Add(5*time.Minute))

	slave.expectNothing(t)
}

func unreachableLocalShutdownServer(t *testing.T) (*Server, *fakeUPS, *localShutdownCalls) {
	t.Helper()
	local := enabledLocalShutdown()
	local.UnreachableAfter = config.Duration{Duration: 5 * time.Minute}
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: local,
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "low-charge", When: config.ShutdownPolicyWhen{ChargeBelow: 10}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	return server, ups, calls
}

func TestLocalShutdownPowersOffMasterWhenUPSUnreachableDuringEpisode(t *testing.T) {
	server, ups, calls := unreachableLocalShutdownServer(t)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(90), RuntimeMinutes: new(60)})
	pollAt(t, server, t0)
	failPollAt(t, server, ups, t0.Add(4*time.Minute))
	server.evaluateLocalShutdown(t0.Add(4*time.Minute), server.latestSuccessfulUPSStatus())
	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("expected no local shutdown before unreachable_after, got %+v", got)
	}

	failPollAt(t, server, ups, t0.Add(5*time.Minute))
	server.evaluateLocalShutdown(t0.Add(5*time.Minute), server.latestSuccessfulUPSStatus())

	got := calls.snapshot()
	if len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerUPSUnreachable {
		t.Fatalf("expected one ups_unreachable local shutdown, got %+v", got)
	}
}

func TestLocalShutdownIgnoresUnreachableUPSOnLinePower(t *testing.T) {
	server, ups, calls := unreachableLocalShutdownServer(t)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: false, BatteryCharge: new(100), RuntimeMinutes: new(60)})
	pollAt(t, server, t0)
	failPollAt(t, server, ups, t0.Add(time.Hour))
	server.evaluateLocalShutdown(t0.Add(time.Hour), server.latestSuccessfulUPSStatus())

	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("expected no local shutdown on line power, got %+v", got)
	}
}

func TestUnreachableEscalatesWaitingLocalShutdown(t *testing.T) {
	local := enabledLocalShutdown()
	local.UnreachableAfter = config.Duration{Duration: 5 * time.Minute}
	server, ups := newEpisodeServer(config.MasterConfig{
		LocalShutdown: local,
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	newFakeSlave(t, server, "node-1")
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(90), RuntimeMinutes: new(60)})
	pollAt(t, server, t0)
	if phase := server.Status().LocalShutdown.Phase; phase != protocol.LocalShutdownPhaseWaitingRemote {
		t.Fatalf("expected waiting_remote, got %q", phase)
	}
	failPollAt(t, server, ups, t0.Add(6*time.Minute))
	server.evaluateLocalShutdown(t0.Add(6*time.Minute), server.latestSuccessfulUPSStatus())

	got := calls.snapshot()
	if len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerUPSUnreachable {
		t.Fatalf("expected waiting local shutdown to escalate on ups_unreachable, got %+v", got)
	}
}

// restartedServer persists an on-battery episode from a first master and
// returns a second master that restored it from the state file without ever
// reading the UPS.
func restartedServer(t *testing.T, cfg config.MasterConfig, crashAt, restartAt time.Time) (*Server, *fakeUPS) {
	t.Helper()
	cfg.StateFile = filepath.Join(t.TempDir(), "master-state.json")
	before, ups := newEpisodeServer(cfg)
	before.localShutdownRunner = (&localShutdownCalls{}).run
	ups.set(UPSStatus{OnBattery: true, BatteryCharge: new(90), RuntimeMinutes: new(60)})
	pollAt(t, before, crashAt)

	after, ups := newEpisodeServer(cfg)
	after.startedAt = restartAt
	if after.Status().Episode == nil {
		t.Fatal("expected the episode to be restored from the state file")
	}
	return after, ups
}

func TestUnreachablePolicyFiresForRestoredEpisodeWhenUPSNeverAnswers(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	server, ups := restartedServer(t, config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "low-charge", When: config.ShutdownPolicyWhen{ChargeBelow: 10}, Target: config.ShutdownPolicyTarget{All: true}},
			{Name: "lost-ups", When: config.ShutdownPolicyWhen{UPSUnreachableFor: config.Duration{Duration: 2 * time.Minute}}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	}, t0, t0.Add(10*time.Minute))
	slave := newFakeSlave(t, server, "node-1")

	failPollAt(t, server, ups, t0.Add(11*time.Minute))
	slave.expectNothing(t)
	failPollAt(t, server, ups, t0.Add(12*time.Minute))

	slave.expectShutdown(t)
}

func TestLocalShutdownPowersOffMasterForRestoredEpisodeWhenUPSNeverAnswers(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	local := enabledLocalShutdown()
	local.UnreachableAfter = config.Duration{Duration: 5 * time.Minute}
	server, ups := restartedServer(t, config.MasterConfig{
		LocalShutdown: local,
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	}, t0, t0.Add(10*time.Minute))
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	newFakeSlave(t, server, "node-1")

	failPollAt(t, server, ups, t0.Add(14*time.Minute))
	server.evaluateLocalShutdown(t0.Add(14*time.Minute), server.latestSuccessfulUPSStatus())
	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("expected no local shutdown before unreachable_after since restart, got %+v", got)
	}
	failPollAt(t, server, ups, t0.Add(15*time.Minute))
	server.evaluateLocalShutdown(t0.Add(15*time.Minute), server.latestSuccessfulUPSStatus())

	got := calls.snapshot()
	if len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerUPSUnreachable {
		t.Fatalf("expected the restored local shutdown to escalate on ups_unreachable, got %+v", got)
	}
}

func TestRestoredEpisodeCommandIsReplayedOnReconnectOnceUnreachableFallbackFires(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	server, ups := restartedServer(t, config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "on-battery", When: config.ShutdownPolicyWhen{}, Target: config.ShutdownPolicyTarget{All: true}},
			{Name: "lost-ups", When: config.ShutdownPolicyWhen{UPSUnreachableFor: config.Duration{Duration: 2 * time.Minute}}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	}, t0, t0.Add(10*time.Minute))
	reconnected := connectFakeSlave(t, "node-1")

	failPollAt(t, server, ups, t0.Add(12*time.Minute))
	if err := server.replayPendingShutdown(reconnected.client); err != nil {
		t.Fatalf("replay: %v", err)
	}

	reconnected.expectShutdown(t)
}

func TestUnreachableFallbackIgnoredWithoutEpisodeWhenUPSNeverAnswers(t *testing.T) {
	server, ups, slave := unreachablePolicyServer(t)
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	server.startedAt = t0

	failPollAt(t, server, ups, t0.Add(time.Hour))

	slave.expectNothing(t)
}
