package master

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"nut-server/internal/version"
)

const (
	defaultInstallRepo  = "riofutab/nut-server"
	maxInstallNodeIDLen = 128
)

func (s *Server) handleInstallSlave(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		http.Error(w, "node_id query parameter is required", http.StatusBadRequest)
		return
	}
	if !isValidInstallNodeID(nodeID) {
		http.Error(w, "node_id must contain only alphanumerics, '-', '_', '.'", http.StatusBadRequest)
		return
	}

	if len(s.cfg.AuthTokens) == 0 || strings.TrimSpace(s.cfg.AuthTokens[0]) == "" {
		http.Error(w, "no auth_tokens configured on master", http.StatusServiceUnavailable)
		return
	}
	token := s.cfg.AuthTokens[0]

	masterAddr := resolveMasterAddr(s.cfg.PublicAddr, s.cfg.ListenAddr, r.Host)
	if masterAddr == "" {
		http.Error(w, "cannot determine master_addr; set public_addr in master.yaml", http.StatusServiceUnavailable)
		return
	}

	repo := s.cfg.InstallRepo
	if repo == "" {
		repo = defaultInstallRepo
	}
	releaseTag := version.ReleaseTag()

	s.directory.SetExpected(nodeID, "", nil)
	s.saveStateForDirectoryChange()

	body := renderInstallScript(repo, releaseTag, nodeID, masterAddr, token)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

func renderInstallScript(repo, releaseTag, nodeID, masterAddr, token string) string {
	scriptURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/install-online.sh", repo, releaseTag)
	if releaseTag == "latest" {
		scriptURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/master/scripts/install-online.sh", repo)
	}
	return fmt.Sprintf(
		"#!/usr/bin/env bash\nset -euo pipefail\ncurl -fsSL %s \\\n  | sudo bash -s -- --role slave --version %s -- \\\n      --node-id %s --master-addr %s --token %s\n",
		shellQuote(scriptURL), shellQuote(releaseTag), shellQuote(nodeID), shellQuote(masterAddr), shellQuote(token),
	)
}

func resolveMasterAddr(publicAddr, listenAddr, requestHost string) string {
	if strings.TrimSpace(publicAddr) != "" {
		return strings.TrimSpace(publicAddr)
	}
	host := requestHostWithoutPort(requestHost)
	if host == "" {
		return ""
	}
	port := listenAddrPort(listenAddr)
	if port == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

func requestHostWithoutPort(hostHeader string) string {
	h := strings.TrimSpace(hostHeader)
	if h == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func listenAddrPort(addr string) string {
	if addr == "" {
		return ""
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	if strings.HasPrefix(addr, ":") {
		return strings.TrimPrefix(addr, ":")
	}
	return ""
}

func isValidInstallNodeID(id string) bool {
	if len(id) == 0 || len(id) > maxInstallNodeIDLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
