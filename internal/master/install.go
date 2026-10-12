package master

import (
	"bytes"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
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
	if !isValidInstallMasterAddr(masterAddr) {
		writeJSONError(w, http.StatusBadRequest, "master_addr must be host:port or [ipv6]:port with a host of only alphanumerics, '-', '_', '.'")
		return
	}
	if isLoopbackHost(requestHostWithoutPort(masterAddr)) {
		writeJSONError(w, http.StatusServiceUnavailable, "master_addr resolves to loopback; set public_addr in master.yaml")
		return
	}
	repo := s.cfg.InstallRepo
	if repo == "" {
		repo = defaultInstallRepo
	}
	params := installScriptParams{repo: repo, releaseTag: releaseTag, nodeID: nodeID, masterAddr: masterAddr}
	if s.cfg.TLS.EnabledForServer() {
		caFile := s.cfg.InstallCAFile
		if caFile == "" {
			caFile = s.cfg.TLS.CertFile
		}
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			slog.Error("read CA for install script failed", "path", caFile, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "cannot read the CA for the install script (install_ca_file or tls.cert_file)")
			return
		}
		// The file may be a combined cert+key PEM; only certificates may leave
		// the master.
		params.caPEM = certificatePEM(caPEM)
		if params.caPEM == nil {
			slog.Error("CA for install script has no certificate", "path", caFile)
			writeJSONError(w, http.StatusInternalServerError, "the CA for the install script (install_ca_file or tls.cert_file) contains no certificate")
			return
		}
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

// certificatePEM re-encodes only the CERTIFICATE blocks of data, or returns nil
// when there are none.
func certificatePEM(data []byte) []byte {
	var out bytes.Buffer
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			_ = pem.Encode(&out, &pem.Block{Type: block.Type, Bytes: block.Bytes})
		}
		data = rest
	}
	if out.Len() == 0 {
		return nil
	}
	return out.Bytes()
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
	return len(id) <= maxInstallNodeIDLen && isInstallScriptName(id)
}

// isValidInstallMasterAddr accepts host:port or [ipv6]:port under the same
// rules install-slave.sh applies to --master-addr and --tls-server-name (the
// host is also the TLS server name), so a script the master hands out never
// fails validation after the node's token has been rotated.
func isValidInstallMasterAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.Contains(host, ":") {
		// SplitHostPort only yields a colon in the host for the bracketed form;
		// ParseIP rejects zones, which the script does not allow either.
		if net.ParseIP(host) == nil {
			return false
		}
	} else if !isInstallScriptName(host) {
		return false
	}
	if strings.Trim(port, "0123456789") != "" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isInstallScriptName reports whether s is non-empty and contains only
// alphanumerics, '-', '_' and '.'.
func isInstallScriptName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
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
