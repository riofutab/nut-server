package slave

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
	"nut-server/internal/shutdowncheck"
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
	// defaultDialTimeout bounds TCP connect plus TLS handshake, so a master
	// that accepts but never completes the handshake cannot stall reconnects.
	defaultDialTimeout       = 10 * time.Second
	metricsReadHeaderTimeout = 10 * time.Second
	// maxRetainedCommandStates matches the master's maxRetainedCompletedCommands
	// so every command the master may still replay keeps its slave-side state.
	maxRetainedCommandStates = 200
	// registerRejectInvalidToken is the master's register_ack message for a
	// token it does not accept for this node.
	registerRejectInvalidToken = "invalid token"
)

// dialTimeout is a variable only so tests can shorten it.
var dialTimeout = defaultDialTimeout

type Client struct {
	cfg           config.SlaveConfig
	hostname      string
	shutdown      ShutdownExecutor
	checkShutdown func(ctx context.Context, argv []string) error
	// shutdownReady is the startup self-check result; nil when skipped (dry-run).
	shutdownReady *bool
	commandMu     sync.Mutex
	commandStates map[string]protocol.ShutdownAckMessage
	// issuedToken is guarded by commandMu because it is persisted with the
	// command states.
	issuedToken string
	executions  map[string]*shutdownExecution
	tlsConfig   *tls.Config
	// State persistence, mirroring the master: snapshot under commandMu, write
	// under persistMu so the fsync never blocks command handling.
	persistMu    sync.Mutex
	persistSeq   uint64
	persistedSeq uint64
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
		checkShutdown: defaultShutdownCheck,
		commandStates: make(map[string]protocol.ShutdownAckMessage),
		executions:    make(map[string]*shutdownExecution),
	}
	client.loadState()
	return client
}

func defaultShutdownCheck(ctx context.Context, argv []string) error {
	return shutdowncheck.Check(ctx, argv, nil)
}

func (c *Client) Run(ctx context.Context) error {
	c.runShutdownSelfCheck(ctx)
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

// runShutdownSelfCheck verifies the shutdown command could run. A failure is
// only reported (log, metric, register message), never fatal: exiting would let
// systemd restart-loop the slave and hide the problem.
func (c *Client) runShutdownSelfCheck(ctx context.Context) {
	if c.cfg.DryRun {
		return
	}
	ready := true
	if err := c.checkShutdown(ctx, c.cfg.ShutdownCommand); err != nil {
		ready = false
		slog.Error("shutdown command self-check failed", "err", err, "command", c.cfg.ShutdownCommand)
	} else {
		slog.Info("shutdown command self-check passed", "command", c.cfg.ShutdownCommand)
	}
	c.shutdownReady = &ready
	setShutdownReady(ready)
}

func (c *Client) runMetricsServer(ctx context.Context) {
	srv := newMetricsServer(c.cfg.MetricsListenAddr)
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

func newMetricsServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(slavePromRegistry(), promhttp.HandlerOpts{}))
	// Liveness: process is up. Readiness: the slave currently holds a registered
	// connection to the master. Both unauthenticated, mirroring the master.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if slaveConnected.Load() {
			writePlain(w, http.StatusOK, "ready")
			return
		}
		writePlain(w, http.StatusServiceUnavailable, "not ready")
	})
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: metricsReadHeaderTimeout}
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
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
	dialer := &net.Dialer{Timeout: dialTimeout}
	if !c.cfg.TLS.EnabledForClient() {
		return dialer.Dial("tcp", c.cfg.MasterAddr)
	}
	if c.tlsConfig == nil {
		tlsConfig, err := c.cfg.TLS.ClientTLSConfig()
		if err != nil {
			return nil, err
		}
		c.tlsConfig = tlsConfig
	}
	// tls.Dialer applies NetDialer.Timeout to the handshake as well as the connect.
	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: c.tlsConfig}
	return tlsDialer.Dial("tcp", c.cfg.MasterAddr)
}

