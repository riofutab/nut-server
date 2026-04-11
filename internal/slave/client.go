package slave

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

type Client struct {
	cfg               config.SlaveConfig
	hostname          string
	shutdown          ShutdownExecutor
	commandMu         sync.Mutex
	commandStates     map[string]protocol.ShutdownAckMessage
	executions        map[string]*shutdownExecution
	tlsConfig         *tls.Config
}

type shutdownExecution struct {
	session *connectionSession
}

type connectionSession struct {
	reader *bufio.Reader
	writer *bufio.Writer
	mu     sync.Mutex
}

func NewClient(cfg config.SlaveConfig) *Client {
	hostname, _ := os.Hostname()
	client := &Client{
		cfg:           cfg,
		hostname:      hostname,
		shutdown:      CommandShutdownExecutor{Command: cfg.ShutdownCommand, DryRun: cfg.DryRun},
		commandStates: make(map[string]protocol.ShutdownAckMessage),
		executions:    make(map[string]*shutdownExecution),
	}
	client.loadState()
	return client
}

func (c *Client) Run() error {
	for {
		if err := c.runOnce(); err != nil {
			log.Printf("slave connection ended: %v", err)
		}
		time.Sleep(c.cfg.ReconnectInterval.Duration)
	}
}

func (c *Client) dial() (net.Conn, error) {
	if !c.cfg.TLS.EnabledForClient() {
		return net.Dial("tcp", c.cfg.MasterAddr)
	}
	if c.tlsConfig == nil {
		tlsConfig, err := c.cfg.TLS.ClientTLSConfig()
		if err != nil {
			return nil, err
		}
		c.tlsConfig = tlsConfig
	}
	return tls.Dial("tcp", c.cfg.MasterAddr, c.tlsConfig)
}

func (c *Client) runOnce() error {
	conn, err := c.dial()
	if err != nil {
		return fmt.Errorf("dial master %s: %w", c.cfg.MasterAddr, err)
	}
	defer conn.Close()

	session := newConnectionSession(conn)

	if err := session.Send(protocol.TypeRegister, protocol.RegisterMessage{
		NodeID:   c.cfg.NodeID,
		Hostname: c.hostname,
		Token:    c.cfg.Token,
		Tags:     c.cfg.Tags,
	}); err != nil {
		return fmt.Errorf("send register: %w", err)
	}

	if err := c.expectRegisterAck(session.reader); err != nil {
		return err
	}
	log.Printf("registered to master %s as %s tags=%v tls=%t", c.cfg.MasterAddr, c.cfg.NodeID, c.cfg.Tags, c.cfg.TLS.EnabledForClient())

	go c.runPingLoop(session)

	for {
		env, err := readEnvelope(session.reader)
		if err != nil {
			return err
		}
		if err := c.handleEnvelope(env, session); err != nil {
			return err
		}
	}
}

func (c *Client) expectRegisterAck(reader *bufio.Reader) error {
	env, err := readEnvelope(reader)
	if err != nil {
		return fmt.Errorf("read register ack: %w", err)
	}
	if env.Type != protocol.TypeRegisterAck {
		return fmt.Errorf("expected register_ack, got %s", env.Type)
	}
	var ack protocol.RegisterAckMessage
	if err := decodePayload(env.Data, &ack); err != nil {
		return err
	}
	if !ack.Accepted {
		return fmt.Errorf("register rejected: %s", ack.Message)
	}
	return nil
}

func (c *Client) handleEnvelope(env protocol.Envelope, session *connectionSession) error {
	switch env.Type {
	case protocol.TypeShutdown:
		var shutdown protocol.ShutdownMessage
		if err := decodePayload(env.Data, &shutdown); err != nil {
			return err
		}
		if existing, ok := c.getCommandState(shutdown.CommandID); ok {
			shouldStartExecution := false
			if existing.Status == protocol.ShutdownStatusExecuting {
				shouldStartExecution = c.beginOrRebindShutdownExecution(shutdown.CommandID, session)
			}
			if err := session.Send(protocol.TypeShutdownAck, existing); err != nil {
				return err
			}
			switch existing.Status {
			case protocol.ShutdownStatusAccepted:
				return c.resumeShutdown(shutdown, session)
			case protocol.ShutdownStatusExecuting:
				if !shouldStartExecution {
					return nil
				}
				go c.executeShutdown(shutdown.CommandID, shutdown)
				return nil
			default:
				return nil
			}
		}

		accepted := protocol.ShutdownAckMessage{
			CommandID: shutdown.CommandID,
			NodeID:    c.cfg.NodeID,
			Status:    protocol.ShutdownStatusAccepted,
			Message:   "shutdown command accepted",
			AckedAt:   time.Now().UTC(),
		}
		c.setCommandState(accepted)
		if err := session.Send(protocol.TypeShutdownAck, accepted); err != nil {
			return err
		}
		return c.resumeShutdown(shutdown, session)
	case protocol.TypeError:
		var msg protocol.ErrorMessage
		if err := decodePayload(env.Data, &msg); err != nil {
			return err
		}
		return fmt.Errorf("master error: %s", msg.Message)
	default:
		return nil
	}
}

