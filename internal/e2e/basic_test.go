//go:build e2e

package e2e

import (
	"testing"

	"nut-server/internal/protocol"
)

func TestE2ERegisterAndShutdownClosesLoop(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm := startMaster(t, masterCfg)

	slave1Cfg := newSlaveConfig(t, masterCfg, "slave-alpha")
	slave2Cfg := newSlaveConfig(t, masterCfg, "slave-beta")
	startSlave(t, slave1Cfg)
	startSlave(t, slave2Cfg)

	waitUntil(t, "both slaves online", func() bool {
		status := rm.Status(t)
		alpha := nodeByID(status, "slave-alpha")
		beta := nodeByID(status, "slave-beta")
		return alpha != nil && alpha.State == protocol.NodeStateOnline &&
			beta != nil && beta.State == protocol.NodeStateOnline
	})

	dry := true
	rm.TriggerShutdown(t, protocol.ShutdownRequest{Reason: "e2e drill", DryRun: &dry})

	waitUntil(t, "both slaves executed", func() bool {
		status := rm.Status(t)
		alpha := nodeByID(status, "slave-alpha")
		beta := nodeByID(status, "slave-beta")
		return alpha != nil && alpha.LastShutdown != nil && alpha.LastShutdown.Status == protocol.ShutdownStatusExecuted &&
			beta != nil && beta.LastShutdown != nil && beta.LastShutdown.Status == protocol.ShutdownStatusExecuted
	})

	waitUntil(t, "shutdown_issued cleared", func() bool {
		return !rm.Status(t).ShutdownIssued
	})
}

func TestE2EShutdownTargetsSingleNodeByTag(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm := startMaster(t, masterCfg)

	slave1Cfg := newSlaveConfig(t, masterCfg, "slave-web")
	slave1Cfg.Tags = []string{"web"}
	slave2Cfg := newSlaveConfig(t, masterCfg, "slave-db")
	slave2Cfg.Tags = []string{"db"}
	startSlave(t, slave1Cfg)
	startSlave(t, slave2Cfg)

	waitUntil(t, "both slaves online", func() bool {
		status := rm.Status(t)
		return nodeByID(status, "slave-web") != nil &&
			nodeByID(status, "slave-web").State == protocol.NodeStateOnline &&
			nodeByID(status, "slave-db") != nil &&
			nodeByID(status, "slave-db").State == protocol.NodeStateOnline
	})

	dry := true
	rm.TriggerShutdown(t, protocol.ShutdownRequest{Reason: "tag-targeted", Tags: []string{"web"}, DryRun: &dry})

	waitUntil(t, "web slave executed", func() bool {
		status := rm.Status(t)
		web := nodeByID(status, "slave-web")
		return web != nil && web.LastShutdown != nil && web.LastShutdown.Status == protocol.ShutdownStatusExecuted
	})

	status := rm.Status(t)
	db := nodeByID(status, "slave-db")
	if db != nil && db.LastShutdown != nil {
		t.Fatalf("db slave should NOT have received shutdown: %+v", db.LastShutdown)
	}
}
