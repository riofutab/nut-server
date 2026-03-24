package master

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

func TestRegistryRemoveIfMatchKeepsNewerConnection(t *testing.T) {
	registry := NewRegistry()

	oldConn, oldPeer := net.Pipe()
	defer oldPeer.Close()
	oldClient := NewClient(oldConn)
	oldClient.NodeID = "node-1"

	newConn, newPeer := net.Pipe()
	defer newPeer.Close()
	newClient := NewClient(newConn)
	newClient.NodeID = "node-1"
	defer newClient.Close()

	registry.Set(oldClient)
	registry.Set(newClient)
	registry.RemoveIfMatch("node-1", oldClient)

	got, ok := registry.Get("node-1")
	if !ok {
		t.Fatalf("expected node to remain registered")
	}
	if got != newClient {
		t.Fatalf("expected newer client to remain registered")
	}
}

func TestReplayPendingShutdownSendsActiveCommand(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	message := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "battery low",
		IssuedAt:  time.Now().UTC(),
	}
	server.rememberShutdownCommand(message, []*Client{{NodeID: "node-1"}})

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.replayPendingShutdown(client)
	}()

	env := readEnvelopeFromConn(t, peer)
	if err := <-errCh; err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}
	if env.Type != protocol.TypeShutdown {
		t.Fatalf("expected shutdown envelope, got %s", env.Type)
	}

	var replayed protocol.ShutdownMessage
	decodePayloadForTest(t, env.Data, &replayed)
	if replayed.CommandID != message.CommandID {
		t.Fatalf("expected command id %s, got %s", message.CommandID, replayed.CommandID)
	}
}

func TestReplayPendingShutdownSkipsTerminalNodes(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	message := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "battery low",
		IssuedAt:  time.Now().UTC(),
	}
	server.rememberShutdownCommand(message, []*Client{{NodeID: "node-1"}})
	server.recordShutdownUpdate(protocol.ShutdownAckMessage{
		CommandID: message.CommandID,
		NodeID:    "node-1",
		Status:    protocol.ShutdownStatusExecuted,
		AckedAt:   time.Now().UTC(),
	})

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-1"

	if err := server.replayPendingShutdown(client); err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}

	if err := peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatalf("expected no replay for terminal node")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no replay is sent, got %v", err)
	}
}

func TestReplayPendingShutdownSkipsUntargetedNodes(t *testing.T) {
	server := NewServer(config.MasterConfig{})
	message := protocol.ShutdownMessage{
		CommandID: "shutdown-1",
		Reason:    "battery low",
		IssuedAt:  time.Now().UTC(),
	}
	server.rememberShutdownCommand(message, []*Client{{NodeID: "node-1"}})

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := NewClient(conn)
	client.NodeID = "node-2"

	if err := server.replayPendingShutdown(client); err != nil {
		t.Fatalf("replay pending shutdown: %v", err)
	}

	if err := peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatalf("expected no replay for untargeted node")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected timeout when no replay is sent, got %v", err)
	}
}

func readEnvelopeFromConn(t *testing.T, conn net.Conn) protocol.Envelope {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func decodePayloadForTest(t *testing.T, data interface{}, dst interface{}) {
	t.Helper()
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
}
