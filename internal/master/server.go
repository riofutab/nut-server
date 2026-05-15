package master

import (
	"crypto/tls"
	"embed"
	"fmt"
	"log"
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
	commandMu           sync.Mutex
	commands            map[string]*shutdownCommandState
	activeCommand       string
	autoShutdownLatched bool
	localShutdown       *localShutdownState
	localShutdownRunner func([]string, string) error
	upsMu               sync.RWMutex
	upsStatus           *protocol.UPSStatusView
	tlsConfig           *tls.Config
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

func (s *Server) Run() error {
	listener, err := s.listen()
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.ListenAddr, err)
	}
	defer listener.Close()

	log.Printf("master listening on %s tls=%t mTLS=%t", s.cfg.ListenAddr, s.cfg.TLS.EnabledForServer(), s.cfg.TLS.RequireClientCert)
	go s.runPollingLoop()
	go s.runAdminServer()
	go s.runCommandWatcher()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept connection: %w", err)
		}
		go s.handleConn(conn)
	}
}
