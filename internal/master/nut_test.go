package master

import (
	"bufio"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func formatUPSStatus(status UPSStatus) string {
	return fmt.Sprintf("{OnBattery:%t LowBattery:%t BatteryCharge:%v RuntimeMinutes:%v}",
		status.OnBattery, status.LowBattery, optionalInt(status.BatteryCharge), optionalInt(status.RuntimeMinutes))
}

func TestParseNUTVar(t *testing.T) {
	cases := []struct {
		name      string
		rest      string
		wantName  string
		wantValue string
		wantErr   bool
	}{
		{name: "plain", rest: `battery.charge "87"`, wantName: "battery.charge", wantValue: "87"},
		{name: "status with spaces", rest: `ups.status "OB DISCHRG LB"`, wantName: "ups.status", wantValue: "OB DISCHRG LB"},
		{name: "escaped quote and backslash", rest: `device.model "a \"b\" c\\d"`, wantName: "device.model", wantValue: `a "b" c\d`},
		{name: "empty value", rest: `ups.firmware ""`, wantName: "ups.firmware", wantValue: ""},
		{name: "unquoted", rest: `battery.charge 87`, wantErr: true},
		{name: "missing value", rest: `battery.charge`, wantErr: true},
		{name: "unescaped quote", rest: `device.model "a"b"`, wantErr: true},
		{name: "dangling escape", rest: `device.model "a\"`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, value, err := parseNUTVar(tc.rest)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q=%q", name, value)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name != tc.wantName || value != tc.wantValue {
				t.Fatalf("got %q=%q want %q=%q", name, value, tc.wantName, tc.wantValue)
			}
		})
	}
}

func TestDecodeNUTVars(t *testing.T) {
	cases := []struct {
		name    string
		vars    map[string]string
		want    UPSStatus
		wantErr string
	}{
		{
			name: "on battery with charge and runtime",
			vars: map[string]string{"ups.status": "OB DISCHRG", "battery.charge": "87", "battery.runtime": "1500"},
			want: UPSStatus{OnBattery: true, BatteryCharge: new(87), RuntimeMinutes: new(25)},
		},
		{
			name: "online",
			vars: map[string]string{"ups.status": "OL CHRG", "battery.charge": "100", "battery.runtime": "3600"},
			want: UPSStatus{BatteryCharge: new(100), RuntimeMinutes: new(60)},
		},
		{
			name: "low battery without charge or runtime",
			vars: map[string]string{"ups.status": "OB LB"},
			want: UPSStatus{OnBattery: true, LowBattery: true},
		},
		{
			name: "decimals round down",
			vars: map[string]string{"ups.status": "OB", "battery.charge": "29.9", "battery.runtime": "299.5"},
			want: UPSStatus{OnBattery: true, BatteryCharge: new(29), RuntimeMinutes: new(4)},
		},
		{name: "status missing", vars: map[string]string{"battery.charge": "50"}, wantErr: "did not report ups.status"},
		{name: "neither OL nor OB", vars: map[string]string{"ups.status": "WAIT"}, wantErr: "neither OL nor OB"},
		{name: "charge not a number", vars: map[string]string{"ups.status": "OB", "battery.charge": "n/a"}, wantErr: "battery.charge"},
		{name: "negative runtime", vars: map[string]string{"ups.status": "OB", "battery.runtime": "-60"}, wantErr: "negative"},
		{name: "NaN charge", vars: map[string]string{"ups.status": "OB", "battery.charge": "NaN"}, wantErr: "not a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := decodeNUTVars(tc.vars)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(status, tc.want) {
				t.Fatalf("status=%s want %s", formatUPSStatus(status), formatUPSStatus(tc.want))
			}
		})
	}
}

