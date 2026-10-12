package slave

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func clearShutdownReady() {
	slaveShutdownReady.Store(shutdownReadyUnchecked)
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func findLogRecord(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record["msg"] == msg {
			return record
		}
	}
	t.Fatalf("log record %q not found in:\n%s", msg, buf.String())
	return nil
}

func scrapeSlaveMetrics(t *testing.T) string {
	t.Helper()
	handler := promhttp.HandlerFor(buildSlaveRegistry(), promhttp.HandlerOpts{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func newSelfCheckClient(cfg config.SlaveConfig, result error, calls *atomic.Int32) *Client {
	client := NewClient(cfg)
	client.checkShutdown = func(_ context.Context, _ []string) error {
		calls.Add(1)
		return result
	}
	return client
}

func TestShutdownSelfCheckPassLogsInfoAndReportsReady(t *testing.T) {
	logs := captureLogs(t)
	t.Cleanup(clearShutdownReady)
	var calls atomic.Int32
	client := newSelfCheckClient(config.SlaveConfig{NodeID: "node-1", ShutdownCommand: []string{"/sbin/shutdown", "-h", "now"}}, nil, &calls)

	client.runShutdownSelfCheck(context.Background())

	if calls.Load() != 1 {
		t.Fatalf("expected check to run once, got %d", calls.Load())
	}
	record := findLogRecord(t, logs, "shutdown command self-check passed")
	if record["level"] != "INFO" {
		t.Fatalf("expected INFO level, got %v", record["level"])
	}
	if client.shutdownReady == nil || !*client.shutdownReady {
		t.Fatalf("expected shutdownReady=true, got %v", client.shutdownReady)
	}
	if body := scrapeSlaveMetrics(t); !strings.Contains(body, "nut_slave_shutdown_ready 1") {
		t.Fatalf("expected nut_slave_shutdown_ready 1; body:\n%s", body)
	}
}

func TestShutdownSelfCheckFailureLogsErrorAndReportsNotReady(t *testing.T) {
	logs := captureLogs(t)
	t.Cleanup(clearShutdownReady)
	var calls atomic.Int32
	client := newSelfCheckClient(config.SlaveConfig{NodeID: "node-1", ShutdownCommand: []string{"/usr/bin/sudo", "/sbin/shutdown"}}, errors.New("sudo denied"), &calls)

	client.runShutdownSelfCheck(context.Background())

	record := findLogRecord(t, logs, "shutdown command self-check failed")
	if record["level"] != "ERROR" {
		t.Fatalf("expected ERROR level, got %v", record["level"])
	}
	if record["err"] != "sudo denied" {
		t.Fatalf("expected err attribute, got %v", record["err"])
	}
	if record["command"] == nil {
		t.Fatalf("expected command attribute in %v", record)
	}
	if client.shutdownReady == nil || *client.shutdownReady {
		t.Fatalf("expected shutdownReady=false, got %v", client.shutdownReady)
	}
	if body := scrapeSlaveMetrics(t); !strings.Contains(body, "nut_slave_shutdown_ready 0") {
		t.Fatalf("expected nut_slave_shutdown_ready 0; body:\n%s", body)
	}
}

func TestShutdownSelfCheckSkippedInDryRun(t *testing.T) {
	clearShutdownReady()
	var calls atomic.Int32
	client := newSelfCheckClient(config.SlaveConfig{NodeID: "node-1", DryRun: true}, nil, &calls)

	client.runShutdownSelfCheck(context.Background())

	if calls.Load() != 0 {
		t.Fatalf("expected check to be skipped in dry-run, got %d calls", calls.Load())
	}
	if client.shutdownReady != nil {
		t.Fatalf("expected shutdownReady=nil in dry-run, got %v", *client.shutdownReady)
	}
	if body := scrapeSlaveMetrics(t); strings.Contains(body, "nut_slave_shutdown_ready") {
		t.Fatalf("expected no shutdown_ready gauge when check skipped; body:\n%s", body)
	}
}

func TestRunRegistersWithDryRunAndShutdownReady(t *testing.T) {
	notReady := false
	cases := []struct {
		name        string
		dryRun      bool
		checkErr    error
		wantReady   *bool
		wantChecked int
	}{
		{name: "failed check", checkErr: errors.New("denied"), wantReady: &notReady, wantChecked: 1},
		{name: "dry-run skips check", dryRun: true, wantChecked: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			t.Cleanup(clearShutdownReady)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer listener.Close()
			var calls atomic.Int32
			client := newSelfCheckClient(config.SlaveConfig{
				MasterAddr:        listener.Addr().String(),
				NodeID:            "node-1",
				Token:             "t",
				DryRun:            tc.dryRun,
				ReconnectInterval: config.Duration{Duration: time.Second},
			}, tc.checkErr, &calls)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- client.Run(ctx) }()
			defer func() {
				cancel()
				<-done
			}()

			register := readRegisterMessage(t, listener)

			if int(calls.Load()) != tc.wantChecked {
				t.Fatalf("check calls=%d want %d", calls.Load(), tc.wantChecked)
			}
			if register.DryRun != tc.dryRun {
				t.Fatalf("DryRun=%v want %v", register.DryRun, tc.dryRun)
			}
			if (register.ShutdownReady == nil) != (tc.wantReady == nil) ||
				(register.ShutdownReady != nil && *register.ShutdownReady != *tc.wantReady) {
				t.Fatalf("ShutdownReady=%v want %v", register.ShutdownReady, tc.wantReady)
			}
		})
	}
}

func readRegisterMessage(t *testing.T, listener net.Listener) protocol.RegisterMessage {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	// Closing right away makes the client's register-ack read fail, so Run
	// returns promptly once the test cancels its context.
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	env, err := protocol.ReadEnvelope(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("read register: %v", err)
	}
	if env.Type != protocol.TypeRegister {
		t.Fatalf("expected register envelope, got %s", env.Type)
	}
	var register protocol.RegisterMessage
	if err := protocol.DecodePayload(env.Data, &register); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	return register
}
