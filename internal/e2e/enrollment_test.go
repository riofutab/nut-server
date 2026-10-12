//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"nut-server/internal/config"
)

func readSlaveIssuedToken(t *testing.T, stateFile string) string {
	t.Helper()
	content, err := os.ReadFile(stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read slave state: %v", err)
	}
	var state struct {
		IssuedToken string `json:"issued_token"`
	}
	if err := json.Unmarshal(content, &state); err != nil {
		t.Fatalf("decode slave state: %v", err)
	}
	return state.IssuedToken
}

func TestE2ESlaveEnrollsAndReconnectsWithIssuedToken(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm := startMaster(t, masterCfg)
	slaveCfg := newSlaveConfig(t, masterCfg, "slave-enroll")
	rs := startSlave(t, slaveCfg)

	waitUntil(t, "slave enrolled", func() bool {
		node := nodeByID(rm.Status(t), "slave-enroll")
		return node != nil && node.Connected && node.Enrolled
	})
	issued := readSlaveIssuedToken(t, slaveCfg.StateFile)
	if issued == "" || issued == rm.enrollmentToken {
		t.Fatalf("slave should store an issued token distinct from the enrollment token")
	}

	rs.Stop()
	waitUntil(t, "slave disconnected", func() bool {
		node := nodeByID(rm.Status(t), "slave-enroll")
		return node != nil && !node.Connected
	})
	startSlave(t, slaveCfg)

	waitUntil(t, "slave reconnected", func() bool {
		node := nodeByID(rm.Status(t), "slave-enroll")
		return node != nil && node.Connected
	})
	if got := readSlaveIssuedToken(t, slaveCfg.StateFile); got != issued {
		t.Fatal("reconnecting with the issued token must not issue a new one")
	}
}

func TestE2ERevokedSlaveReEnrollsWhileEnrollmentIsOpen(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm := startMaster(t, masterCfg)
	slaveCfg := newSlaveConfig(t, masterCfg, "slave-revoke")
	startSlave(t, slaveCfg)
	waitUntil(t, "slave enrolled", func() bool {
		node := nodeByID(rm.Status(t), "slave-revoke")
		return node != nil && node.Connected && node.Enrolled
	})
	first := readSlaveIssuedToken(t, slaveCfg.StateFile)

	code, body := rm.AdminDo(t, http.MethodDelete, "/nodes/slave-revoke/token")

	if code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", code, body)
	}
	waitUntil(t, "slave re-enrolled with a new token", func() bool {
		node := nodeByID(rm.Status(t), "slave-revoke")
		issued := readSlaveIssuedToken(t, slaveCfg.StateFile)
		return node != nil && node.Connected && node.Enrolled && issued != "" && issued != first
	})
}

func TestE2EClosedEnrollmentRejectsNewSlave(t *testing.T) {
	masterCfg := newMasterConfig(t)
	masterCfg.Enrollment = config.EnrollmentClosed
	rm := startMaster(t, masterCfg)
	const rejectedPattern = `nut_master_register_attempts_total\{result="rejected"\}\s+([0-9.e+]+)`
	preRejected := parseCounter(t, rm.FetchMetrics(t), rejectedPattern)
	slaveCfg := newSlaveConfig(t, masterCfg, "slave-late")
	startSlave(t, slaveCfg)

	waitUntil(t, "register rejected", func() bool {
		return parseCounter(t, rm.FetchMetrics(t), rejectedPattern) > preRejected
	})

	if node := nodeByID(rm.Status(t), "slave-late"); node != nil {
		t.Fatalf("closed enrollment must not admit a new node, got %+v", node)
	}
}
