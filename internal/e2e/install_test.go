//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"

	"nut-server/internal/protocol"
	"nut-server/internal/version"
)

func setReleaseVersion(t *testing.T, v string) {
	t.Helper()
	orig := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = orig })
}

func TestE2EInstallScriptIssuesNodeTokenAndPreRegisters(t *testing.T) {
	setReleaseVersion(t, "v9.9.9")
	masterCfg := newMasterConfig(t)
	masterCfg.PublicAddr = "nut.test:9999"
	rm := startMaster(t, masterCfg)

	code, script := rm.AdminDo(t, http.MethodPost, "/nodes/installed-01/install-script")

	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, script)
	}
	wants := []string{
		"--role slave",
		"--version 'v9.9.9'",
		"--node-id 'installed-01'",
		"--master-addr 'nut.test:9999'",
		"--token-file",
		"--disable-tls",
	}
	for _, w := range wants {
		if !strings.Contains(script, w) {
			t.Errorf("script missing %q\nscript:\n%s", w, script)
		}
	}
	if strings.Contains(script, rm.enrollmentToken) {
		t.Error("script must not contain the enrollment token")
	}
	node := nodeByID(rm.Status(t), "installed-01")
	if node == nil {
		t.Fatal("install endpoint should pre-register installed-01")
	}
	if node.State != protocol.NodeStateNeverSeen || !node.Enrolled {
		t.Errorf("pre-registered node=%+v want never_seen and enrolled", node)
	}
}

func TestE2EInstallScriptTokenRegistersTheNode(t *testing.T) {
	setReleaseVersion(t, "v9.9.9")
	masterCfg := newMasterConfig(t)
	masterCfg.PublicAddr = "nut.test:9999"
	rm := startMaster(t, masterCfg)
	_, script := rm.AdminDo(t, http.MethodPost, "/nodes/installed-01/install-script")
	token := heredocToken(t, script)

	slaveCfg := newSlaveConfig(t, masterCfg, "installed-01")
	slaveCfg.Token = token
	startSlave(t, slaveCfg)

	waitUntil(t, "installed slave online", func() bool {
		node := nodeByID(rm.Status(t), "installed-01")
		return node != nil && node.Connected && node.Enrolled
	})
	if issued := readSlaveIssuedToken(t, slaveCfg.StateFile); issued != "" {
		t.Fatal("a slave registering with its node token must not be issued another one")
	}
}

func TestE2EInstallScriptRejectsMissingAdminToken(t *testing.T) {
	masterCfg := newMasterConfig(t)
	rm := startMaster(t, masterCfg)

	resp, err := http.Post(rm.adminURL+"/nodes/installed-01/install-script", "", nil)
	if err != nil {
		t.Fatalf("POST install-script: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func heredocToken(t *testing.T, script string) string {
	t.Helper()
	const opener = "<<'NUT_TOKEN_EOF'\n"
	start := strings.Index(script, opener)
	if start < 0 {
		t.Fatalf("no token heredoc in script:\n%s", script)
	}
	rest := script[start+len(opener):]
	end := strings.Index(rest, "\n")
	return rest[:end]
}
