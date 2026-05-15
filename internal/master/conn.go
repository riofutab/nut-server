package master

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
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
		log.Printf("register failed from %s: %v", conn.RemoteAddr(), err)
		_ = client.Send(protocol.TypeError, protocol.ErrorMessage{Message: err.Error()})
		return
	}

	if !security.ValidateToken(s.cfg.AuthTokens, register.Token) {
		_ = client.Send(protocol.TypeRegisterAck, protocol.RegisterAckMessage{Accepted: false, Message: "invalid token"})
		log.Printf("reject slave %s: invalid token", register.NodeID)
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
		log.Printf("ack register to %s failed: %v", client.NodeID, err)
		return
	}
	log.Printf("slave registered node_id=%s hostname=%s tags=%v", client.NodeID, client.Hostname, client.Tags)
	if err := s.replayPendingShutdown(client); err != nil {
		log.Printf("replay pending shutdown to %s failed: %v", client.NodeID, err)
		return
	}

	for {
		_ = client.SetReadDeadline(IdleReadDeadline)
		env, err := client.ReadEnvelope()
		if err != nil {
			if err != io.EOF {
				log.Printf("read from %s failed: %v", client.NodeID, err)
			}
			return
		}
		client.Touch()
		s.directory.Touch(client.NodeID, time.Now().UTC())
		if err := s.handleEnvelope(client, env); err != nil {
			log.Printf("handle %s from %s failed: %v", env.Type, client.NodeID, err)
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
		log.Printf("shutdown update node_id=%s command_id=%s status=%s message=%s", ack.NodeID, ack.CommandID, ack.Status, ack.Message)
		return nil
	default:
		return fmt.Errorf("unsupported message type %s", env.Type)
	}
}
