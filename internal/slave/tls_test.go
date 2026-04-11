package slave

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nut-server/internal/config"
)

func TestClientDialUsesTLSWhenConfigured(t *testing.T) {
	caCertPEM, _, caCert, caKey := generateCertificateAuthority(t)
	serverCertPEM, serverKeyPEM := generateSignedCertificate(t, caCert, caKey, false, []string{"localhost"})

	dir := t.TempDir()
	serverCertFile := writePEMFile(t, dir, "server.crt", serverCertPEM)
	serverKeyFile := writePEMFile(t, dir, "server.key", serverKeyPEM)
	caFile := writePEMFile(t, dir, "ca.crt", caCertPEM)

	serverTLSConfig, err := config.TLSConfig{
		Enabled:  true,
		CertFile: serverCertFile,
		KeyFile:  serverKeyFile,
	}.ServerTLSConfig()
	if err != nil {
		t.Fatalf("build server tls config: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig)
	if err != nil {
		t.Fatalf("listen tls: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		accepted <- conn
	}()

	client := NewClient(config.SlaveConfig{
		MasterAddr: listener.Addr().String(),
		TLS: config.TLSConfig{
			Enabled:    true,
			CAFile:     caFile,
			ServerName: "localhost",
		},
	})

	dialed := make(chan net.Conn, 1)
	dialErr := make(chan error, 1)
	go func() {
		conn, err := client.dial()
		if err != nil {
			dialErr <- err
			return
		}
		dialed <- conn
	}()
	serverConn := waitAcceptedConn(t, accepted, errCh)
	defer serverConn.Close()
	if err := serverConn.(*tls.Conn).Handshake(); err != nil {
		t.Fatalf("server-side tls handshake: %v", err)
	}

	var conn net.Conn
	select {
	case conn = <-dialed:
		defer conn.Close()
	case err := <-dialErr:
		t.Fatalf("dial tls master: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for tls dial")
	}

	if _, ok := conn.(*tls.Conn); !ok {
		t.Fatalf("expected tls client connection, got %T", conn)
	}
}

func TestClientDialUsesMTLSWhenConfigured(t *testing.T) {
	caCertPEM, _, caCert, caKey := generateCertificateAuthority(t)
	serverCertPEM, serverKeyPEM := generateSignedCertificate(t, caCert, caKey, false, []string{"localhost"})
	clientCertPEM, clientKeyPEM := generateSignedCertificate(t, caCert, caKey, true, nil)

	dir := t.TempDir()
	serverCertFile := writePEMFile(t, dir, "server.crt", serverCertPEM)
	serverKeyFile := writePEMFile(t, dir, "server.key", serverKeyPEM)
	clientCertFile := writePEMFile(t, dir, "client.crt", clientCertPEM)
	clientKeyFile := writePEMFile(t, dir, "client.key", clientKeyPEM)
	caFile := writePEMFile(t, dir, "ca.crt", caCertPEM)

	serverTLSConfig, err := config.TLSConfig{
		Enabled:           true,
		CertFile:          serverCertFile,
		KeyFile:           serverKeyFile,
		CAFile:            caFile,
		RequireClientCert: true,
	}.ServerTLSConfig()
	if err != nil {
		t.Fatalf("build server tls config: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig)
	if err != nil {
		t.Fatalf("listen tls: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		accepted <- conn
	}()

	client := NewClient(config.SlaveConfig{
		MasterAddr: listener.Addr().String(),
		TLS: config.TLSConfig{
			Enabled:    true,
			CertFile:   clientCertFile,
			KeyFile:    clientKeyFile,
			CAFile:     caFile,
			ServerName: "localhost",
		},
	})

	dialed := make(chan net.Conn, 1)
	dialErr := make(chan error, 1)
	go func() {
		conn, err := client.dial()
		if err != nil {
			dialErr <- err
			return
		}
		dialed <- conn
	}()
	serverConn := waitAcceptedConn(t, accepted, errCh)
	defer serverConn.Close()
	if err := serverConn.(*tls.Conn).Handshake(); err != nil {
		t.Fatalf("server-side mTLS handshake: %v", err)
	}

	select {
	case conn := <-dialed:
		defer conn.Close()
	case err := <-dialErr:
		t.Fatalf("dial mTLS master: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for mTLS dial")
	}

	state := serverConn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatalf("expected peer client certificate to be present")
	}
}

func TestClientDialFailsWithWrongCA(t *testing.T) {
	_, _, serverCA, serverCAKey := generateCertificateAuthority(t)
	otherCAPEM, _, _, _ := generateCertificateAuthority(t)
	serverCertPEM, serverKeyPEM := generateSignedCertificate(t, serverCA, serverCAKey, false, []string{"localhost"})

	dir := t.TempDir()
	serverCertFile := writePEMFile(t, dir, "server.crt", serverCertPEM)
	serverKeyFile := writePEMFile(t, dir, "server.key", serverKeyPEM)
	wrongCAFile := writePEMFile(t, dir, "wrong-ca.crt", otherCAPEM)

	serverTLSConfig, err := config.TLSConfig{
		Enabled:  true,
		CertFile: serverCertFile,
		KeyFile:  serverKeyFile,
	}.ServerTLSConfig()
	if err != nil {
		t.Fatalf("build server tls config: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig)
	if err != nil {
		t.Fatalf("listen tls: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	client := NewClient(config.SlaveConfig{
		MasterAddr: listener.Addr().String(),
		TLS: config.TLSConfig{
			Enabled:    true,
			CAFile:     wrongCAFile,
			ServerName: "localhost",
		},
	})

	if _, err := client.dial(); err == nil {
		t.Fatalf("expected tls dial to fail with wrong CA")
	}
}

func waitAcceptedConn(t *testing.T, accepted <-chan net.Conn, errCh <-chan error) net.Conn {
	t.Helper()
	select {
	case conn := <-accepted:
		return conn
	case err := <-errCh:
		t.Fatalf("accept tls connection: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for tls accept")
	}
	return nil
}

func generateCertificateAuthority(t *testing.T) ([]byte, []byte, *x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}

	serial, err := randomSerialNumber()
	if err != nil {
		t.Fatalf("generate ca serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "nut-server-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create ca certificate: %v", err)
	}

	certificate, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parse ca certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM, certificate, privateKey
}

func generateSignedCertificate(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey, isClient bool, hosts []string) ([]byte, []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}

	serial, err := randomSerialNumber()
	if err != nil {
		t.Fatalf("generate leaf serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "nut-server-test-leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	if isClient {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		template.IPAddresses = nil
		template.DNSNames = nil
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func writePEMFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write pem file %s: %v", path, err)
	}
	return path
}
