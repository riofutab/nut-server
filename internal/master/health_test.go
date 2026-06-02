package master

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"nut-server/internal/config"
)

func TestHealthzAlwaysOK(t *testing.T) {
	s := NewServer(config.MasterConfig{})
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz want 200, got %d", rec.Code)
	}
}

func TestReadyzReflectsListenerState(t *testing.T) {
	s := NewServer(config.MasterConfig{})

	// Before Run sets the ready flag the master is not accepting connections.
	rec := httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before ready want 503, got %d", rec.Code)
	}

	s.ready.Store(true)
	rec = httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz when ready want 200, got %d", rec.Code)
	}
}
