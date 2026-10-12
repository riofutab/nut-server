package master

import (
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
	"nut-server/internal/shutdowncheck"
)

//go:embed web/index.html
var webFS embed.FS

// Accept retry backoff for transient listener errors (e.g. EMFILE), mirroring
// net/http.Server: a resource blip must not take the master down mid-outage.
const (
	minAcceptRetryDelay = 5 * time.Millisecond
	maxAcceptRetryDelay = 1 * time.Second
)

type Server struct {
	cfg            config.MasterConfig
	registry       *Registry
	directory      *NodeDirectory
	shutdownIssued atomic.Bool
	ready          atomic.Bool
	// upsConfirmed is set by the first successful UPS poll of this process.
	// Auto shutdown activity restored from the state file waits for it.
	upsConfirmed        atomic.Bool
	commandMu           sync.Mutex
	commands            map[string]*shutdownCommandState
	activeCommand       string
	episode             *autoEpisode
	lastNodeRetryAt     time.Time
	localShutdown       *localShutdownState
	localShutdownRunner func(command []string, trigger string, dryRun bool) error
	upsReader           func(config.SNMPConfig) (UPSStatus, error)
	checkShutdown       func(ctx context.Context, argv []string) error
	// localCommandReady is the startup self-check result of the local shutdown
	// command (nil = not checked). Written in Run before the admin server
	// starts, read-only afterwards.
	localCommandReady *bool
	upsMu             sync.RWMutex
	upsStatus         *protocol.UPSStatusView
	tlsConfig         *tls.Config
	// State persistence. persistSeq is bumped under commandMu when a snapshot is
	// taken; the disk write runs under persistMu (NOT commandMu) so the fsync no
	// longer blocks readers or the command-watcher tick. persistedSeq guards
	// against a slow writer clobbering a newer snapshot.
	persistMu    sync.Mutex
	persistSeq   uint64
	persistedSeq uint64
	// Active connection handlers. Run drains these on shutdown so a handler's
	// state write (e.g. saveStateForDirectoryChange on register) cannot land
	// after Run returns.
	connsMu sync.Mutex
	conns   map[net.Conn]struct{}
}

type shutdownCommandState struct {
	Message        protocol.ShutdownMessage
	TargetNodes    map[string]struct{}
	NodeUpdates    map[string]protocol.ShutdownAckMessage
	TimeoutAt      *time.Time
	CompletedAt    *time.Time
	ReplayDisabled bool `json:"replay_disabled,omitempty"`
	// Auto marks the episode command created by shutdown policies. Unlike a
	// manual command it stays active until line power returns, so failed nodes
	// keep being retried and later policies can widen it.
	Auto bool `json:"auto,omitempty"`
}

type localShutdownState struct {
	Phase             string     `json:"phase"`
	CommandID         string     `json:"command_id,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	DeadlineAt        *time.Time `json:"deadline_at,omitempty"`
	Trigger           string     `json:"trigger,omitempty"`
	LastRebroadcastAt *time.Time `json:"last_rebroadcast_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	LastAttemptAt     *time.Time `json:"last_attempt_at,omitempty"`
	// DryRun follows the command that armed the local shutdown, so a dry-run
	// command never powers off the master even when the config is not dry-run.
	DryRun bool `json:"dry_run,omitempty"`
}

func NewServer(cfg config.MasterConfig) *Server {
	server := &Server{
		cfg:       cfg,
		registry:  NewRegistry(),
		directory: NewNodeDirectory(),
		commands:  make(map[string]*shutdownCommandState),
		conns:     make(map[net.Conn]struct{}),
	}
	server.localShutdownRunner = server.runLocalShutdownCommand
	server.upsReader = ReadUPSStatus
	server.checkShutdown = func(ctx context.Context, argv []string) error {
		return shutdowncheck.Check(ctx, argv, nil)
	}
	if cfg.SNMP.Target != "" {
		server.upsStatus = &protocol.UPSStatusView{Target: cfg.SNMP.Target}
	}
	server.loadState()
	return server
}

func (s *Server) listen() (net.Listener, error) {
	if !s.cfg.TLS.EnabledForServer() {
		return net.Listen("tcp", s.cfg.ListenAddr)
	}
	if s.tlsConfig == nil {
		tlsConfig, err := s.cfg.TLS.ServerTLSConfig()
		if err != nil {
			return nil, err
		}
		s.tlsConfig = tlsConfig
	}
	return tls.Listen("tcp", s.cfg.ListenAddr, s.tlsConfig)
}

func (s *Server) Run(ctx context.Context) error {
	s.runLocalShutdownSelfCheck(ctx)
	listener, err := s.listen()
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.ListenAddr, err)
	}
	defer listener.Close()
	s.ready.Store(true)
	defer s.ready.Store(false)

	slog.Info("master listening",
		"addr", s.cfg.ListenAddr,
		"tls", s.cfg.TLS.EnabledForServer(),
		"mtls", s.cfg.TLS.RequireClientCert)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.runPollingLoop(ctx) }()
	go func() { defer wg.Done(); s.runAdminServer(ctx) }()
	go func() { defer wg.Done(); s.runCommandWatcher(ctx) }()

	var connWG sync.WaitGroup
	var acceptDelay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				slog.Info("master shutting down", "reason", ctx.Err())
				// Force-close in-flight connections so their handlers unblock
				// from ReadEnvelope, then wait for any pending state writes to
				// flush before returning (and before callers tear down dirs).
				s.closeActiveConns()
				connWG.Wait()
				wg.Wait()
				return nil
			}
			acceptDelay = nextAcceptRetryDelay(acceptDelay)
			slog.Error("accept connection failed, retrying", "err", err, "delay", acceptDelay)
			select {
			case <-ctx.Done():
			case <-time.After(acceptDelay):
			}
			continue
		}
		acceptDelay = 0
		s.trackConn(conn)
		connWG.Add(1)
		go func(c net.Conn) {
			defer connWG.Done()
			defer s.untrackConn(c)
			s.handleConn(c)
		}(conn)
	}
}

func nextAcceptRetryDelay(current time.Duration) time.Duration {
	if current == 0 {
		return minAcceptRetryDelay
	}
	return min(current*2, maxAcceptRetryDelay)
}

// runLocalShutdownSelfCheck verifies the local shutdown command could run when
// the master may have to power itself off. A failure is reported (log and
// /status), never fatal: exiting would let systemd restart-loop the master and
// stop it from shutting down the slaves.
func (s *Server) runLocalShutdownSelfCheck(ctx context.Context) {
	if !s.cfg.LocalShutdown.Enabled || s.cfg.DryRun {
		return
	}
	ready := true
	if err := s.checkShutdown(ctx, s.cfg.LocalShutdown.Command); err != nil {
		ready = false
		slog.Error("shutdown command self-check failed", "err", err, "command", s.cfg.LocalShutdown.Command)
	} else {
		slog.Info("shutdown command self-check passed", "command", s.cfg.LocalShutdown.Command)
	}
	s.localCommandReady = &ready
}
