//go:build e2e

package e2e

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"nut-server/internal/protocol"
)

func TestE2EMetricsReflectsClosedLoop(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm := startMaster(t, masterCfg)

	slaveCfg := newSlaveConfig(t, masterCfg, "slave-metrics")
	startSlave(t, slaveCfg)

	waitUntil(t, "slave online", func() bool {
		status := rm.Status(t)
		node := nodeByID(status, "slave-metrics")
		return node != nil && node.State == protocol.NodeStateOnline
	})

	preBody := rm.FetchMetrics(t)
	preIssued := parseCounter(t, preBody, `nut_master_shutdowns_issued_total\s+([0-9.e+]+)`)
	preExecuted := parseCounter(t, preBody, `nut_master_shutdown_acks_total\{status="executed"\}\s+([0-9.e+]+)`)
	preAccepted := parseCounter(t, preBody, `nut_master_register_attempts_total\{result="accepted"\}\s+([0-9.e+]+)`)

	if preAccepted < 1 {
		t.Fatalf("expected at least one accepted register before shutdown, got %f", preAccepted)
	}

	dry := true
	rm.TriggerShutdown(t, protocol.ShutdownRequest{Reason: "metrics drill", DryRun: &dry})

	waitUntil(t, "shutdown completed", func() bool {
		status := rm.Status(t)
		node := nodeByID(status, "slave-metrics")
		return node != nil && node.LastShutdown != nil && node.LastShutdown.Status == protocol.ShutdownStatusExecuted
	})

	body := rm.FetchMetrics(t)

	wantSubstrings := []string{
		"nut_master_build_info 1",
		`nut_master_nodes{state="online"}`,
		"nut_master_registered_slaves",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\nbody:\n%s", want, body)
		}
	}

	issued := parseCounter(t, body, `nut_master_shutdowns_issued_total\s+([0-9.e+]+)`)
	executed := parseCounter(t, body, `nut_master_shutdown_acks_total\{status="executed"\}\s+([0-9.e+]+)`)
	if issued <= preIssued {
		t.Errorf("shutdowns_issued_total did not advance: pre=%f post=%f", preIssued, issued)
	}
	if executed <= preExecuted {
		t.Errorf("shutdown_acks_total{executed} did not advance: pre=%f post=%f", preExecuted, executed)
	}
}

func parseCounter(t *testing.T, body, pattern string) float64 {
	t.Helper()
	re := regexp.MustCompile(pattern)
	match := re.FindStringSubmatch(body)
	if len(match) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("parse counter %q: %v", match[1], err)
	}
	return v
}
