package master

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	"nut-server/internal/config"
	"nut-server/internal/security"
)

// nodeTokenBytes is the entropy of an issued per-node token; it is sent to the
// slave hex-encoded (64 characters).
const nodeTokenBytes = 32

var (
	errInvalidToken     = errors.New("invalid token")
	errEnrollmentClosed = errors.New("enrollment closed")
)

func newNodeToken() string {
	buf := make([]byte, nodeTokenBytes)
	rand.Read(buf) // never returns an error since Go 1.24
	return hex.EncodeToString(buf)
}

func hashTokens(tokens []string) []string {
	hashes := make([]string, 0, len(tokens))
	for _, token := range tokens {
		hashes = append(hashes, security.HashToken(token))
	}
	return hashes
}

// authenticateNode checks a register token. A node with an issued token must
// present it. Otherwise, while enrollment is open, an enrollment token admits
// the node and a new token is returned; it only becomes the node's token once
// confirmIssuedToken runs.
func (s *Server) authenticateNode(nodeID, token string) (string, error) {
	s.tokensMu.Lock()
	hash, enrolled := s.nodeTokens[nodeID]
	s.tokensMu.Unlock()
	if enrolled {
		if !security.MatchToken([]string{hash}, token) {
			return "", errInvalidToken
		}
		return "", nil
	}
	if s.cfg.Enrollment == config.EnrollmentClosed {
		return "", errEnrollmentClosed
	}
	if !security.MatchToken(s.enrollmentHashes, token) {
		return "", errInvalidToken
	}
	return newNodeToken(), nil
}

// confirmIssuedToken stores the hash of a token issued at enrollment. It fails
// when the node got a token in the meantime (another enrollment or the install
// script), which must not be overwritten.
func (s *Server) confirmIssuedToken(nodeID, token string) error {
	s.tokensMu.Lock()
	if _, exists := s.nodeTokens[nodeID]; exists {
		s.tokensMu.Unlock()
		return errors.New("node already has an issued token")
	}
	s.nodeTokens[nodeID] = security.HashToken(token)
	s.tokensMu.Unlock()
	s.saveStateForDirectoryChange()
	return nil
}

// issueNodeToken replaces the node's token with a new one and disconnects the
// node, since its connection was authenticated with the old token.
func (s *Server) issueNodeToken(nodeID string) string {
	token := newNodeToken()
	s.tokensMu.Lock()
	s.nodeTokens[nodeID] = security.HashToken(token)
	s.tokensMu.Unlock()
	s.saveStateForDirectoryChange()
	s.disconnectNode(nodeID)
	return token
}

// removeNodeToken deletes the node's token without persisting; it reports
// whether one existed.
func (s *Server) removeNodeToken(nodeID string) bool {
	s.tokensMu.Lock()
	defer s.tokensMu.Unlock()
	if _, ok := s.nodeTokens[nodeID]; !ok {
		return false
	}
	delete(s.nodeTokens, nodeID)
	return true
}

func (s *Server) isEnrolled(nodeID string) bool {
	s.tokensMu.Lock()
	defer s.tokensMu.Unlock()
	_, ok := s.nodeTokens[nodeID]
	return ok
}

// nodeTokensSnapshot copies the token hashes for the state file. It is called
// with commandMu held, so tokensMu must never be held while taking commandMu.
func (s *Server) nodeTokensSnapshot() map[string]string {
	s.tokensMu.Lock()
	defer s.tokensMu.Unlock()
	if len(s.nodeTokens) == 0 {
		return nil
	}
	snapshot := make(map[string]string, len(s.nodeTokens))
	for nodeID, hash := range s.nodeTokens {
		snapshot[nodeID] = hash
	}
	return snapshot
}

func (s *Server) disconnectNode(nodeID string) {
	if client, ok := s.registry.Get(nodeID); ok {
		_ = client.Close()
	}
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if !s.removeNodeToken(nodeID) {
		writeJSONError(w, http.StatusNotFound, "node has no issued token")
		return
	}
	s.saveStateForDirectoryChange()
	s.disconnectNode(nodeID)
	slog.Info("node token revoked", "node_id", nodeID)
	w.WriteHeader(http.StatusNoContent)
}
