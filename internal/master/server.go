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
)

//go:embed web/index.html
var webFS embed.FS

type Server struct {
	cfg                 config.MasterConfig
	registry            *Registry
	directory           *NodeDirectory
	commandSeq          atomic.Uint64
	shutdownIssued      atomic.Bool
	ready               atomic.Bool
	commandMu           sync.Mutex
	commands            map[string]*shutdownCommandState
	activeCommand       string
	autoShutdownLatched bool
	localShutdown       *localShutdownState
	localShutdownRunner func([]string, string) error
	upsMu               sync.RWMutex
	upsStatus           *protocol.UPSStatusView
	tlsConfig           *tls.Config
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
}

type localShutdownState struct {
	Phase             string     `json:"phase"`
	CommandID         string     `json:"command_id,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	DeadlineAt        *time.Time `json:"deadline_at,omitempty"`
	Trigger           string     `json:"trigger,omitempty"`
	LastRebroadcastAt *time.Time `json:"last_rebroadcast_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
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
			return fmt.Errorf("accept connection: %w", err)
		}
		s.trackConn(conn)
		connWG.Add(1)
		go func(c net.Conn) {
			defer connWG.Done()
			defer s.untrackConn(c)
			s.handleConn(c)
		}(conn)
	}
}
