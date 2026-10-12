package slave

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

const configToken = "config-token"

type masterConn struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func (m *masterConn) read(t *testing.T) (protocol.Envelope, error) {
	t.Helper()
	_ = m.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	return protocol.ReadEnvelope(m.reader)
}

func (m *masterConn) readRegister(t *testing.T) protocol.RegisterMessage {
	t.Helper()
	env, err := m.read(t)
	if err != nil {
		t.Fatalf("read register: %v", err)
	}
	if env.Type != protocol.TypeRegister {
		t.Fatalf("got %s want register", env.Type)
	}
	var register protocol.RegisterMessage
	if err := protocol.DecodePayload(env.Data, &register); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	return register
}

func (m *masterConn) ack(t *testing.T, ack protocol.RegisterAckMessage) {
	t.Helper()
	if err := protocol.WriteEnvelope(m.writer, protocol.TypeRegisterAck, ack); err != nil {
		t.Fatalf("write ack: %v", err)
	}
}

// startFakeMaster returns a client pointed at a loopback listener plus a
// function that accepts the next slave connection.
func startFakeMaster(t *testing.T, cfg config.SlaveConfig) (*Client, func() *masterConn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	cfg.MasterAddr = listener.Addr().String()
	cfg.NodeID = "node-1"
	cfg.Token = configToken
	cfg.DryRun = true
	accept := func() *masterConn {
		t.Helper()
		conn, err := listener.Accept()
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return &masterConn{conn: conn, reader: bufio.NewReader(conn), writer: bufio.NewWriter(conn)}
	}
	return NewClient(cfg), accept
}

// runOnceAsync runs one connection attempt; the returned channel yields its
// error once the attempt ends (cancel or connection close).
func runOnceAsync(t *testing.T, client *Client) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.runOnce(ctx, func() {}) }()
	t.Cleanup(cancel)
	return cancel, done
}

func waitRunOnce(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("runOnce did not return")
		return nil
	}
}

func readStateFile(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(content, &state); err != nil {
		t.Fatalf("decode state file: %v", err)
	}
	return state
}

func TestSlaveStoresIssuedTokenBeforeSendingAnythingElse(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "slave-state.json")
	client, accept := startFakeMaster(t, config.SlaveConfig{StateFile: stateFile})
	cancel, done := runOnceAsync(t, client)
	master := accept()

	register := master.readRegister(t)
	master.ack(t, protocol.RegisterAckMessage{Accepted: true, IssuedToken: "issued-1"})
	next, err := master.read(t)

	if err != nil {
		t.Fatalf("expected an envelope confirming the token, got %v", err)
	}
	if register.Token != configToken {
		t.Fatalf("first register should use the config token, got %q", register.Token)
	}
	if next.Type != protocol.TypePing {
		t.Fatalf("got %s want ping", next.Type)
	}
	if got := string(readStateFile(t, stateFile)["issued_token"]); got != `"issued-1"` {
		t.Fatalf("issued token must be persisted before the next envelope, got %s", got)
	}
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state file mode=%o want 600", perm)
	}
	cancel()
	waitRunOnce(t, done)
}

func TestSlaveRegistersWithStoredIssuedToken(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "slave-state.json")
	seed := NewClient(config.SlaveConfig{StateFile: stateFile})
	if err := seed.setIssuedToken("issued-1"); err != nil {
		t.Fatalf("seed issued token: %v", err)
	}
	client, accept := startFakeMaster(t, config.SlaveConfig{StateFile: stateFile})
	_, done := runOnceAsync(t, client)
	master := accept()

	register := master.readRegister(t)

	if register.Token != "issued-1" {
		t.Fatalf("register token=%q want the stored issued token", register.Token)
	}
	_ = master.conn.Close()
	waitRunOnce(t, done)
}

func TestSlaveClearsRejectedIssuedTokenAndFallsBackToConfigToken(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "slave-state.json")
	seed := NewClient(config.SlaveConfig{StateFile: stateFile})
	if err := seed.setIssuedToken("revoked"); err != nil {
		t.Fatalf("seed issued token: %v", err)
	}
	client, accept := startFakeMaster(t, config.SlaveConfig{StateFile: stateFile})

	_, done := runOnceAsync(t, client)
	first := accept()
	first.readRegister(t)
	first.ack(t, protocol.RegisterAckMessage{Accepted: false, Message: "invalid token"})
	err := waitRunOnce(t, done)

	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("runOnce err=%v want invalid token rejection", err)
	}
	if _, ok := readStateFile(t, stateFile)["issued_token"]; ok {
		t.Fatal("rejected issued token should be cleared from the state file")
	}
	_, done = runOnceAsync(t, client)
	second := accept()
	if register := second.readRegister(t); register.Token != configToken {
		t.Fatalf("next register token=%q want config token", register.Token)
	}
	_ = second.conn.Close()
	waitRunOnce(t, done)
}

func TestSlaveKeepsIssuedTokenOnOtherRejections(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "slave-state.json")
	seed := NewClient(config.SlaveConfig{StateFile: stateFile})
	if err := seed.setIssuedToken("issued-1"); err != nil {
		t.Fatalf("seed issued token: %v", err)
	}
	client, accept := startFakeMaster(t, config.SlaveConfig{StateFile: stateFile})

	_, done := runOnceAsync(t, client)
	master := accept()
	master.readRegister(t)
	master.ack(t, protocol.RegisterAckMessage{Accepted: false, Message: "client certificate identity mismatch"})
	waitRunOnce(t, done)

	if got := string(readStateFile(t, stateFile)["issued_token"]); got != `"issued-1"` {
		t.Fatalf("issued token should be kept, got %s", got)
	}
}

func TestSlaveClosesConnectionWhenIssuedTokenCannotBePersisted(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	client, accept := startFakeMaster(t, config.SlaveConfig{StateFile: filepath.Join(blocker, "slave-state.json")})
	_, done := runOnceAsync(t, client)
	master := accept()
	master.readRegister(t)

	master.ack(t, protocol.RegisterAckMessage{Accepted: true, IssuedToken: "issued-1"})
	env, err := master.read(t)

	if err == nil {
		t.Fatalf("slave must close the connection without confirming, got %s", env.Type)
	}
	if runErr := waitRunOnce(t, done); runErr == nil {
		t.Fatal("runOnce should report the persistence failure")
	}
	client.commandMu.Lock()
	defer client.commandMu.Unlock()
	if client.issuedToken != "" {
		t.Fatal("an unpersisted issued token must not be kept in memory")
	}
}