func (c *Client) resumeShutdown(shutdown protocol.ShutdownMessage, session *connectionSession) error {
	if shutdown.DryRun || c.cfg.DryRun {
		log.Printf("dry-run shutdown command received node_id=%s command_id=%s reason=%s", c.cfg.NodeID, shutdown.CommandID, shutdown.Reason)
		executed := protocol.ShutdownAckMessage{
			CommandID: shutdown.CommandID,
			NodeID:    c.cfg.NodeID,
			Status:    protocol.ShutdownStatusExecuted,
			Message:   "dry-run shutdown simulated",
			AckedAt:   time.Now().UTC(),
		}
		c.setCommandState(executed)
		return session.Send(protocol.TypeShutdownAck, executed)
	}

	executing := protocol.ShutdownAckMessage{
		CommandID: shutdown.CommandID,
		NodeID:    c.cfg.NodeID,
		Status:    protocol.ShutdownStatusExecuting,
		Message:   "executing shutdown command",
		AckedAt:   time.Now().UTC(),
	}
	c.setCommandState(executing)
	if err := session.Send(protocol.TypeShutdownAck, executing); err != nil {
		return err
	}
	if !c.beginOrRebindShutdownExecution(shutdown.CommandID, session) {
		return nil
	}
	go c.executeShutdown(shutdown.CommandID, shutdown)
	return nil
}

func (c *Client) runPingLoop(session *connectionSession) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := session.Send(protocol.TypePing, protocol.PingMessage{SentAt: time.Now().UTC()}); err != nil {
			return
		}
	}
}

func (c *Client) executeShutdown(commandID string, shutdown protocol.ShutdownMessage) {
	defer c.finishShutdownExecution(commandID)

	update := protocol.ShutdownAckMessage{
		CommandID: shutdown.CommandID,
		NodeID:    c.cfg.NodeID,
		Status:    protocol.ShutdownStatusExecuted,
		Message:   "shutdown command completed",
		AckedAt:   time.Now().UTC(),
	}
	if err := c.shutdown.Execute(log.Default()); err != nil {
		update.Status = protocol.ShutdownStatusFailed
		update.Message = err.Error()
		update.AckedAt = time.Now().UTC()
	}
	c.setCommandState(update)
	session := c.shutdownExecutionSession(commandID)
	if session == nil {
		return
	}
	if err := session.Send(protocol.TypeShutdownAck, update); err != nil {
		log.Printf("report shutdown status failed node_id=%s command_id=%s status=%s: %v", c.cfg.NodeID, shutdown.CommandID, update.Status, err)
	}
}

func (c *Client) getCommandState(commandID string) (protocol.ShutdownAckMessage, bool) {
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	state, ok := c.commandStates[commandID]
	return state, ok
}

func (c *Client) setCommandState(update protocol.ShutdownAckMessage) {
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	c.commandStates[update.CommandID] = update
	c.saveStateLocked()
}

func (c *Client) beginOrRebindShutdownExecution(commandID string, session *connectionSession) bool {
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	if execution, ok := c.executions[commandID]; ok {
		execution.session = session
		return false
	}
	c.executions[commandID] = &shutdownExecution{session: session}
	return true
}

func (c *Client) finishShutdownExecution(commandID string) {
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	delete(c.executions, commandID)
}

func (c *Client) shutdownExecutionSession(commandID string) *connectionSession {
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	if execution := c.executions[commandID]; execution != nil {
		return execution.session
	}
	return nil
}

func readEnvelope(reader *bufio.Reader) (protocol.Envelope, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return protocol.Envelope{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return protocol.Envelope{}, err
	}
	return env, nil
}

func decodePayload(data interface{}, dst interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, dst)
}

func newConnectionSession(conn net.Conn) *connectionSession {
	return &connectionSession{
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}
}

func (s *connectionSession) Send(messageType string, payload interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	envelope := protocol.Envelope{Type: messageType, Data: payload}
	if err := json.NewEncoder(s.writer).Encode(envelope); err != nil {
		return err
	}
	return s.writer.Flush()
}
