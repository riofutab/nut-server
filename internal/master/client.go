package master

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"nut-server/internal/protocol"
)

var (
	HandshakeReadDeadline = 5 * time.Second
	IdleReadDeadline      = 45 * time.Second
	WriteDeadline         = 5 * time.Second
)

type Client struct {
	NodeID   string
	Hostname string
	Tags     []string
	Conn     net.Conn
	mu       sync.Mutex
	reader   *bufio.Reader
	writer   *bufio.Writer
	lastSeen atomic.Int64
}

func NewClient(conn net.Conn) *Client {
	client := &Client{
		Conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}
	client.Touch()
	return client
}

func (c *Client) Send(messageType string, payload interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_ = c.Conn.SetWriteDeadline(time.Now().Add(WriteDeadline))
	if err := protocol.WriteEnvelope(c.writer, messageType, payload); err != nil {
		return fmt.Errorf("send %s to %s: %w", messageType, c.NodeID, err)
	}
	return nil
}

func (c *Client) SetReadDeadline(timeout time.Duration) error {
	if timeout <= 0 {
		return c.Conn.SetReadDeadline(time.Time{})
	}
	return c.Conn.SetReadDeadline(time.Now().Add(timeout))
}

func (c *Client) ReadEnvelope() (protocol.Envelope, error) {
	return protocol.ReadEnvelope(c.reader)
}

func (c *Client) Touch() {
	c.lastSeen.Store(time.Now().UTC().UnixNano())
}

func (c *Client) LastSeen() time.Time {
	unixNano := c.lastSeen.Load()
	if unixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, unixNano).UTC()
}

func (c *Client) Close() error {
	return c.Conn.Close()
}
