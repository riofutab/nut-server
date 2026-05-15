package slave

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestSlaveMetricsEndpointExposesExpectedFamilies(t *testing.T) {
	recordConnectAttempt("success")
	recordConnectAttempt("dial_error")
	recordConnectAttempt("register_error")
	recordShutdownReceived()
	recordShutdownStatus("executing")
	recordShutdownStatus("executed")
	recordShutdownStatus("failed")
	setConnected(true)
	t.Cleanup(func() { setConnected(false) })

	reg := buildSlaveRegistry()
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	body := rec.Body.String()

	wantSubstrings := []string{
		`nut_slave_build_info 1`,
		`nut_slave_connect_attempts_total{result="success"}`,
		`nut_slave_connect_attempts_total{result="dial_error"}`,
		`nut_slave_connect_attempts_total{result="register_error"}`,
		`nut_slave_shutdowns_received_total`,
		`nut_slave_shutdown_status_total{status="executing"}`,
		`nut_slave_shutdown_status_total{status="executed"}`,
		`nut_slave_shutdown_status_total{status="failed"}`,
		`nut_slave_connected 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestSlaveConnectedGaugeFollowsState(t *testing.T) {
	setConnected(false)
	reg := buildSlaveRegistry()
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "nut_slave_connected 0") {
		t.Fatalf("expected connected=0 when no session; body:\n%s", rec.Body.String())
	}

	setConnected(true)
	t.Cleanup(func() { setConnected(false) })

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if !strings.Contains(rec2.Body.String(), "nut_slave_connected 1") {
		t.Fatalf("expected connected=1 after setConnected(true); body:\n%s", rec2.Body.String())
	}
}
