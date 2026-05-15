//go:build e2e

package e2e

import (
	"testing"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func TestE2ETLSRegisterAndShutdown(t *testing.T) {
	certFile, keyFile := writeTLSCertPair(t, "127.0.0.1")

	masterCfg := newMasterConfig(t)
	masterCfg.TLS = config.TLSConfig{
		Enabled:  true,
		CertFile: certFile,
		KeyFile:  keyFile,
	}
	rm := startMaster(t, masterCfg)

	slaveCfg := newSlaveConfig(t, masterCfg, "slave-tls")
	slaveCfg.TLS = config.TLSConfig{
		Enabled:            true,
		InsecureSkipVerify: true,
	}
	startSlave(t, slaveCfg)

	waitUntil(t, "tls slave online", func() bool {
		status := rm.Status(t)
		node := nodeByID(status, "slave-tls")
		return node != nil && node.State == protocol.NodeStateOnline
	})

	dry := true
	rm.TriggerShutdown(t, protocol.ShutdownRequest{Reason: "tls drill", DryRun: &dry})

	waitUntil(t, "tls shutdown completed", func() bool {
		status := rm.Status(t)
		node := nodeByID(status, "slave-tls")
		return node != nil && node.LastShutdown != nil && node.LastShutdown.Status == protocol.ShutdownStatusExecuted
	})
}
