package slave

import (
	"bufio"
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
	cfg             config.SlaveConfig
	hostname        string
	shutdown        ShutdownExecutor
	executedMu      sync.Mutex
	executedCommand map[string]struct{}
}

func NewClient(cfg config.SlaveConfig) *Client {
	hostname, _ := os.Hostname()
	return &Client{
		cfg:             cfg,
		hostname:        hostname,
		shutdown:        ShutdownExecutor{Command: cfg.ShutdownCommand, DryRun: cfg.DryRun},
		executedCommand: make(map[string]struct{}),
	}
}

func (c *Client) Run() error {
	for {
		if err := c.runOnce(); err != nil {
			log.Printf("slave connection ended: %v", err)
		}
		time.Sleep(c.cfg.ReconnectInterval.Duration)
	}
}

func (c *Client) runOnce() error {
	conn, err := net.Dial("tcp", c.cfg.MasterAddr)
	if err != nil {
		return fmt.Errorf("dial master %s: %w", c.cfg.MasterAddr, err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	encoder := json.NewEncoder(writer)

	register := protocol.Envelope{
		Type: protocol.TypeRegister,
		Data: protocol.RegisterMessage{
			NodeID:   c.cfg.NodeID,
			Hostname: c.hostname,
			Token:    c.cfg.Token,
		},
	}
	if err := encoder.Encode(register); err != nil {
		return fmt.Errorf("send register: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush register: %w", err)
	}

	if err := c.expectRegisterAck(reader); err != nil {
		return err
	}
	log.Printf("registered to master %s as %s", c.cfg.MasterAddr, c.cfg.NodeID)

	go c.runPingLoop(writer)

	for {
		env, err := readEnvelope(reader)
		if err != nil {
			return err
		}
		if err := c.handleEnvelope(env, encoder, writer); err != nil {
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

func (c *Client) handleEnvelope(env protocol.Envelope, encoder *json.Encoder, writer *bufio.Writer) error {
	switch env.Type {
	case protocol.TypeShutdown:
		var shutdown protocol.ShutdownMessage
		if err := decodePayload(env.Data, &shutdown); err != nil {
			return err
		}
		ack := protocol.ShutdownAckMessage{
			CommandID: shutdown.CommandID,
			NodeID:    c.cfg.NodeID,
			Accepted:  true,
			Message:   "shutdown accepted",
			AckedAt:   time.Now().UTC(),
		}
		if shutdown.DryRun || c.cfg.DryRun {
			ack.Message = "dry-run shutdown acknowledged"
			log.Printf("dry-run shutdown command received node_id=%s command_id=%s reason=%s", c.cfg.NodeID, shutdown.CommandID, shutdown.Reason)
		} else if c.alreadyExecuted(shutdown.CommandID) {
			ack.Message = "duplicate shutdown command ignored"
		} else {
			c.markExecuted(shutdown.CommandID)
			go func() {
				if err := c.shutdown.Execute(log.Default()); err != nil {
					log.Printf("execute shutdown command failed: %v", err)
				}
			}()
		}
		if err := encoder.Encode(protocol.Envelope{Type: protocol.TypeShutdownAck, Data: ack}); err != nil {
			return err
		}
		return writer.Flush()
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

func (c *Client) runPingLoop(writer *bufio.Writer) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	encoder := json.NewEncoder(writer)

	for range ticker.C {
		if err := encoder.Encode(protocol.Envelope{Type: protocol.TypePing, Data: protocol.PingMessage{SentAt: time.Now().UTC()}}); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func (c *Client) alreadyExecuted(commandID string) bool {
	c.executedMu.Lock()
	defer c.executedMu.Unlock()
	_, ok := c.executedCommand[commandID]
	return ok
}

func (c *Client) markExecuted(commandID string) {
	c.executedMu.Lock()
	defer c.executedMu.Unlock()
	c.executedCommand[commandID] = struct{}{}
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
