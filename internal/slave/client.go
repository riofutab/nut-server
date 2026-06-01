package slave

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

const (
	registerAckReadDeadline = 10 * time.Second
	writeDeadline           = 5 * time.Second
	pingInterval            = 15 * time.Second
	maxReconnectInterval    = 60 * time.Second
	backoffJitterFactor     = 0.2
	// readIdleTimeout bounds the steady-state read so a half-open master
	// connection (no RST, e.g. master host crash) is detected: the master pongs
	// every ping, so missing reads for longer than this means the link is dead.
	// Must exceed pingInterval so a healthy connection never times out.
	readIdleTimeout = 3 * pingInterval
)

type Client struct {
	cfg           config.SlaveConfig
	hostname      string
	shutdown      ShutdownExecutor
	commandMu     sync.Mutex
	commandStates map[string]protocol.ShutdownAckMessage
	executions    map[string]*shutdownExecution
	tlsConfig     *tls.Config
}

type shutdownExecution struct {
	session *connectionSession
}

type connectionSession struct {
	conn   net.Conn
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

func (c *Client) Run(ctx context.Context) error {
	if c.cfg.MetricsListenAddr != "" {
		go c.runMetricsServer(ctx)
	}
	delay := c.cfg.ReconnectInterval.Duration
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		// Reset the backoff once a connection reaches steady state (successful
		// register), not on runOnce's return: runOnce only ever returns an
		// error, so the old err==nil reset was dead code and the delay grew
		// monotonically to the cap over the slave's lifetime.
		resetBackoff := func() { delay = c.cfg.ReconnectInterval.Duration }
		if err := c.runOnce(ctx, resetBackoff); err != nil {
			slog.Warn("slave connection ended", "master", c.cfg.MasterAddr, "node_id", c.cfg.NodeID, "err", err)
		}
		if !sleepWithContext(ctx, jitterDuration(delay)) {
			return nil
		}
		delay = nextBackoff(delay)
	}
}

func (c *Client) runMetricsServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(slavePromRegistry(), promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: c.cfg.MetricsListenAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info("metrics server starting", "addr", c.cfg.MetricsListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("metrics server stopped", "err", err)
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next <= 0 || next > maxReconnectInterval {
		return maxReconnectInterval
	}
	return next
}

func jitterDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	delta := float64(d) * backoffJitterFactor
	offset := (rand.Float64()*2 - 1) * delta
	result := time.Duration(float64(d) + offset)
	if result < 0 {
		return 0
	}
	return result
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

func (c *Client) runOnce(parentCtx context.Context, onRegistered func()) error {
	conn, err := c.dial()
	if err != nil {
		recordConnectAttempt("dial_error")
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
		recordConnectAttempt("register_error")
		return fmt.Errorf("send register: %w", err)
	}

	session.setReadDeadline(registerAckReadDeadline)
	if err := c.expectRegisterAck(session.reader); err != nil {
		recordConnectAttempt("register_error")
		return err
	}
	recordConnectAttempt("success")
	setConnected(true)
	defer setConnected(false)
	onRegistered()
	slog.Info("registered to master",
		"master", c.cfg.MasterAddr,
		"node_id", c.cfg.NodeID,
		"tags", c.cfg.Tags,
		"tls", c.cfg.TLS.EnabledForClient())

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	go c.runPingLoop(ctx, session, conn)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		session.setReadDeadline(readIdleTimeout)
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
			case protocol.ShutdownStatusAccepted, protocol.ShutdownStatusFailed:
				// Retry a previously-failed shutdown when the master replays it,
				// so a transient failure does not leave the node powered on.
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

		recordShutdownReceived()
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
	case protocol.TypePong:
		// Liveness reply to our ping; receiving it refreshes the read deadline.
		return nil
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
		slog.Info("dry-run shutdown received",
			"command_id", shutdown.CommandID,
			"node_id", c.cfg.NodeID,
			"reason", shutdown.Reason)
		executed := protocol.ShutdownAckMessage{
			CommandID: shutdown.CommandID,
			NodeID:    c.cfg.NodeID,
			Status:    protocol.ShutdownStatusExecuted,
			Message:   "dry-run shutdown simulated",
			AckedAt:   time.Now().UTC(),
		}
		c.setCommandState(executed)
		recordShutdownStatus(executed.Status)
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
	recordShutdownStatus(executing.Status)
	if err := session.Send(protocol.TypeShutdownAck, executing); err != nil {
		return err
	}
	if !c.beginOrRebindShutdownExecution(shutdown.CommandID, session) {
		return nil
	}
	go c.executeShutdown(shutdown.CommandID, shutdown)
	return nil
}

func (c *Client) runPingLoop(ctx context.Context, session *connectionSession, conn net.Conn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := session.Send(protocol.TypePing, protocol.PingMessage{SentAt: time.Now().UTC()}); err != nil {
				_ = conn.Close()
				return
			}
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
	if err := c.shutdown.Execute(slog.Default()); err != nil {
		update.Status = protocol.ShutdownStatusFailed
		update.Message = err.Error()
		update.AckedAt = time.Now().UTC()
	}
	c.setCommandState(update)
	recordShutdownStatus(update.Status)
	session := c.shutdownExecutionSession(commandID)
	if session == nil {
		return
	}
	if err := session.Send(protocol.TypeShutdownAck, update); err != nil {
		slog.Error("report shutdown status failed",
			"command_id", shutdown.CommandID,
			"node_id", c.cfg.NodeID,
			"status", update.Status,
			"err", err)
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
	return protocol.ReadEnvelope(reader)
}

func decodePayload(data interface{}, dst interface{}) error {
	return protocol.DecodePayload(data, dst)
}

func newConnectionSession(conn net.Conn) *connectionSession {
	return &connectionSession{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}
}

func (s *connectionSession) Send(messageType string, payload interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		_ = s.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	}
	envelope := protocol.Envelope{Type: messageType, Data: payload}
	if err := json.NewEncoder(s.writer).Encode(envelope); err != nil {
		return err
	}
	return s.writer.Flush()
}

func (s *connectionSession) setReadDeadline(timeout time.Duration) {
	if s.conn == nil {
		return
	}
	if timeout <= 0 {
		_ = s.conn.SetReadDeadline(time.Time{})
		return
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(timeout))
}
