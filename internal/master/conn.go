package master

import (
	"crypto/tls"
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
		recordRegisterAttempt("invalid")
		_ = client.Send(protocol.TypeError, protocol.ErrorMessage{Message: err.Error()})
		return
	}

	if !security.ValidateToken(s.cfg.AuthTokens, register.Token) {
		_ = client.Send(protocol.TypeRegisterAck, protocol.RegisterAckMessage{Accepted: false, Message: "invalid token"})
		slog.Warn("reject slave: invalid token", "node_id", register.NodeID, "peer", conn.RemoteAddr().String())
		recordRegisterAttempt("rejected")
		return
	}

	if err := verifyPeerIdentity(conn, register.NodeID, s.cfg.TLS.BindNodeIDToCert); err != nil {
		_ = client.Send(protocol.TypeRegisterAck, protocol.RegisterAckMessage{Accepted: false, Message: "client certificate identity mismatch"})
		slog.Warn("reject slave: client cert identity mismatch", "node_id", register.NodeID, "peer", conn.RemoteAddr().String(), "err", err)
		recordRegisterAttempt("rejected")
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
	recordRegisterAttempt("accepted")
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
	var register protocol.RegisterMessage
	if err := protocol.DecodePayload(env.Data, &register); err != nil {
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

// verifyPeerIdentity optionally binds the claimed node_id to the TLS client
// certificate. When tls.bind_node_id_to_cert is enabled and the peer presents a
// client certificate, the node_id must equal the certificate CommonName or one
// of its DNS SANs — this stops any token holder from registering as (or forging
// acks for) an arbitrary node_id under mTLS. It is opt-in so existing mTLS
// deployments whose certificate CN is not the node_id keep working; plaintext
// or token-only connections carry no cryptographic identity and remain
// trust-on-first-use.
func verifyPeerIdentity(conn net.Conn, nodeID string, enforce bool) error {
	if !enforce {
		return nil
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	leaf := certs[0]
	if leaf.Subject.CommonName == nodeID {
		return nil
	}
	for _, name := range leaf.DNSNames {
		if name == nodeID {
			return nil
		}
	}
	return fmt.Errorf("node_id %q does not match client certificate CN/SAN", nodeID)
}

func (s *Server) handleEnvelope(client *Client, env protocol.Envelope) error {
	switch env.Type {
	case protocol.TypePing:
		client.Touch()
		// Reply so the slave can detect a half-open connection: a slave that
		// stops receiving pongs within its read deadline will reconnect.
		return client.Send(protocol.TypePong, protocol.PongMessage{SentAt: time.Now().UTC()})
	case protocol.TypeShutdownAck:
		var ack protocol.ShutdownAckMessage
		if err := protocol.DecodePayload(env.Data, &ack); err != nil {
			return err
		}
		// Bind the ack to the authenticated connection identity: a slave may
		// only report status for itself, never forge acks for other nodes.
		ack.NodeID = client.NodeID
		s.recordShutdownUpdate(ack)
		recordShutdownAck(ack.Status)
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
