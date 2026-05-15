package master

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"nut-server/internal/protocol"
)

func (s *Server) runAdminServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("/status", s.requireAdminToken(s.handleStatus))
	mux.HandleFunc("/commands/shutdown", s.requireAdminToken(s.handleManualShutdown))
	mux.HandleFunc("/commands/reset", s.requireAdminToken(s.handleReset))
	mux.HandleFunc("POST /nodes/expect", s.requireAdminToken(s.handleExpectNode))
	mux.HandleFunc("DELETE /nodes/expect/{node_id}", s.requireAdminToken(s.handleUnsetExpected))
	mux.HandleFunc("DELETE /nodes/{node_id}", s.requireAdminToken(s.handleDeleteNode))
	if err := http.ListenAndServe(s.cfg.AdminListenAddr, mux); err != nil {
		log.Printf("admin server stopped: %v", err)
	}
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
			http.Error(w, "admin token not configured", http.StatusServiceUnavailable)
			return
		}
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		candidate := []byte(strings.TrimPrefix(header, prefix))
		if subtle.ConstantTimeCompare(candidate, expected) != 1 {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.Status())
}

func (s *Server) handleManualShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request protocol.ShutdownRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return
	}
	message, summary, err := s.TriggerShutdown(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message": message,
		"status":  summary,
	})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
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
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	s.directory.UnsetExpected(nodeID)
	s.saveStateForDirectoryChange()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if nodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	switch s.directory.Delete(nodeID) {
	case DeleteOK:
		s.saveStateForDirectoryChange()
		w.WriteHeader(http.StatusNoContent)
	case DeleteNotFound:
		http.Error(w, "node not found", http.StatusNotFound)
	case DeleteRefusedSeen:
		http.Error(w, "node has been seen; cannot delete", http.StatusConflict)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode response failed: %v", err)
	}
}
