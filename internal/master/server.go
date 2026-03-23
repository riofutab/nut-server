package master

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
	"nut-server/internal/security"
)

type Server struct {
	cfg              config.MasterConfig
	registry         *Registry
	commandSeq       atomic.Uint64
	shutdownIssued   atomic.Bool
	shutdownIssuedMu sync.Mutex
}

func NewServer(cfg config.MasterConfig) *Server {
	return &Server{
		cfg:      cfg,
		registry: NewRegistry(),
	}
}

func (s *Server) Run() error {
	listener, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.ListenAddr, err)
	}
	defer listener.Close()

	log.Printf("master listening on %s", s.cfg.ListenAddr)
	go s.runPollingLoop()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept connection: %w", err)
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	client := NewClient(conn)
	defer func() {
		if client.NodeID != "" {
			s.registry.Remove(client.NodeID)
		}
		_ = client.Close()
	}()

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
	s.registry.Set(client)

	if err := client.Send(protocol.TypeRegisterAck, protocol.RegisterAckMessage{Accepted: true, Message: "registered"}); err != nil {
		log.Printf("ack register to %s failed: %v", client.NodeID, err)
		return
	}
	log.Printf("slave registered node_id=%s hostname=%s", client.NodeID, client.Hostname)

	for {
		env, err := client.ReadEnvelope()
		if err != nil {
			if err != io.EOF {
				log.Printf("read from %s failed: %v", client.NodeID, err)
			}
			return
		}
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
		log.Printf("shutdown ack node_id=%s command_id=%s accepted=%t message=%s", ack.NodeID, ack.CommandID, ack.Accepted, ack.Message)
		return nil
	default:
		return fmt.Errorf("unsupported message type %s", env.Type)
	}
}

func (s *Server) runPollingLoop() {
	ticker := time.NewTicker(s.cfg.PollInterval.Duration)
	defer ticker.Stop()

	for {
		if err := s.evaluateUPS(); err != nil {
			log.Printf("poll UPS failed: %v", err)
		}
		<-ticker.C
	}
}

func (s *Server) evaluateUPS() error {
	status, err := ReadUPSStatus(s.cfg.SNMP)
	if err != nil {
		return err
	}
	if ShouldShutdown(status, s.cfg.ShutdownPolicy) {
		if s.shutdownIssued.CompareAndSwap(false, true) {
			commandID := fmt.Sprintf("shutdown-%d", s.commandSeq.Add(1))
			if s.cfg.DryRun {
				log.Printf("UPS policy triggered in dry-run mode, broadcasting shutdown command_id=%s", commandID)
			} else {
				log.Printf("UPS policy triggered, broadcasting shutdown command_id=%s", commandID)
			}
			return s.BroadcastShutdown(commandID, s.cfg.ShutdownPolicy.ShutdownReason)
		}
	}
	return nil
}

func (s *Server) BroadcastShutdown(commandID, reason string) error {
	message := protocol.ShutdownMessage{
		CommandID: commandID,
		Reason:    reason,
		DryRun:    s.cfg.DryRun,
		IssuedAt:  time.Now().UTC(),
	}
	var firstErr error
	for _, client := range s.registry.List() {
		if err := client.Send(protocol.TypeShutdown, message); err != nil {
			log.Printf("send shutdown to %s failed: %v", client.NodeID, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if s.cfg.DryRun {
			log.Printf("sent dry-run shutdown to node_id=%s command_id=%s", client.NodeID, commandID)
		} else {
			log.Printf("sent shutdown to node_id=%s command_id=%s", client.NodeID, commandID)
		}
	}
	return firstErr
}
