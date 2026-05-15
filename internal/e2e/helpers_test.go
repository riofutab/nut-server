//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/master"
	"nut-server/internal/protocol"
	"nut-server/internal/slave"
)

const (
	pollInterval = 25 * time.Millisecond
	waitTimeout  = 5 * time.Second
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("alloc free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

type runningMaster struct {
	server     *master.Server
	cfg        config.MasterConfig
	cancel     context.CancelFunc
	done       chan struct{}
	adminURL   string
	listenAddr string
	adminToken string
	authToken  string
}

func newMasterConfig(t *testing.T) config.MasterConfig {
	t.Helper()
	tmp := t.TempDir()
	listenPort := freeTCPPort(t)
	adminPort := freeTCPPort(t)
	return config.MasterConfig{
		ListenAddr:      fmt.Sprintf("127.0.0.1:%d", listenPort),
		AdminListenAddr: fmt.Sprintf("127.0.0.1:%d", adminPort),
		AdminToken:      "e2e-admin-token",
		StateFile:       filepath.Join(tmp, "master-state.json"),
		AuthTokens:      []string{"e2e-auth-token"},
		PollInterval:    config.Duration{Duration: 250 * time.Millisecond},
		CommandTimeout:  config.Duration{Duration: 2 * time.Second},
		OfflineAfter:    config.Duration{Duration: 1 * time.Second},
		DryRun:          true,
		LogUPSStatus:    false,
		ShutdownPolicy: config.ShutdownPolicy{
			RequireOnBattery:  true,
			MinBatteryCharge:  30,
			MinRuntimeMinutes: 15,
			ShutdownReason:    "e2e",
		},
		SNMP: config.SNMPConfig{
			Target:         "127.0.0.1",
			Port:           161,
			Community:      "public",
			Version:        "2c",
			TimeoutSeconds: 1,
		},
	}
}

func startMaster(t *testing.T, cfg config.MasterConfig) *runningMaster {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server := master.NewServer(cfg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Run(ctx)
	}()
	scheme := "http"
	if cfg.TLS.EnabledForServer() {
		scheme = "https"
	}
	rm := &runningMaster{
		server:     server,
		cfg:        cfg,
		cancel:     cancel,
		done:       done,
		adminURL:   fmt.Sprintf("http://%s", cfg.AdminListenAddr),
		listenAddr: fmt.Sprintf("%s://%s", scheme, cfg.ListenAddr),
		adminToken: cfg.AdminToken,
		authToken:  cfg.AuthTokens[0],
	}
	t.Cleanup(rm.Stop)
	rm.waitAdminReady(t)
	return rm
}

func (rm *runningMaster) Stop() {
	rm.cancel()
	select {
	case <-rm.done:
	case <-time.After(5 * time.Second):
	}
}

func (rm *runningMaster) waitAdminReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, rm.adminURL+"/status", nil)
		req.Header.Set("Authorization", "Bearer "+rm.adminToken)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("master admin not ready at %s within %s", rm.adminURL, waitTimeout)
}

func (rm *runningMaster) Status(t *testing.T) protocol.StatusResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rm.adminURL+"/status", nil)
	if err != nil {
		t.Fatalf("build status request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+rm.adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch /status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/status returned %d", resp.StatusCode)
	}
	var status protocol.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	return status
}

func (rm *runningMaster) TriggerShutdown(t *testing.T, body protocol.ShutdownRequest) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode shutdown body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, rm.adminURL+"/commands/shutdown", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build shutdown request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+rm.adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("trigger shutdown: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("shutdown POST returned %d: %s", resp.StatusCode, buf)
	}
}

func (rm *runningMaster) FetchMetrics(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(rm.adminURL + "/metrics")
	if err != nil {
		t.Fatalf("fetch /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics returned %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

type runningSlave struct {
	cfg    config.SlaveConfig
	cancel context.CancelFunc
	done   chan struct{}
}

func newSlaveConfig(t *testing.T, masterCfg config.MasterConfig, nodeID string) config.SlaveConfig {
	t.Helper()
	tmp := t.TempDir()
	return config.SlaveConfig{
		NodeID:            nodeID,
		MasterAddr:        masterCfg.ListenAddr,
		Token:             masterCfg.AuthTokens[0],
		StateFile:         filepath.Join(tmp, nodeID+"-state.json"),
		ReconnectInterval: config.Duration{Duration: 100 * time.Millisecond},
		DryRun:            true,
		ShutdownCommand:   []string{"/bin/true"},
	}
}

func startSlave(t *testing.T, cfg config.SlaveConfig) *runningSlave {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := slave.NewClient(cfg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Run(ctx)
	}()
	rs := &runningSlave{cfg: cfg, cancel: cancel, done: done}
	t.Cleanup(rs.Stop)
	return rs
}

func (rs *runningSlave) Stop() {
	rs.cancel()
	select {
	case <-rs.done:
	case <-time.After(5 * time.Second):
	}
}

func waitUntil(t *testing.T, label string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for: %s", label)
}

func nodeByID(status protocol.StatusResponse, nodeID string) *protocol.NodeStatus {
	for i := range status.Nodes {
		if status.Nodes[i].NodeID == nodeID {
			return &status.Nodes[i]
		}
	}
	return nil
}

func writeTLSCertPair(t *testing.T, host string) (certFile, keyFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa generate: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{host},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	tmp := t.TempDir()
	certFile = filepath.Join(tmp, "cert.pem")
	keyFile = filepath.Join(tmp, "key.pem")
	if err := writePEM(certFile, "CERTIFICATE", derBytes); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := writePEM(keyFile, "EC PRIVATE KEY", keyBytes); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

func writePEM(path, blockType string, der []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}

