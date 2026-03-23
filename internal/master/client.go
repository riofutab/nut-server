package master

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"nut-server/internal/protocol"
)

type Client struct {
	NodeID   string
	Hostname string
	Tags     []string
	Conn     net.Conn
	mu       sync.Mutex
	reader   *bufio.Reader
	writer   *bufio.Writer
}

func NewClient(conn net.Conn) *Client {
	return &Client{
		Conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}
}

func (c *Client) Send(messageType string, payload interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	envelope := protocol.Envelope{Type: messageType, Data: payload}
	if err := json.NewEncoder(c.writer).Encode(envelope); err != nil {
		return fmt.Errorf("encode %s to %s: %w", messageType, c.NodeID, err)
	}
	if err := c.writer.Flush(); err != nil {
		return fmt.Errorf("flush %s to %s: %w", messageType, c.NodeID, err)
	}
	return nil
}

func (c *Client) ReadEnvelope() (protocol.Envelope, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return protocol.Envelope{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

func (c *Client) Close() error {
	return c.Conn.Close()
}

func (c *Client) SetDeadline(timeout time.Duration) error {
	return c.Conn.SetDeadline(time.Now().Add(timeout))
}
