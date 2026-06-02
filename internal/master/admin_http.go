package master

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nut-server/internal/protocol"
)

func (s *Server) runAdminServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(masterPromRegistry(s), promhttp.HandlerOpts{}))
	mux.HandleFunc("/status", s.requireAdminToken(s.handleStatus))
	mux.HandleFunc("/commands/shutdown", s.requireAdminToken(s.handleManualShutdown))
	mux.HandleFunc("/commands/reset", s.requireAdminToken(s.handleReset))
	mux.HandleFunc("POST /nodes/expect", s.requireAdminToken(s.handleExpectNode))
	mux.HandleFunc("DELETE /nodes/expect/{node_id}", s.requireAdminToken(s.handleUnsetExpected))
	mux.HandleFunc("DELETE /nodes/{node_id}", s.requireAdminToken(s.handleDeleteNode))
	mux.HandleFunc("GET /install/slave", s.requireAdminToken(s.handleInstallSlave))

	srv := &http.Server{Addr: s.cfg.AdminListenAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("admin server stopped", "err", err)
	}
}

// handleHealthz is an unauthenticated liveness probe: a 200 means the admin
// process is up and serving. It deliberately checks nothing else so a transient
// dependency failure never makes an orchestrator kill a healthy process.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writePlain(w, http.StatusOK, "ok")
}

// handleReadyz is an unauthenticated readiness probe: 200 once the master TCP
// listener is accepting slave connections, 503 before startup or during
// shutdown.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if s.ready.Load() {
		writePlain(w, http.StatusOK, "ready")
		return
	}
	writePlain(w, http.StatusServiceUnavailable, "not ready")
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) requireAdminToken(next http.HandlerFunc) http.HandlerFunc {
	expected := []byte(s.cfg.AdminToken)
	return func(w http.ResponseWriter, r *http.Request) {
		if len(expected) == 0 {
			writeJSONError(w, http.StatusServiceUnavailable, "admin token not configured")
			return
		}
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			writeJSONError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		candidate := []byte(strings.TrimPrefix(header, prefix))
		if subtle.ConstantTimeCompare(candidate, expected) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.Status())
}

func (s *Server) handleManualShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request protocol.ShutdownRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("decode request: %v", err))
		return
	}
	message, summary, err := s.TriggerShutdown(request)
	if err != nil && message.CommandID == "" {
		// Rejected before any command was created (no matching targets, or a
		// shutdown is already active).
		status := http.StatusBadRequest
		if errors.Is(err, errShutdownAlreadyActive) {
			status = http.StatusConflict
		}
		writeJSONError(w, status, err.Error())
		return
	}
	response := map[string]interface{}{
		"message": message,
		"status":  summary,
	}
	if err != nil {
		// The command was created, persisted and delivered to every reachable
		// slave, but at least one delivery failed. Report partial success so the
		// operator does not mistake an issued shutdown for a rejected one.
		response["delivery_error"] = err.Error()
		writeJSON(w, http.StatusMultiStatus, response)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.ResetActiveCommand()
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset complete"})
}

type expectNodeRequest struct {
	NodeID   string   `json:"node_id"`
	Hostname string   `json:"hostname,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

func (s *Server) handleExpectNode(w http.ResponseWriter, r *http.Request) {
	var req expectNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	if req.NodeID == "" {
		writeJSONError(w, http.StatusBadRequest, "node_id is required")
		return
	}
	s.directory.SetExpected(req.NodeID, req.Hostname, req.Tags)
	s.saveStateForDirectoryChange()
	meta, _ := s.directory.Get(req.NodeID)
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) handleUnsetExpected(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if nodeID == "" {
		writeJSONError(w, http.StatusBadRequest, "node_id is required")
		return
	}
	s.directory.UnsetExpected(nodeID)
	s.saveStateForDirectoryChange()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if nodeID == "" {
		writeJSONError(w, http.StatusBadRequest, "node_id is required")
		return
	}
	switch s.directory.Delete(nodeID) {
	case DeleteOK:
		s.saveStateForDirectoryChange()
		w.WriteHeader(http.StatusNoContent)
	case DeleteNotFound:
		writeJSONError(w, http.StatusNotFound, "node not found")
	case DeleteRefusedSeen:
		writeJSONError(w, http.StatusConflict, "node has been seen; cannot delete")
	}
}

// writeJSONError emits errors in the same JSON envelope as success responses so
// admin API clients never have to parse two response shapes.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("encode response failed", "err", err)
	}
}
