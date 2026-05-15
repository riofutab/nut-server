//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"nut-server/internal/protocol"
)

func TestE2EInstallSlaveReturnsScriptAndPreRegisters(t *testing.T) {
	masterCfg := newMasterConfig(t)
	masterCfg.PublicAddr = "nut.test:9999"
	rm := startMaster(t, masterCfg)

	req, err := http.NewRequest(http.MethodGet, rm.adminURL+"/install/slave?node_id=installed-01", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+rm.adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch /install/slave: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, buf)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyStr := string(body)
	wants := []string{
		"--role slave",
		"--node-id 'installed-01'",
		"--master-addr 'nut.test:9999'",
		"--token '" + rm.authToken + "'",
	}
	for _, w := range wants {
		if !strings.Contains(bodyStr, w) {
			t.Errorf("body missing %q\nbody:\n%s", w, bodyStr)
		}
	}

	status := rm.Status(t)
	node := nodeByID(status, "installed-01")
	if node == nil {
		t.Fatal("install endpoint should pre-register installed-01")
	}
	if node.State != protocol.NodeStateNeverSeen {
		t.Errorf("pre-registered node state=%s want %s", node.State, protocol.NodeStateNeverSeen)
	}
}

func TestE2EInstallSlaveRejectsMissingAdminToken(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm := startMaster(t, masterCfg)

	resp, err := http.Get(rm.adminURL + "/install/slave?node_id=installed-01")
	if err != nil {
		t.Fatalf("GET /install/slave: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}
