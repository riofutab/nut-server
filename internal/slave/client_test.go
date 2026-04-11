package slave

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func TestHandleShutdownReportsLifecycleAndDeduplicatesExecution(t *testing.T) {
	client := NewClient(config.SlaveConfig{NodeID: "node-1"})
	executor := &fakeShutdownExecutor{}
	client.shutdown = executor

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	reader := bufio.NewReader(peer)

	session := newConnectionSession(conn)
	shutdown := protocol.Envelope{
		Type: protocol.TypeShutdown,
		Data: protocol.ShutdownMessage{
			CommandID: "shutdown-1",
			Reason:    "battery low",
			IssuedAt:  time.Now().UTC(),
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleEnvelope(shutdown, session)
	}()

	if first := readShutdownUpdate(t, peer, reader); first.Status != protocol.ShutdownStatusAccepted {
		t.Fatalf("expected first status accepted, got %s", first.Status)
	}
	if second := readShutdownUpdate(t, peer, reader); second.Status != protocol.ShutdownStatusExecuting {
		t.Fatalf("expected second status executing, got %s", second.Status)
	}
	if third := readShutdownUpdate(t, peer, reader); third.Status != protocol.ShutdownStatusExecuted {
		t.Fatalf("expected third status executed, got %s", third.Status)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handle shutdown: %v", err)
	}

	if executor.Calls() != 1 {
		t.Fatalf("expected shutdown executor to run once, got %d", executor.Calls())
	}

	dupErrCh := make(chan error, 1)
	go func() {
		dupErrCh <- client.handleEnvelope(shutdown, session)
	}()

	duplicate := readShutdownUpdate(t, peer, reader)
	if err := <-dupErrCh; err != nil {
		t.Fatalf("handle duplicate shutdown: %v", err)
	}
	if duplicate.Status != protocol.ShutdownStatusExecuted {
		t.Fatalf("expected duplicate replay to return executed status, got %s", duplicate.Status)
	}
	if executor.Calls() != 1 {
		t.Fatalf("expected duplicate shutdown not to re-run executor, got %d calls", executor.Calls())
	}
}

func TestHandleShutdownReportsFailures(t *testing.T) {
	client := NewClient(config.SlaveConfig{NodeID: "node-1"})
	executor := &fakeShutdownExecutor{err: errTestShutdownFailed}
	client.shutdown = executor

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	reader := bufio.NewReader(peer)

	session := newConnectionSession(conn)
	shutdown := protocol.Envelope{
		Type: protocol.TypeShutdown,
		Data: protocol.ShutdownMessage{
			CommandID: "shutdown-2",
			Reason:    "battery low",
			IssuedAt:  time.Now().UTC(),
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleEnvelope(shutdown, session)
	}()

	_ = readShutdownUpdate(t, peer, reader)
	_ = readShutdownUpdate(t, peer, reader)
	final := readShutdownUpdate(t, peer, reader)
	if err := <-errCh; err != nil {
		t.Fatalf("handle shutdown: %v", err)
	}
	if final.Status != protocol.ShutdownStatusFailed {
		t.Fatalf("expected failed status, got %s", final.Status)
	}
	if final.Message == "" {
		t.Fatalf("expected failure message to be populated")
	}
}

func TestHandleShutdownResumesAcceptedStateAfterReconnect(t *testing.T) {
	client := NewClient(config.SlaveConfig{NodeID: "node-1"})
	executor := &fakeShutdownExecutor{}
	client.shutdown = executor
	client.setCommandState(protocol.ShutdownAckMessage{
		CommandID: "shutdown-accepted",
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusAccepted,
		Message:   "shutdown command accepted",
		AckedAt:   time.Now().UTC().Add(-2 * time.Second),
	})

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	reader := bufio.NewReader(peer)

	session := newConnectionSession(conn)
	shutdown := protocol.Envelope{
		Type: protocol.TypeShutdown,
		Data: protocol.ShutdownMessage{
			CommandID: "shutdown-accepted",
			Reason:    "battery low",
			IssuedAt:  time.Now().UTC(),
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleEnvelope(shutdown, session)
	}()

	if first := readShutdownUpdate(t, peer, reader); first.Status != protocol.ShutdownStatusAccepted {
		t.Fatalf("expected replayed accepted status, got %s", first.Status)
	}
	if second := readShutdownUpdate(t, peer, reader); second.Status != protocol.ShutdownStatusExecuting {
		t.Fatalf("expected resumed executing status, got %s", second.Status)
	}
	if third := readShutdownUpdate(t, peer, reader); third.Status != protocol.ShutdownStatusExecuted {
		t.Fatalf("expected resumed executed status, got %s", third.Status)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handle resumed shutdown: %v", err)
	}
	if executor.Calls() != 1 {
		t.Fatalf("expected resumed shutdown executor to run once, got %d", executor.Calls())
	}
}

func TestHandleShutdownResumesExecutingStateAfterReconnect(t *testing.T) {
	client := NewClient(config.SlaveConfig{NodeID: "node-1"})
	executor := &fakeShutdownExecutor{}
	client.shutdown = executor
	client.setCommandState(protocol.ShutdownAckMessage{
		CommandID: "shutdown-executing",
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuting,
		Message:   "executing shutdown command",
		AckedAt:   time.Now().UTC().Add(-2 * time.Second),
	})

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	reader := bufio.NewReader(peer)

	session := newConnectionSession(conn)
	shutdown := protocol.Envelope{
		Type: protocol.TypeShutdown,
		Data: protocol.ShutdownMessage{
			CommandID: "shutdown-executing",
			Reason:    "battery low",
			IssuedAt:  time.Now().UTC(),
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleEnvelope(shutdown, session)
	}()

	if first := readShutdownUpdate(t, peer, reader); first.Status != protocol.ShutdownStatusExecuting {
		t.Fatalf("expected replayed executing status, got %s", first.Status)
	}
	if second := readShutdownUpdate(t, peer, reader); second.Status != protocol.ShutdownStatusExecuted {
		t.Fatalf("expected resumed executed status, got %s", second.Status)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handle resumed shutdown: %v", err)
	}
	if executor.Calls() != 1 {
		t.Fatalf("expected resumed shutdown executor to run once, got %d", executor.Calls())
	}
}

func TestHandleShutdownDoesNotDuplicateActiveExecutionOnReconnect(t *testing.T) {
	client := NewClient(config.SlaveConfig{NodeID: "node-1"})
	executor := &fakeShutdownExecutor{}
	client.shutdown = executor
	client.setCommandState(protocol.ShutdownAckMessage{
		CommandID: "shutdown-running",
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuting,
		Message:   "executing shutdown command",
		AckedAt:   time.Now().UTC().Add(-2 * time.Second),
	})
	client.commandMu.Lock()
	client.executingCommands["shutdown-running"] = struct{}{}
	client.commandMu.Unlock()

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	reader := bufio.NewReader(peer)

	session := newConnectionSession(conn)
	shutdown := protocol.Envelope{
		Type: protocol.TypeShutdown,
		Data: protocol.ShutdownMessage{
			CommandID: "shutdown-running",
			Reason:    "battery low",
			IssuedAt:  time.Now().UTC(),
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleEnvelope(shutdown, session)
	}()

	if first := readShutdownUpdate(t, peer, reader); first.Status != protocol.ShutdownStatusExecuting {
		t.Fatalf("expected replayed executing status, got %s", first.Status)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handle active shutdown replay: %v", err)
	}
	if executor.Calls() != 0 {
		t.Fatalf("expected active execution not to restart, got %d calls", executor.Calls())
	}
	if err := peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatalf("expected no duplicate final status while execution is already running")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no duplicate final status is sent, got %v", err)
	}
}

type fakeShutdownExecutor struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeShutdownExecutor) Execute(_ *log.Logger) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.err
}

func (f *fakeShutdownExecutor) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var errTestShutdownFailed = testError("shutdown failed")

type testError string

func (e testError) Error() string {
	return string(e)
}

func readShutdownUpdate(t *testing.T, conn net.Conn, reader *bufio.Reader) protocol.ShutdownAckMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read shutdown update: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Type != protocol.TypeShutdownAck {
		t.Fatalf("expected shutdown_ack envelope, got %s", env.Type)
	}
	var update protocol.ShutdownAckMessage
	payload, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(payload, &update); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return update
}
