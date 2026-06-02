package master

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"testing"
	"time"
)

// TestMatchCertIdentity exhaustively covers the CN/SAN binding logic that stops
// a token holder from registering as an arbitrary node_id under mTLS.
func TestMatchCertIdentity(t *testing.T) {
	tests := []struct {
		name    string
		cert    *x509.Certificate
		nodeID  string
		wantErr bool
	}{
		{
			name:   "empty chain falls back to token trust",
			cert:   nil,
			nodeID: "node-1",
		},
		{
			name:   "common name matches",
			cert:   &x509.Certificate{Subject: pkix.Name{CommonName: "node-1"}},
			nodeID: "node-1",
		},
		{
			name:   "dns san matches when cn differs",
			cert:   &x509.Certificate{Subject: pkix.Name{CommonName: "leaf"}, DNSNames: []string{"other", "node-1"}},
			nodeID: "node-1",
		},
		{
			name:    "no cn or san match is rejected",
			cert:    &x509.Certificate{Subject: pkix.Name{CommonName: "leaf"}, DNSNames: []string{"other"}},
			nodeID:  "node-1",
			wantErr: true,
		},
		{
			name:    "empty node id never silently matches an empty cn",
			cert:    &x509.Certificate{Subject: pkix.Name{CommonName: "leaf"}},
			nodeID:  "node-1",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var certs []*x509.Certificate
			if tc.cert != nil {
				certs = []*x509.Certificate{tc.cert}
			}
			err := matchCertIdentity(certs, tc.nodeID)
			if tc.wantErr && err == nil {
				t.Fatalf("expected mismatch error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected match, got error: %v", err)
			}
		})
	}
}

// TestVerifyPeerIdentityNonTLSPaths covers the early-return branches that keep
// token-only and plaintext deployments working.
func TestVerifyPeerIdentityNonTLSPaths(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	// enforce=false: never inspects the connection.
	if err := verifyPeerIdentity(left, "node-1", false); err != nil {
		t.Fatalf("enforce=false should always pass: %v", err)
	}
	// enforce=true but the connection is plain TCP (not *tls.Conn): no
	// cryptographic identity to bind, so it remains trust-on-first-use.
	if err := verifyPeerIdentity(left, "node-1", true); err != nil {
		t.Fatalf("non-tls conn should pass: %v", err)
	}
}

// TestVerifyPeerIdentityOverTLS exercises the real *tls.Conn path end to end:
// a client certificate whose CN equals the node_id is accepted, a mismatched
// node_id is rejected.
func TestVerifyPeerIdentityOverTLS(t *testing.T) {
	caCertPEM, _, caCert, caKey := generateCertificateAuthority(t)
	serverCertPEM, serverKeyPEM := generateSignedCertificate(t, caCert, caKey, false, []string{"localhost"})
	clientCertPEM, clientKeyPEM := generateIdentityClientCertificate(t, caCert, caKey, "node-1", []string{"node-alt"})

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCertPEM) {
		t.Fatalf("append ca")
	}
	serverCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}

	serverConn := handshakeMTLS(t, serverCert, clientCert, pool)
	defer serverConn.Close()

	state := serverConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatalf("expected client certificate on server side")
	}

	// CN == node_id → accepted.
	if err := verifyPeerIdentity(serverConn, "node-1", true); err != nil {
		t.Fatalf("matching CN should be accepted: %v", err)
	}
	// SAN == node_id → accepted.
	if err := verifyPeerIdentity(serverConn, "node-alt", true); err != nil {
		t.Fatalf("matching SAN should be accepted: %v", err)
	}
	// Neither CN nor SAN matches → rejected.
	if err := verifyPeerIdentity(serverConn, "imposter", true); err == nil {
		t.Fatalf("mismatched node_id must be rejected under mTLS")
	}
}

// handshakeMTLS performs a full mTLS handshake over net.Pipe and returns the
// server-side *tls.Conn with the client's certificate available.
func handshakeMTLS(t *testing.T, serverCert, clientCert tls.Certificate, pool *x509.CertPool) *tls.Conn {
	t.Helper()
	srvPipe, cliPipe := net.Pipe()

	serverConn := tls.Server(srvPipe, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	})
	clientConn := tls.Client(cliPipe, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      pool,
		ServerName:   "localhost",
	})

	errCh := make(chan error, 1)
	go func() { errCh <- clientConn.Handshake() }()
	if err := serverConn.Handshake(); err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("client handshake: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("client handshake timed out")
	}
	t.Cleanup(func() { clientConn.Close() })
	return serverConn
}

// generateIdentityClientCertificate signs a client certificate with a caller
// chosen CommonName and DNS SANs so identity-binding can be exercised.
func generateIdentityClientCertificate(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey, commonName string, sans []string) ([]byte, []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	serial, err := randomSerialNumber()
	if err != nil {
		t.Fatalf("client serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     sans,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM
}
