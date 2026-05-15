package master

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nut-server/internal/config"
)

func TestMetricsEndpointExposesExpectedFamilies(t *testing.T) {
	server := NewServer(config.MasterConfig{
		AdminToken: "secret",
		SNMP:       config.SNMPConfig{Target: "10.0.0.1"},
	})

	recordUPSPollResult(true)
	recordUPSPollResult(false)
	recordRegisterAttempt("accepted")
	recordRegisterAttempt("rejected")
	recordRegisterAttempt("invalid")
	recordShutdownIssued()
	recordShutdownAck("executed")
	recordShutdownAck("failed")

	reg := buildMasterRegistry(server)
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	body := rec.Body.String()

	wantSubstrings := []string{
		`nut_master_build_info 1`,
		`nut_master_ups_poll_total{result="success"}`,
		`nut_master_ups_poll_total{result="error"}`,
		`nut_master_register_attempts_total{result="accepted"}`,
		`nut_master_register_attempts_total{result="rejected"}`,
		`nut_master_register_attempts_total{result="invalid"}`,
		`nut_master_shutdowns_issued_total`,
		`nut_master_shutdown_acks_total{status="executed"}`,
		`nut_master_shutdown_acks_total{status="failed"}`,
		`nut_master_registered_slaves`,
		`nut_master_nodes{state="online"}`,
		`nut_master_nodes{state="offline"}`,
		`nut_master_nodes{state="never_seen"}`,
		`nut_master_shutdown_active`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestRecordShutdownAckIgnoresEmptyStatus(t *testing.T) {
	server := NewServer(config.MasterConfig{
		AdminToken: "secret",
		SNMP:       config.SNMPConfig{Target: "10.0.0.1"},
	})
	recordShutdownAck("")

	reg := buildMasterRegistry(server)
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), `nut_master_shutdown_acks_total{status=""}`) {
		t.Fatalf("empty-status ack should be skipped")
	}
}
