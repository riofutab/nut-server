//go:build e2e

package e2e

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

// fakeUpsd answers LIST VAR for one UPS on every connection; setStatus changes
// the ups.status it reports.
type fakeUpsd struct {
	mu     sync.Mutex
	status string
	port   int
}

func startFakeUpsd(t *testing.T, ups, status string) *fakeUpsd {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake upsd: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	upsd := &fakeUpsd{status: status, port: listener.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go upsd.serve(conn, ups)
		}
	}()
	return upsd
}

func (u *fakeUpsd) setStatus(status string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = status
}

func (u *fakeUpsd) serve(conn net.Conn, ups string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "LOGOUT" {
			_, _ = conn.Write([]byte("OK Goodbye\n"))
			return
		}
		if line != "LIST VAR "+ups {
			_, _ = conn.Write([]byte("ERR UNKNOWN-UPS\n"))
			continue
		}
		u.mu.Lock()
		status := u.status
		u.mu.Unlock()
		var reply strings.Builder
		fmt.Fprintf(&reply, "BEGIN LIST VAR %s\n", ups)
		fmt.Fprintf(&reply, "VAR %s ups.status %q\n", ups, status)
		fmt.Fprintf(&reply, "END LIST VAR %s\n", ups)
		_, _ = conn.Write([]byte(reply.String()))
	}
}

func TestE2ENUTLowBatteryShutsDownSlave(t *testing.T) {
	upsd := startFakeUpsd(t, "myups", "OL")
	masterCfg := newMasterConfig(t)
	masterCfg.SNMP = config.SNMPConfig{}
	masterCfg.NUT = config.NUTConfig{Host: "127.0.0.1", Port: uint16(upsd.port), UPS: "myups", TimeoutSeconds: 1}
	masterCfg.ShutdownPolicies = []config.ShutdownPolicySpec{
		{Name: "lb", When: config.ShutdownPolicyWhen{LowBattery: true}, Target: config.ShutdownPolicyTarget{All: true}, Reason: "low battery"},
	}
	rm := startMaster(t, masterCfg)
	startSlave(t, newSlaveConfig(t, masterCfg, "slave-usb"))
	waitUntil(t, "slave online and UPS read over NUT", func() bool {
		status := rm.Status(t)
		node := nodeByID(status, "slave-usb")
		return node != nil && node.State == protocol.NodeStateOnline &&
			status.UPS != nil && status.UPS.Source == protocol.UPSSourceNUT && status.UPS.OnBattery != nil
	})

	upsd.setStatus("OB DISCHRG")
	waitUntil(t, "episode started on battery", func() bool {
		return rm.Status(t).Episode != nil
	})
	if node := nodeByID(rm.Status(t), "slave-usb"); node.LastShutdown != nil {
		t.Fatalf("slave shut down before LB: %+v", node.LastShutdown)
	}

	upsd.setStatus("OB DISCHRG LB")
	waitUntil(t, "slave simulated shutdown on LB", func() bool {
		node := nodeByID(rm.Status(t), "slave-usb")
		return node != nil && node.LastShutdown != nil && node.LastShutdown.Status == protocol.ShutdownStatusSimulated
	})
	if ups := rm.Status(t).UPS; ups.Target != fmt.Sprintf("myups@127.0.0.1:%d", upsd.port) || ups.LowBattery == nil || !*ups.LowBattery {
		t.Fatalf("unexpected UPS view %+v", ups)
	}
}