func TestReadNUTVarListRejectsBadReplies(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		wantErr string
	}{
		{name: "unknown ups", reply: "ERR UNKNOWN-UPS\n", wantErr: `does not serve a UPS named "myups"`},
		{name: "stale data", reply: "ERR DATA-STALE\n", wantErr: "DATA-STALE"},
		{name: "driver down", reply: "ERR DRIVER-NOT-CONNECTED\n", wantErr: "DRIVER-NOT-CONNECTED"},
		{name: "access denied", reply: "ERR ACCESS-DENIED\n", wantErr: "ACCESS-DENIED"},
		{name: "other ups in header", reply: "BEGIN LIST VAR other\n", wantErr: "unexpected reply"},
		{name: "foreign line", reply: "BEGIN LIST VAR myups\nVAR other battery.charge \"1\"\n", wantErr: "unexpected line"},
		{name: "truncated", reply: "BEGIN LIST VAR myups\nVAR myups ups.status \"OB\"\n", wantErr: "read reply"},
		{name: "line too long", reply: "BEGIN LIST VAR myups\nVAR myups x \"" + strings.Repeat("a", maxNUTLineBytes) + "\"\n", wantErr: "longer than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReaderSize(strings.NewReader(tc.reply), maxNUTLineBytes)

			_, err := readNUTVarList(reader, "myups")

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestReadNUTVarListCapsVariableCount(t *testing.T) {
	var reply strings.Builder
	reply.WriteString("BEGIN LIST VAR myups\n")
	for i := 0; i <= maxNUTVars; i++ {
		fmt.Fprintf(&reply, "VAR myups v%d \"1\"\n", i)
	}
	reply.WriteString("END LIST VAR myups\n")

	_, err := readNUTVarList(bufio.NewReader(strings.NewReader(reply.String())), "myups")

	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("expected variable cap error, got %v", err)
	}
}

// fakeUpsd serves one connection: it records the request lines and answers
// LIST VAR with reply.
func fakeUpsd(t *testing.T, reply string) (config.NUTConfig, <-chan []string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requests := make(chan []string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		var lines []string
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			if strings.HasPrefix(scanner.Text(), "LIST VAR ") {
				_, _ = conn.Write([]byte(reply))
			}
			if scanner.Text() == "LOGOUT" {
				_, _ = conn.Write([]byte("OK Goodbye\n"))
				break
			}
		}
		requests <- lines
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return config.NUTConfig{Host: "127.0.0.1", Port: uint16(addr.Port), UPS: "myups", TimeoutSeconds: 2}, requests
}

func TestReadNUTStatusListsVariablesAndLogsOut(t *testing.T) {
	cfg, requests := fakeUpsd(t, "BEGIN LIST VAR myups\n"+
		"VAR myups battery.charge \"42\"\n"+
		"VAR myups battery.runtime \"600\"\n"+
		"VAR myups device.model \"Back-UPS \\\"ES\\\"\"\n"+
		"VAR myups ups.status \"OB DISCHRG\"\n"+
		"END LIST VAR myups\n")

	status, err := ReadNUTStatus(cfg)

	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := (UPSStatus{OnBattery: true, BatteryCharge: new(42), RuntimeMinutes: new(10)}); !reflect.DeepEqual(status, want) {
		t.Fatalf("status=%s want %s", formatUPSStatus(status), formatUPSStatus(want))
	}
	select {
	case lines := <-requests:
		if !reflect.DeepEqual(lines, []string{"LIST VAR myups", "LOGOUT"}) {
			t.Fatalf("requests=%q want LIST VAR then LOGOUT", lines)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake upsd saw no requests")
	}
}

func TestReadNUTStatusReportsUnreachableUpsd(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	_, err = ReadNUTStatus(config.NUTConfig{Host: "127.0.0.1", Port: uint16(port), UPS: "myups", TimeoutSeconds: 1})

	if err == nil || !strings.Contains(err.Error(), "connect upsd") {
		t.Fatalf("expected connect error, got %v", err)
	}
}

func TestReadingWithoutRuntimeNeverFiresRuntimePolicy(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "low-runtime", When: config.ShutdownPolicyWhen{RuntimeBelow: 10}, Target: config.ShutdownPolicyTarget{All: true}},
			{Name: "low-charge", When: config.ShutdownPolicyWhen{ChargeBelow: 30}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "web-1")
	ups.set(UPSStatus{OnBattery: true})

	if err := server.evaluateUPS(time.Now().UTC()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	slave.expectNothing(t)
	if status := server.Status(); status.Episode == nil || len(status.Episode.FiredPolicies) != 0 {
		t.Fatalf("expected an episode with no fired policy, got %+v", status.Episode)
	}
}

func TestLowBatteryPolicyFiresOnLBFlag(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		NUT: config.NUTConfig{Host: "127.0.0.1", Port: 3493, UPS: "myups", TimeoutSeconds: 2},
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "lb", When: config.ShutdownPolicyWhen{LowBattery: true}, Target: config.ShutdownPolicyTarget{All: true}},
		},
	})
	slave := newFakeSlave(t, server, "web-1")
	ups.set(UPSStatus{OnBattery: true})
	if err := server.evaluateUPS(time.Now().UTC()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	slave.expectNothing(t)

	ups.set(UPSStatus{OnBattery: true, LowBattery: true})
	if err := server.evaluateUPS(time.Now().UTC()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if message := slave.expectShutdown(t); !message.Target.All {
		t.Fatalf("expected an all-node shutdown, got %+v", message.Target)
	}
}

func TestLowBatteryPowersOffMasterAsEmergency(t *testing.T) {
	server, ups := newEpisodeServer(config.MasterConfig{
		NUT:           config.NUTConfig{Host: "127.0.0.1", Port: 3493, UPS: "myups", TimeoutSeconds: 2},
		LocalShutdown: enabledLocalShutdown(),
		ShutdownPolicies: []config.ShutdownPolicySpec{
			{Name: "never", When: config.ShutdownPolicyWhen{ChargeBelow: 1}, Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}}},
		},
	})
	calls := &localShutdownCalls{}
	server.localShutdownRunner = calls.run
	ups.set(UPSStatus{OnBattery: true, LowBattery: true})
	if err := server.evaluateUPS(time.Now().UTC()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	server.evaluateLocalShutdown(time.Now().UTC(), server.latestSuccessfulUPSStatus())

	got := calls.snapshot()
	if len(got) != 1 || got[0].trigger != protocol.LocalShutdownTriggerLowBattery {
		t.Fatalf("expected low_battery local shutdown, got %+v", got)
	}
}

