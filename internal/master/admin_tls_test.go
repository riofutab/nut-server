package master

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"testing"
	"time"

	"nut-server/internal/config"
)

func TestLoadAdminTLSConfigReturnsNilWhenUnset(t *testing.T) {
	tlsConfig, err := loadAdminTLSConfig(config.AdminTLSConfig{})

	if err != nil || tlsConfig != nil {
		t.Fatalf("expected no TLS config and no error, got %v, %v", tlsConfig, err)
	}
}

func TestLoadAdminTLSConfigRequiresTLS12(t *testing.T) {
	_, _, caCert, caKey := generateCertificateAuthority(t)
	certPEM, keyPEM := generateSignedCertificate(t, caCert, caKey, false, []string{"localhost"})
	dir := t.TempDir()

	tlsConfig, err := loadAdminTLSConfig(config.AdminTLSConfig{
		CertFile: writePEMFile(t, dir, "admin.crt", certPEM),
		KeyFile:  writePEMFile(t, dir, "admin.key", keyPEM),
	})

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if tlsConfig.MinVersion != tls.VersionTLS12 || len(tlsConfig.Certificates) != 1 {
		t.Fatalf("unexpected tls config: min=%x certs=%d", tlsConfig.MinVersion, len(tlsConfig.Certificates))
	}
}

func TestLoadAdminTLSConfigFailsOnMissingFile(t *testing.T) {
	_, err := loadAdminTLSConfig(config.AdminTLSConfig{CertFile: "/nonexistent/admin.crt", KeyFile: "/nonexistent/admin.key"})

	if err == nil {
		t.Fatal("expected an error for a missing certificate")
	}
}

func TestIsLoopbackListenAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:9001": true,
		"[::1]:9001":     true,
		"localhost:9001": true,
		":9001":          false,
		"0.0.0.0:9001":   false,
		"10.0.0.5:9001":  false,
		"garbage":        false,
	}
	for addr, want := range cases {
		if got := isLoopbackListenAddr(addr); got != want {
			t.Errorf("isLoopbackListenAddr(%q)=%v want %v", addr, got, want)
		}
	}
}

func TestAdminServerServesHTTPSWithAdminTLS(t *testing.T) {
	caPEM, _, caCert, caKey := generateCertificateAuthority(t)
	certPEM, keyPEM := generateSignedCertificate(t, caCert, caKey, false, []string{"localhost"})
	dir := t.TempDir()
	tlsConfig, err := loadAdminTLSConfig(config.AdminTLSConfig{
		CertFile: writePEMFile(t, dir, "admin.crt", certPEM),
		KeyFile:  writePEMFile(t, dir, "admin.key", keyPEM),
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	addr := freeLocalAddr(t)
	server := NewServer(config.MasterConfig{AdminListenAddr: addr})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); server.runAdminServer(ctx, tlsConfig) }()
	t.Cleanup(func() { cancel(); <-done })
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)

	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp := getWhenListening(t, client, "https://"+addr+"/healthz")
	resp.Body.Close()
	legacy := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MaxVersion: tls.VersionTLS11}}}
	_, legacyErr := legacy.Get("https://" + addr + "/healthz")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 over https, got %d", resp.StatusCode)
	}
	if legacyErr == nil {
		t.Fatal("expected a TLS 1.1 client to be refused")
	}
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func getWhenListening(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
