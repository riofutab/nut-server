package master

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

func TestServerListenUsesTLSWhenConfigured(t *testing.T) {
	caCertPEM, _, caCert, caKey := generateCertificateAuthority(t)
	serverCertPEM, serverKeyPEM := generateSignedCertificate(t, caCert, caKey, false, []string{"localhost"})

	dir := t.TempDir()
	serverCertFile := writePEMFile(t, dir, "server.crt", serverCertPEM)
	serverKeyFile := writePEMFile(t, dir, "server.key", serverKeyPEM)

	server := NewServer(config.MasterConfig{
		ListenAddr: randomLocalAddr(t),
		TLS: config.TLSConfig{
			Enabled:  true,
			CertFile: serverCertFile,
			KeyFile:  serverKeyFile,
		},
	})

	listener, err := server.listen()
	if err != nil {
		t.Fatalf("listen with tls: %v", err)
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

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caCertPEM) {
		t.Fatalf("append root ca")
	}
	dialed := make(chan *tls.Conn, 1)
	dialErr := make(chan error, 1)
	go func() {
		conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    rootCAs,
			ServerName: "localhost",
		})
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

	var clientConn *tls.Conn
	select {
	case clientConn = <-dialed:
		defer clientConn.Close()
	case err := <-dialErr:
		t.Fatalf("dial tls listener: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for tls dial")
	}

	if server.tlsConfig == nil {
		t.Fatalf("expected server tls config to be cached")
	}
}

func TestServerListenRequiresClientCertWhenConfigured(t *testing.T) {
	caCertPEM, _, caCert, caKey := generateCertificateAuthority(t)
	serverCertPEM, serverKeyPEM := generateSignedCertificate(t, caCert, caKey, false, []string{"localhost"})
	clientCertPEM, clientKeyPEM := generateSignedCertificate(t, caCert, caKey, true, nil)

	dir := t.TempDir()
	serverCertFile := writePEMFile(t, dir, "server.crt", serverCertPEM)
	serverKeyFile := writePEMFile(t, dir, "server.key", serverKeyPEM)
	clientCertFile := writePEMFile(t, dir, "client.crt", clientCertPEM)
	clientKeyFile := writePEMFile(t, dir, "client.key", clientKeyPEM)
	caFile := writePEMFile(t, dir, "ca.crt", caCertPEM)

	server := NewServer(config.MasterConfig{
		ListenAddr: randomLocalAddr(t),
		TLS: config.TLSConfig{
			Enabled:           true,
			CertFile:          serverCertFile,
			KeyFile:           serverKeyFile,
			CAFile:            caFile,
			RequireClientCert: true,
		},
	})

	listener, err := server.listen()
	if err != nil {
		t.Fatalf("listen with mTLS: %v", err)
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

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caCertPEM) {
		t.Fatalf("append root ca")
	}
	certificate, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	dialed := make(chan *tls.Conn, 1)
	dialErr := make(chan error, 1)
	go func() {
		conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
			MinVersion:   tls.VersionTLS12,
			RootCAs:      rootCAs,
			ServerName:   "localhost",
			Certificates: []tls.Certificate{certificate},
		})
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

	var clientConn *tls.Conn
	select {
	case clientConn = <-dialed:
		defer clientConn.Close()
	case err := <-dialErr:
		t.Fatalf("dial mTLS listener: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for mTLS dial")
	}

	state := serverConn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatalf("expected peer client certificate to be present")
	}
	if server.tlsConfig == nil || server.tlsConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("expected server tls config to require client cert")
	}
}

func TestServerListenWithoutTLSReturnsPlainListener(t *testing.T) {
	server := NewServer(config.MasterConfig{ListenAddr: randomLocalAddr(t)})
	listener, err := server.listen()
	if err != nil {
		t.Fatalf("listen without tls: %v", err)
	}
	defer listener.Close()

	if server.tlsConfig != nil {
		t.Fatalf("expected no cached tls config for plain tcp listener")
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
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "nut-server-test-ca",
		},
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
		Subject: pkix.Name{
			CommonName: "nut-server-test-leaf",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    hosts,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
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

func randomLocalAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local addr: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}
	return addr
}
