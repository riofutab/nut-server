package master

import (
	"crypto/tls"
	"log/slog"
	"net"
)

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