func TestLowBatteryOnLinePowerIsNotAnEmergency(t *testing.T) {
	server, _ := newEpisodeServer(config.MasterConfig{LocalShutdown: enabledLocalShutdown()})

	trigger := server.emergencyTrigger(&UPSStatus{LowBattery: true}, time.Now().UTC())

	if trigger != "" {
		t.Fatalf("expected no emergency on line power, got %q", trigger)
	}
}

func TestStatusReportsNUTSourceAndLowBattery(t *testing.T) {
	server := NewServer(config.MasterConfig{NUT: config.NUTConfig{Host: "10.0.0.5", Port: 3493, UPS: "myups"}})

	server.recordUPSSuccess(UPSStatus{OnBattery: true, LowBattery: true}, time.Now().UTC())

	ups := server.Status().UPS
	if ups == nil || ups.Source != protocol.UPSSourceNUT || ups.Target != "myups@10.0.0.5:3493" {
		t.Fatalf("expected nut source and target, got %+v", ups)
	}
	if ups.LowBattery == nil || !*ups.LowBattery {
		t.Fatalf("expected low_battery true, got %v", ups.LowBattery)
	}
	if ups.BatteryCharge != nil || ups.RuntimeMinutes != nil {
		t.Fatalf("expected missing charge/runtime to stay absent, got charge=%v runtime=%v", ups.BatteryCharge, ups.RuntimeMinutes)
	}
}
