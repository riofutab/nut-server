package master

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"nut-server/internal/version"
)

const (
	defaultInstallRepo  = "riofutab/nut-server"
	maxInstallNodeIDLen = 128
	// devReleaseTag is what version.ReleaseTag reports for a build without a
	// release version; such a master cannot pin the install scripts.
	devReleaseTag = "latest"
	// Heredoc delimiters in the generated script; the token is hex and the CA
	// base64, so neither can contain them.
	installTokenDelimiter = "NUT_TOKEN_EOF"
	installCADelimiter    = "NUT_CA_EOF"
	installCALineWidth    = 76
)

type installScriptParams struct {
	repo       string
	releaseTag string
	nodeID     string
	masterAddr string
	token      string
	// serverName and caPEM are set when the master listener uses TLS.
	serverName string
	caPEM      []byte
}

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if !isValidInstallNodeID(nodeID) {
		writeJSONError(w, http.StatusBadRequest, "node_id must contain only alphanumerics, '-', '_', '.'")
		return
	}
	releaseTag := version.ReleaseTag()
	if releaseTag == devReleaseTag {
		writeJSONError(w, http.StatusServiceUnavailable, "master is a development build without a release tag; install scripts cannot be pinned to a release")
		return
	}
	masterAddr := resolveMasterAddr(s.cfg.PublicAddr, s.cfg.ListenAddr, r.Host)
	if masterAddr == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "cannot determine master_addr; set public_addr in master.yaml")
		return
	}
	repo := s.cfg.InstallRepo
	if repo == "" {
		repo = defaultInstallRepo
	}
	params := installScriptParams{repo: repo, releaseTag: releaseTag, nodeID: nodeID, masterAddr: masterAddr}
	if s.cfg.TLS.EnabledForServer() {
		caPEM, err := os.ReadFile(s.cfg.TLS.CertFile)
		if err != nil {
			slog.Error("read master certificate for install script failed", "path", s.cfg.TLS.CertFile, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "cannot read master tls.cert_file")
			return
		}
		params.caPEM = caPEM
		params.serverName = requestHostWithoutPort(masterAddr)
	}

	s.directory.SetExpected(nodeID, "", nil)
	params.token = s.issueNodeToken(nodeID)
	slog.Info("issued node token for install script", "node_id", nodeID)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(renderInstallScript(params)))
}

// renderInstallScript builds a bash script that keeps the token and CA off
// every command line (ps would show them): both are written from quoted
// heredocs into a private temp dir that is removed on exit.
func renderInstallScript(p installScriptParams) string {
	// install-online.sh is committed to the repo (and bundled inside the release
	// tarballs) but is never published as a standalone release asset, so fetch
	// it from raw.githubusercontent at the release tag.
	scriptURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/scripts/install-online.sh", p.repo, p.releaseTag)

	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\numask 077\n")
	b.WriteString("work_dir=$(mktemp -d)\ntrap 'rm -rf \"$work_dir\"' EXIT\n")
	fmt.Fprintf(&b, "cat > \"$work_dir/token\" <<'%s'\n%s\n%s\n", installTokenDelimiter, p.token, installTokenDelimiter)
	tlsArgs := " --disable-tls"
	if p.caPEM != nil {
		fmt.Fprintf(&b, "base64 -d > \"$work_dir/ca.crt\" <<'%s'\n%s%s\n", installCADelimiter, wrapBase64(p.caPEM), installCADelimiter)
		tlsArgs = fmt.Sprintf(" --tls-server-name %s --tls-ca-file \"$work_dir/ca.crt\"", shellQuote(p.serverName))
	}
	fmt.Fprintf(&b, "curl -fsSL %s -o \"$work_dir/install-online.sh\"\n", shellQuote(scriptURL))
	fmt.Fprintf(&b, "sudo bash \"$work_dir/install-online.sh\" --role slave --version %s --repo %s -- \\\n", shellQuote(p.releaseTag), shellQuote(p.repo))
	fmt.Fprintf(&b, "  --node-id %s --master-addr %s --token-file \"$work_dir/token\"%s\n", shellQuote(p.nodeID), shellQuote(p.masterAddr), tlsArgs)
	return b.String()
}

func wrapBase64(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(encoded) > installCALineWidth {
		b.WriteString(encoded[:installCALineWidth])
		b.WriteByte('\n')
		encoded = encoded[installCALineWidth:]
	}
	b.WriteString(encoded)
	b.WriteByte('\n')
	return b.String()
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
