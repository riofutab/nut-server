package master

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"nut-server/internal/protocol"
	"nut-server/internal/security"
)

func (s *Server) handleConn(conn net.Conn) {
	client := NewClient(conn)
	defer func() {
		if client.NodeID != "" {
			s.registry.RemoveIfMatch(client.NodeID, client)
		}
		_ = client.Close()
	}()

	_ = client.SetReadDeadline(HandshakeReadDeadline)
	register, err := s.readRegister(client)
	if err != nil {
		slog.Warn("register failed", "peer", conn.RemoteAddr().String(), "err", err)
		_ = client.Send(protocol.TypeError, protocol.ErrorMessage{Message: err.Error()})
		return
	}

	if !security.ValidateToken(s.cfg.AuthTokens, register.Token) {
		_ = client.Send(protocol.TypeRegisterAck, protocol.RegisterAckMessage{Accepted: false, Message: "invalid token"})
		slog.Warn("reject slave: invalid token", "node_id", register.NodeID, "peer", conn.RemoteAddr().String())
		return
	}

	client.NodeID = register.NodeID
	client.Hostname = register.Hostname
	client.Tags = register.Tags
	client.Touch()
	s.registry.Set(client)
	s.directory.Observe(register.NodeID, register.Hostname, register.Tags, time.Now().UTC())
	s.saveStateForDirectoryChange()

	if err := client.Send(protocol.TypeRegisterAck, protocol.RegisterAckMessage{Accepted: true, Message: "registered"}); err != nil {
		slog.Error("ack register failed", "node_id", client.NodeID, "err", err)
		return
	}
	slog.Info("slave registered", "node_id", client.NodeID, "hostname", client.Hostname, "tags", client.Tags)
	if err := s.replayPendingShutdown(client); err != nil {
		slog.Error("replay pending shutdown failed", "node_id", client.NodeID, "err", err)
		return
	}

	for {
		_ = client.SetReadDeadline(IdleReadDeadline)
		env, err := client.ReadEnvelope()
		if err != nil {
			if err != io.EOF {
				slog.Warn("read from slave failed", "node_id", client.NodeID, "err", err)
			}
			return
		}
		client.Touch()
		s.directory.Touch(client.NodeID, time.Now().UTC())
		if err := s.handleEnvelope(client, env); err != nil {
			slog.Error("handle envelope failed", "node_id", client.NodeID, "type", env.Type, "err", err)
		}
	}
}

func (s *Server) readRegister(client *Client) (protocol.RegisterMessage, error) {
	env, err := client.ReadEnvelope()
	if err != nil {
		return protocol.RegisterMessage{}, err
	}
	if env.Type != protocol.TypeRegister {
		return protocol.RegisterMessage{}, fmt.Errorf("expected register message, got %s", env.Type)
	}
	payload, err := json.Marshal(env.Data)
	if err != nil {
		return protocol.RegisterMessage{}, fmt.Errorf("marshal register payload: %w", err)
	}
	var register protocol.RegisterMessage
	if err := json.Unmarshal(payload, &register); err != nil {
		return protocol.RegisterMessage{}, fmt.Errorf("decode register payload: %w", err)
	}
	if register.NodeID == "" {
		return protocol.RegisterMessage{}, fmt.Errorf("node_id is required")
	}
	if register.Hostname == "" {
		return protocol.RegisterMessage{}, fmt.Errorf("hostname is required")
	}
	return register, nil
}

func (s *Server) handleEnvelope(client *Client, env protocol.Envelope) error {
	switch env.Type {
	case protocol.TypePing:
		client.Touch()
		return nil
	case protocol.TypeShutdownAck:
		payload, err := json.Marshal(env.Data)
		if err != nil {
			return err
		}
		var ack protocol.ShutdownAckMessage
		if err := json.Unmarshal(payload, &ack); err != nil {
			return err
		}
		s.recordShutdownUpdate(ack)
		slog.Info("shutdown update",
			"command_id", ack.CommandID,
			"node_id", ack.NodeID,
			"status", ack.Status,
			"message", ack.Message)
		return nil
	default:
		return fmt.Errorf("unsupported message type %s", env.Type)
	}
}