func (c *Client) runOnce(parentCtx context.Context, onRegistered func()) error {
	conn, err := c.dial()
	if err != nil {
		recordConnectAttempt("dial_error")
		return fmt.Errorf("dial master %s: %w", c.cfg.MasterAddr, err)
	}
	defer conn.Close()

	session := newConnectionSession(conn)

	token, usedIssuedToken := c.registerToken()
	if err := session.Send(protocol.TypeRegister, protocol.RegisterMessage{
		NodeID:        c.cfg.NodeID,
		Hostname:      c.hostname,
		Token:         token,
		Tags:          c.cfg.Tags,
		DryRun:        c.cfg.DryRun,
		ShutdownReady: c.shutdownReady,
	}); err != nil {
		recordConnectAttempt("register_error")
		return fmt.Errorf("send register: %w", err)
	}

	session.setReadDeadline(registerAckReadDeadline)
	ack, err := c.expectRegisterAck(session.reader)
	if err != nil {
		recordConnectAttempt("register_error")
		return err
	}
	if !ack.Accepted {
		recordConnectAttempt("register_error")
		if usedIssuedToken && ack.Message == registerRejectInvalidToken {
			// The master no longer knows the issued token (revoked, or its
			// state was lost): enroll again with the config token next time.
			slog.Warn("master rejected the issued token; falling back to the config token", "node_id", c.cfg.NodeID)
			if err := c.setIssuedToken(""); err != nil {
				slog.Error("clear issued token failed", "node_id", c.cfg.NodeID, "err", err)
			}
		}
		return fmt.Errorf("register rejected: %s", ack.Message)
	}
	if ack.IssuedToken != "" {
		// Persist before confirming: the master binds the node to the token on
		// token_stored, so it must never bind a token we could lose.
		if err := c.setIssuedToken(ack.IssuedToken); err != nil {
			recordConnectAttempt("register_error")
			return fmt.Errorf("store issued token: %w", err)
		}
		if err := session.Send(protocol.TypeTokenStored, nil); err != nil {
			recordConnectAttempt("register_error")
			return fmt.Errorf("confirm issued token: %w", err)
		}
		slog.Info("enrolled with master; stored issued token", "node_id", c.cfg.NodeID, "state_file", c.cfg.StateFile)
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
		env, err := protocol.ReadEnvelope(session.reader)
		if err != nil {
			return err
		}
		if err := c.handleEnvelope(env, session); err != nil {
			return err
		}
	}
}

func (c *Client) expectRegisterAck(reader *bufio.Reader) (protocol.RegisterAckMessage, error) {
	env, err := protocol.ReadEnvelope(reader)
	if err != nil {
		return protocol.RegisterAckMessage{}, fmt.Errorf("read register ack: %w", err)
	}
	if env.Type != protocol.TypeRegisterAck {
		return protocol.RegisterAckMessage{}, fmt.Errorf("expected register_ack, got %s", env.Type)
	}
	var ack protocol.RegisterAckMessage
	if err := protocol.DecodePayload(env.Data, &ack); err != nil {
		return protocol.RegisterAckMessage{}, err
	}
	return ack, nil
}

// ackMessage builds a shutdown ack for this node; NodeID and AckedAt are
// identical across every call site, so centralizing them avoids drift.
func (c *Client) ackMessage(commandID, status, message string) protocol.ShutdownAckMessage {
	return protocol.ShutdownAckMessage{
		CommandID: commandID,
		NodeID:    c.cfg.NodeID,
		Status:    status,
		Message:   message,
		AckedAt:   time.Now().UTC(),
	}
}

func (c *Client) handleEnvelope(env protocol.Envelope, session *connectionSession) error {
	switch env.Type {
	case protocol.TypeShutdown:
		var shutdown protocol.ShutdownMessage
		if err := protocol.DecodePayload(env.Data, &shutdown); err != nil {
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
		accepted := c.ackMessage(shutdown.CommandID, protocol.ShutdownStatusAccepted, "shutdown command accepted")
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
		if err := protocol.DecodePayload(env.Data, &msg); err != nil {
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
		simulated := c.ackMessage(shutdown.CommandID, protocol.ShutdownStatusSimulated, "dry-run shutdown simulated")
		c.setCommandState(simulated)
		recordShutdownStatus(simulated.Status)
		return session.Send(protocol.TypeShutdownAck, simulated)
	}

	executing := c.ackMessage(shutdown.CommandID, protocol.ShutdownStatusExecuting, "executing shutdown command")
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

	update := c.ackMessage(shutdown.CommandID, protocol.ShutdownStatusExecuted, "shutdown command completed")
	ctx := context.Background()
	if c.cfg.ShutdownCommandTimeout.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.ShutdownCommandTimeout.Duration)
		defer cancel()
	}
	if err := c.shutdown.Execute(ctx, slog.Default()); err != nil {
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
	c.commandStates[update.CommandID] = update
	c.pruneCommandStatesLocked()
	seq, content := c.snapshotStateLocked()
	c.commandMu.Unlock()
	_ = c.persistState(seq, content)
}

// pruneCommandStatesLocked drops the oldest states (by AckedAt) beyond
// maxRetainedCommandStates. Commands with an in-flight execution are never
// dropped, so their final ack still has a state to update.
func (c *Client) pruneCommandStatesLocked() {
	excess := len(c.commandStates) - maxRetainedCommandStates
	if excess <= 0 {
		return
	}
	candidates := make([]string, 0, len(c.commandStates))
	for id := range c.commandStates {
		if _, running := c.executions[id]; !running {
			candidates = append(candidates, id)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return c.commandStates[candidates[i]].AckedAt.Before(c.commandStates[candidates[j]].AckedAt)
	})
	for i := 0; i < excess && i < len(candidates); i++ {
		delete(c.commandStates, candidates[i])
	}
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
	return protocol.WriteEnvelope(s.writer, messageType, payload)
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
