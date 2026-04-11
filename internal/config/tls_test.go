package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMasterConfigWithTLS(t *testing.T) {
	dir := t.TempDir()
	certFile := writeTestFile(t, dir, "master.crt", "server-cert")
	keyFile := writeTestFile(t, dir, "master.key", "server-key")
	caFile := writeTestFile(t, dir, "ca.crt", "ca-cert")
	configFile := filepath.Join(dir, "master.yaml")

	content := strings.Join([]string{
		"listen_addr: \"127.0.0.1:9000\"",
		"auth_tokens:",
		"  - \"token-1\"",
		"tls:",
		"  enabled: true",
		"  cert_file: '" + filepath.ToSlash(certFile) + "'",
		"  key_file: '" + filepath.ToSlash(keyFile) + "'",
		"  ca_file: '" + filepath.ToSlash(caFile) + "'",
		"  require_client_cert: true",
		"snmp:",
		"  target: \"192.168.1.10\"",
	}, "\n") + "\n"
	if err := os.WriteFile(configFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadMasterConfig(configFile)
	if err != nil {
		t.Fatalf("load master config: %v", err)
	}
	if !cfg.TLS.Enabled {
		t.Fatalf("expected tls enabled")
	}
	if cfg.TLS.CertFile != filepath.ToSlash(certFile) {
		t.Fatalf("expected cert file %s, got %s", filepath.ToSlash(certFile), cfg.TLS.CertFile)
	}
	if cfg.TLS.KeyFile != filepath.ToSlash(keyFile) {
		t.Fatalf("expected key file %s, got %s", filepath.ToSlash(keyFile), cfg.TLS.KeyFile)
	}
	if cfg.TLS.CAFile != filepath.ToSlash(caFile) {
		t.Fatalf("expected CA file %s, got %s", filepath.ToSlash(caFile), cfg.TLS.CAFile)
	}
	if !cfg.TLS.RequireClientCert {
		t.Fatalf("expected require_client_cert true")
	}
}

func TestLoadMasterConfigRejectsIncompleteTLS(t *testing.T) {
	dir := t.TempDir()
	certFile := writeTestFile(t, dir, "master.crt", "server-cert")
	configFile := filepath.Join(dir, "master.yaml")

	content := strings.Join([]string{
		"auth_tokens:",
		"  - \"token-1\"",
		"tls:",
		"  enabled: true",
		"  cert_file: '" + filepath.ToSlash(certFile) + "'",
		"snmp:",
		"  target: \"192.168.1.10\"",
	}, "\n") + "\n"
	if err := os.WriteFile(configFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadMasterConfig(configFile); err == nil {
		t.Fatalf("expected master TLS validation error")
	}
}

func TestLoadSlaveConfigWithTLS(t *testing.T) {
	dir := t.TempDir()
	certFile := writeTestFile(t, dir, "slave.crt", "client-cert")
	keyFile := writeTestFile(t, dir, "slave.key", "client-key")
	caFile := writeTestFile(t, dir, "ca.crt", "ca-cert")
	configFile := filepath.Join(dir, "slave.yaml")

	content := strings.Join([]string{
		"node_id: \"slave-01\"",
		"master_addr: \"127.0.0.1:9000\"",
		"token: \"token-1\"",
		"tls:",
		"  enabled: true",
		"  cert_file: '" + filepath.ToSlash(certFile) + "'",
		"  key_file: '" + filepath.ToSlash(keyFile) + "'",
		"  ca_file: '" + filepath.ToSlash(caFile) + "'",
		"  server_name: \"localhost\"",
	}, "\n") + "\n"
	if err := os.WriteFile(configFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadSlaveConfig(configFile)
	if err != nil {
		t.Fatalf("load slave config: %v", err)
	}
	if !cfg.TLS.Enabled {
		t.Fatalf("expected tls enabled")
	}
	if cfg.TLS.ServerName != "localhost" {
		t.Fatalf("expected server name localhost, got %s", cfg.TLS.ServerName)
	}
	if cfg.TLS.CAFile != filepath.ToSlash(caFile) {
		t.Fatalf("expected CA file %s, got %s", filepath.ToSlash(caFile), cfg.TLS.CAFile)
	}
}

func TestLoadSlaveConfigRejectsConflictingTLSVerification(t *testing.T) {
	dir := t.TempDir()
	caFile := writeTestFile(t, dir, "ca.crt", "ca-cert")
	configFile := filepath.Join(dir, "slave.yaml")

	content := strings.Join([]string{
		"node_id: \"slave-01\"",
		"master_addr: \"127.0.0.1:9000\"",
		"token: \"token-1\"",
		"tls:",
		"  enabled: true",
		"  ca_file: '" + filepath.ToSlash(caFile) + "'",
		"  insecure_skip_verify: true",
	}, "\n") + "\n"
	if err := os.WriteFile(configFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadSlaveConfig(configFile); err == nil {
		t.Fatalf("expected slave TLS validation error")
	}
}

func TestTLSConfigValidateServer(t *testing.T) {
	tests := []struct {
		name    string
		cfg     TLSConfig
		wantErr string
	}{
		{
			name:    "missing cert",
			cfg:     TLSConfig{Enabled: true, KeyFile: "server.key"},
			wantErr: "tls.cert_file must not be empty",
		},
		{
			name:    "missing key",
			cfg:     TLSConfig{Enabled: true, CertFile: "server.crt"},
			wantErr: "tls.key_file must not be empty",
		},
		{
			name:    "mtls without ca",
			cfg:     TLSConfig{Enabled: true, CertFile: "server.crt", KeyFile: "server.key", RequireClientCert: true},
			wantErr: "tls.ca_file must not be empty",
		},
		{
			name: "valid server tls",
			cfg:  TLSConfig{Enabled: true, CertFile: "server.crt", KeyFile: "server.key", CAFile: "ca.crt", RequireClientCert: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateServer()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateServer() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateServer() error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestTLSConfigValidateClient(t *testing.T) {
	tests := []struct {
		name    string
		cfg     TLSConfig
		wantErr string
	}{
		{
			name:    "missing key",
			cfg:     TLSConfig{Enabled: true, CertFile: "client.crt"},
			wantErr: "tls.key_file must not be empty",
		},
		{
			name:    "missing cert",
			cfg:     TLSConfig{Enabled: true, KeyFile: "client.key"},
			wantErr: "tls.cert_file must not be empty",
		},
		{
			name:    "conflicting verification options",
			cfg:     TLSConfig{Enabled: true, CAFile: "ca.crt", InsecureSkipVerify: true},
			wantErr: "tls.ca_file cannot be combined",
		},
		{
			name: "valid client tls",
			cfg:  TLSConfig{Enabled: true, CertFile: "client.crt", KeyFile: "client.key", CAFile: "ca.crt", ServerName: "master.local"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateClient()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateClient() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateClient() error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestServerTLSConfigBuildsMTLSConfig(t *testing.T) {
	dir := t.TempDir()
	caFile, certFile, keyFile := writeTLSFiles(t, dir, "server.local")

	cfg, err := TLSConfig{
		Enabled:           true,
		CertFile:          certFile,
		KeyFile:           keyFile,
		CAFile:            caFile,
		RequireClientCert: true,
	}.ServerTLSConfig()
	if err != nil {
		t.Fatalf("ServerTLSConfig() error = %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected tls config")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("expected RequireAndVerifyClientCert, got %v", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Fatalf("expected client CAs to be set")
	}
}

func TestClientTLSConfigBuildsVerificationSettings(t *testing.T) {
	dir := t.TempDir()
	caFile, certFile, keyFile := writeTLSFiles(t, dir, "master.local")

	cfg, err := TLSConfig{
		Enabled:    true,
		CertFile:   certFile,
		KeyFile:    keyFile,
		CAFile:     caFile,
		ServerName: "master.local",
	}.ClientTLSConfig()
	if err != nil {
		t.Fatalf("ClientTLSConfig() error = %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected tls config")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil {
		t.Fatalf("expected root CAs to be set")
	}
	if cfg.ServerName != "master.local" {
		t.Fatalf("expected server name master.local, got %s", cfg.ServerName)
	}
	if cfg.InsecureSkipVerify {
		t.Fatalf("expected certificate verification to remain enabled")
	}
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file %s: %v", path, err)
	}
	return path
}

func writeTLSFiles(t *testing.T, dir, commonName string) (caFile, certFile, keyFile string) {
	t.Helper()

	caCertPEM, caKey := generateCertificateAuthority(t)
	leafCertPEM, leafKeyPEM := generateSignedCertificate(t, caCertPEM, caKey, commonName)

	caFile = writeTestFile(t, dir, "ca.pem", string(caCertPEM))
	certFile = writeTestFile(t, dir, "leaf.pem", string(leafCertPEM))
	keyFile = writeTestFile(t, dir, "leaf.key", string(leafKeyPEM))
	return caFile, certFile, keyFile
}

func generateCertificateAuthority(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key
}

func generateSignedCertificate(t *testing.T, caCertPEM []byte, caKey *rsa.PrivateKey, commonName string) ([]byte, []byte) {
	t.Helper()

	block, _ := pem.Decode(caCertPEM)
	if block == nil {
		t.Fatalf("decode CA pem")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: commonName},
		DNSNames:              []string{commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	return certPEM, keyPEM
}
