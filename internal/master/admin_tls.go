package master

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"

	"nut-server/internal/config"
)

// loadAdminTLSConfig returns nil when admin_tls is not configured. The
// certificate is read once at startup; replacing it needs a restart.
func loadAdminTLSConfig(cfg config.AdminTLSConfig) (*tls.Config, error) {
	if cfg.CertFile == "" && cfg.KeyFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load admin_tls certificate: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}, nil
}

// warnPlaintextAdmin flags an admin API that sends the bearer token in clear
// text over a network interface.
func warnPlaintextAdmin(listenAddr string, tlsConfig *tls.Config) {
	if tlsConfig != nil || isLoopbackListenAddr(listenAddr) {
		return
	}
	slog.Warn("admin API listens on a non-loopback address without admin_tls; admin_token is sent in clear text",
		"admin_listen_addr", listenAddr)
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
