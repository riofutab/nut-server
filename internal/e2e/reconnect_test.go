//go:build e2e

package e2e

import (
	"testing"
	"time"

	"nut-server/internal/protocol"
)

func TestE2ESlaveReconnectsAfterMasterRestart(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm1 := startMaster(t, masterCfg)

	slaveCfg := newSlaveConfig(t, masterCfg, "slave-reconnect")
	startSlave(t, slaveCfg)

	waitUntil(t, "slave online", func() bool {
		status := rm1.Status(t)
		node := nodeByID(status, "slave-reconnect")
		return node != nil && node.State == protocol.NodeStateOnline
	})

	rm1.Stop()

	time.Sleep(150 * time.Millisecond)

	rm2 := startMaster(t, masterCfg)

	waitUntil(t, "slave reconnected to new master", func() bool {
		status := rm2.Status(t)
		node := nodeByID(status, "slave-reconnect")
		return node != nil && node.State == protocol.NodeStateOnline
	})
}
