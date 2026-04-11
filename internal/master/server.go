package master

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
	"nut-server/internal/security"
)

type Server struct {
	cfg            config.MasterConfig
	registry       *Registry
	commandSeq     atomic.Uint64
	shutdownIssued atomic.Bool
	commandMu      sync.Mutex
	commands       map[string]*shutdownCommandState
	activeCommand  string
	tlsConfig      *tls.Config
}

type shutdownCommandState struct {
	Message     protocol.ShutdownMessage
	TargetNodes map[string]struct{}
	NodeUpdates map[string]protocol.ShutdownAckMessage
	TimeoutAt   *time.Time
	CompletedAt *time.Time
}

func NewServer(cfg config.MasterConfig) *Server {
	server := &Server{
		cfg:      cfg,
		registry: NewRegistry(),
		commands: make(map[string]*shutdownCommandState),
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

func (s *Server) runAdminServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/commands/shutdown", s.handleManualShutdown)
	mux.HandleFunc("/commands/reset", s.handleReset)
	if err := http.ListenAndServe(s.cfg.AdminListenAddr, mux); err != nil {
		log.Printf("admin server stopped: %v", err)
	}
}

func (s *Server) runCommandWatcher() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.markTimedOutCommands(time.Now().UTC())
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.Status())
}

func (s *Server) handleManualShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request protocol.ShutdownRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return
	}
	message, summary, err := s.TriggerShutdown(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message": message,
		"status":  summary,
	})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.ResetActiveCommand()
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset complete"})
}

func (s *Server) handleConn(conn net.Conn) {
	client := NewClient(conn)
	defer func() {
		if client.NodeID != "" {
			s.registry.RemoveIfMatch(client.NodeID, client)
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
	client.Touch()
	s.registry.Set(client)

	if err := client.Send(protocol.TypeRegisterAck, protocol.RegisterAckMessage{Accepted: true, Message: "registered"}); err != nil {
		log.Printf("ack register to %s failed: %v", client.NodeID, err)
		return
	}
	log.Printf("slave registered node_id=%s hostname=%s tags=%v", client.NodeID, client.Hostname, client.Tags)
	if err := s.replayPendingShutdown(client); err != nil {
		log.Printf("replay pending shutdown to %s failed: %v", client.NodeID, err)
		return
	}

	for {
		env, err := client.ReadEnvelope()
		if err != nil {
			if err != io.EOF {
				log.Printf("read from %s failed: %v", client.NodeID, err)
			}
			return
		}
		client.Touch()
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
		client.Touch()
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
		s.recordShutdownUpdate(ack)
		log.Printf("shutdown update node_id=%s command_id=%s status=%s message=%s", ack.NodeID, ack.CommandID, ack.Status, ack.Message)
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
		_, _, err := s.TriggerShutdown(protocol.ShutdownRequest{Reason: s.cfg.ShutdownPolicy.ShutdownReason})
		if err != nil && err == errShutdownAlreadyActive {
			return nil
		}
		return err
	}
	return nil
}

var errShutdownAlreadyActive = fmt.Errorf("shutdown already active")

func (s *Server) TriggerShutdown(request protocol.ShutdownRequest) (protocol.ShutdownMessage, protocol.CommandSummary, error) {
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		reason = s.cfg.ShutdownPolicy.ShutdownReason
	}
	if reason == "" {
		reason = "shutdown requested"
	}
	message := protocol.ShutdownMessage{
		CommandID: fmt.Sprintf("shutdown-%d", s.commandSeq.Add(1)),
		Reason:    reason,
		DryRun:    s.cfg.DryRun,
		IssuedAt:  time.Now().UTC(),
		Target: protocol.ShutdownTarget{
			All:     len(request.NodeIDs) == 0 && len(request.Tags) == 0,
			NodeIDs: append([]string(nil), request.NodeIDs...),
			Tags:    append([]string(nil), request.Tags...),
		},
	}
	if request.DryRun != nil {
		message.DryRun = *request.DryRun
	}
	if !message.Target.All && len(message.Target.NodeIDs) == 0 && len(message.Target.Tags) == 0 {
		message.Target.All = true
	}

	targets := s.selectTargets(message.Target)
	if len(targets) == 0 {
		return protocol.ShutdownMessage{}, protocol.CommandSummary{}, fmt.Errorf("no target nodes matched request")
	}

	timeout := s.cfg.CommandTimeout.Duration
	if request.TimeoutSeconds != nil && *request.TimeoutSeconds > 0 {
		timeout = time.Duration(*request.TimeoutSeconds) * time.Second
	}
	timeoutAt := message.IssuedAt.Add(timeout)

	s.commandMu.Lock()
	if s.activeCommand != "" {
		s.commandMu.Unlock()
		return protocol.ShutdownMessage{}, protocol.CommandSummary{}, errShutdownAlreadyActive
	}
	targetNodes := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == nil || target.NodeID == "" {
			continue
		}
		targetNodes[target.NodeID] = struct{}{}
	}
	s.commands[message.CommandID] = &shutdownCommandState{
		Message:     message,
		TargetNodes: targetNodes,
		NodeUpdates: make(map[string]protocol.ShutdownAckMessage),
		TimeoutAt:   &timeoutAt,
	}
	s.activeCommand = message.CommandID
	s.shutdownIssued.Store(true)
	s.saveStateLocked()
	s.commandMu.Unlock()

	var firstErr error
	for _, client := range targets {
		if err := client.Send(protocol.TypeShutdown, message); err != nil {
			log.Printf("send shutdown to %s failed: %v", client.NodeID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	summary := s.commandSummary(message.CommandID)
	return message, summary, firstErr
}

func (s *Server) selectTargets(target protocol.ShutdownTarget) []*Client {
	clients := s.registry.List()
	if target.All {
		return clients
	}
	nodeIDSet := make(map[string]struct{}, len(target.NodeIDs))
	for _, nodeID := range target.NodeIDs {
		nodeIDSet[nodeID] = struct{}{}
	}
	tagSet := make(map[string]struct{}, len(target.Tags))
	for _, tag := range target.Tags {
		tagSet[tag] = struct{}{}
	}
	matched := make([]*Client, 0, len(clients))
	added := make(map[string]struct{}, len(clients))
	for _, client := range clients {
		if _, ok := nodeIDSet[client.NodeID]; ok {
			if _, seen := added[client.NodeID]; !seen {
				matched = append(matched, client)
				added[client.NodeID] = struct{}{}
			}
			continue
		}
		for _, tag := range client.Tags {
			if _, ok := tagSet[tag]; ok {
				if _, seen := added[client.NodeID]; !seen {
					matched = append(matched, client)
					added[client.NodeID] = struct{}{}
				}
				break
			}
		}
	}
	return matched
}

func (s *Server) rememberShutdownCommand(message protocol.ShutdownMessage, targets []*Client) {
	targetNodes := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == nil || target.NodeID == "" {
			continue
		}
		targetNodes[target.NodeID] = struct{}{}
	}

	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	s.commands[message.CommandID] = &shutdownCommandState{
		Message:     message,
		TargetNodes: targetNodes,
		NodeUpdates: make(map[string]protocol.ShutdownAckMessage),
	}
	s.activeCommand = message.CommandID
	s.saveStateLocked()
}

func (s *Server) markTimedOutCommands(now time.Time) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	changed := false
	for commandID, command := range s.commands {
		if command.CompletedAt != nil || command.TimeoutAt == nil || now.Before(*command.TimeoutAt) {
			continue
		}
		for nodeID := range command.TargetNodes {
			update, ok := command.NodeUpdates[nodeID]
			if ok && isCompleteShutdownStatus(update.Status) {
				continue
			}
			command.NodeUpdates[nodeID] = protocol.ShutdownAckMessage{
				CommandID: commandID,
				NodeID:    nodeID,
				Status:    protocol.ShutdownStatusTimeout,
				Message:   "command timed out waiting for terminal status",
				AckedAt:   now,
			}
			changed = true
		}
		if commandComplete(command) {
			completedAt := now
			command.CompletedAt = &completedAt
			if s.activeCommand == commandID {
				s.activeCommand = ""
				s.shutdownIssued.Store(false)
			}
			changed = true
		}
	}
	if changed {
		s.saveStateLocked()
	}
}

func (s *Server) recordShutdownUpdate(update protocol.ShutdownAckMessage) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	command, ok := s.commands[update.CommandID]
	if !ok {
		return
	}
	if existing, exists := command.NodeUpdates[update.NodeID]; exists && !shouldReplaceShutdownUpdate(existing, update) {
		return
	}
	command.NodeUpdates[update.NodeID] = update
	if commandComplete(command) {
		now := time.Now().UTC()
		command.CompletedAt = &now
		s.shutdownIssued.Store(false)
		if s.activeCommand == update.CommandID {
			s.activeCommand = ""
		}
	}
	s.saveStateLocked()
}

func (s *Server) replayPendingShutdown(client *Client) error {
	s.commandMu.Lock()
	commandID := s.activeCommand
	if commandID == "" {
		s.commandMu.Unlock()
		return nil
	}
	command, ok := s.commands[commandID]
	if !ok {
		s.commandMu.Unlock()
		return nil
	}
	if _, wasTarget := command.TargetNodes[client.NodeID]; !wasTarget {
		s.commandMu.Unlock()
		return nil
	}
	lastUpdate, hasUpdate := command.NodeUpdates[client.NodeID]
	message := command.Message
	timeoutAt := command.TimeoutAt
	s.commandMu.Unlock()

	if timeoutAt != nil && time.Now().UTC().After(*timeoutAt) {
		return nil
	}
	if hasUpdate && isCompleteShutdownStatus(lastUpdate.Status) {
		return nil
	}
	return client.Send(protocol.TypeShutdown, message)
}

func (s *Server) ResetActiveCommand() {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	s.activeCommand = ""
	s.shutdownIssued.Store(false)
	s.saveStateLocked()
}

func (s *Server) Status() protocol.StatusResponse {
	s.commandMu.Lock()
	activeCommandID := s.activeCommand
	commandCopy := make(map[string]*shutdownCommandState, len(s.commands))
	for commandID, state := range s.commands {
		copied := &shutdownCommandState{
			Message:     state.Message,
			TargetNodes: make(map[string]struct{}, len(state.TargetNodes)),
			NodeUpdates: make(map[string]protocol.ShutdownAckMessage, len(state.NodeUpdates)),
			TimeoutAt:   state.TimeoutAt,
			CompletedAt: state.CompletedAt,
		}
		for nodeID := range state.TargetNodes {
			copied.TargetNodes[nodeID] = struct{}{}
		}
		for nodeID, update := range state.NodeUpdates {
			copied.NodeUpdates[nodeID] = update
		}
		commandCopy[commandID] = copied
	}
	s.commandMu.Unlock()

	var activeSummary *protocol.CommandSummary
	if activeCommandID != "" {
		summary := summarizeCommand(commandCopy[activeCommandID])
		activeSummary = &summary
	}

	clients := s.registry.List()
	clientMap := make(map[string]*Client, len(clients))
	for _, client := range clients {
		clientMap[client.NodeID] = client
	}
	knownNodes := make(map[string]struct{})
	for _, client := range clients {
		knownNodes[client.NodeID] = struct{}{}
	}
	for _, command := range commandCopy {
		for nodeID := range command.TargetNodes {
			knownNodes[nodeID] = struct{}{}
		}
		for nodeID := range command.NodeUpdates {
			knownNodes[nodeID] = struct{}{}
		}
	}

	nodeIDs := make([]string, 0, len(knownNodes))
	for nodeID := range knownNodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)

	nodes := make([]protocol.NodeStatus, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		node := protocol.NodeStatus{NodeID: nodeID}
		if client, ok := clientMap[nodeID]; ok {
			lastSeen := client.LastSeen()
			node.Hostname = client.Hostname
			node.Tags = append([]string(nil), client.Tags...)
			node.Connected = true
			node.LastSeen = &lastSeen
		}
		if update, ok := latestNodeUpdate(nodeID, commandCopy); ok {
			copied := update
			node.LastShutdown = &copied
		}
		nodes = append(nodes, node)
	}

	return protocol.StatusResponse{
		ShutdownIssued: s.shutdownIssued.Load(),
		ActiveCommand:  activeSummary,
		Nodes:          nodes,
	}
}

func (s *Server) commandSummary(commandID string) protocol.CommandSummary {
	s.commandMu.Lock()
	command := s.commands[commandID]
	var copied *shutdownCommandState
	if command != nil {
		copied = &shutdownCommandState{
			Message:     command.Message,
			TargetNodes: make(map[string]struct{}, len(command.TargetNodes)),
			NodeUpdates: make(map[string]protocol.ShutdownAckMessage, len(command.NodeUpdates)),
			TimeoutAt:   command.TimeoutAt,
			CompletedAt: command.CompletedAt,
		}
		for nodeID := range command.TargetNodes {
			copied.TargetNodes[nodeID] = struct{}{}
		}
		for nodeID, update := range command.NodeUpdates {
			copied.NodeUpdates[nodeID] = update
		}
	}
	s.commandMu.Unlock()
	return summarizeCommand(copied)
}

func summarizeCommand(command *shutdownCommandState) protocol.CommandSummary {
	if command == nil {
		return protocol.CommandSummary{}
	}
	updates := make([]protocol.ShutdownAckMessage, 0, len(command.NodeUpdates))
	summary := protocol.CommandSummary{
		CommandID: command.Message.CommandID,
		Reason:    command.Message.Reason,
		DryRun:    command.Message.DryRun,
		IssuedAt:  command.Message.IssuedAt,
		Target:    command.Message.Target,
		Targeted:  len(command.TargetNodes),
	}
	if command.TimeoutAt != nil {
		timeoutAt := *command.TimeoutAt
		summary.TimeoutAt = &timeoutAt
	}
	for nodeID := range command.TargetNodes {
		update, ok := command.NodeUpdates[nodeID]
		if !ok {
			summary.Outstanding++
			continue
		}
		updates = append(updates, update)
		summary.Acknowledged++
		switch update.Status {
		case protocol.ShutdownStatusAccepted:
		case protocol.ShutdownStatusExecuting:
			summary.Executing++
		case protocol.ShutdownStatusExecuted:
			summary.Executed++
		case protocol.ShutdownStatusFailed:
			summary.Failed++
		case protocol.ShutdownStatusTimeout:
			summary.Timeout++
		}
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].NodeID < updates[j].NodeID })
	summary.LastNodeUpdates = updates
	summary.Complete = commandComplete(command)
	if command.CompletedAt != nil {
		completedAt := *command.CompletedAt
		summary.CompletedAt = &completedAt
	}
	return summary
}

func latestNodeUpdate(nodeID string, commands map[string]*shutdownCommandState) (protocol.ShutdownAckMessage, bool) {
	var latest protocol.ShutdownAckMessage
	var found bool
	for _, command := range commands {
		update, ok := command.NodeUpdates[nodeID]
		if !ok {
			continue
		}
		if !found || update.AckedAt.After(latest.AckedAt) {
			latest = update
			found = true
		}
	}
	return latest, found
}

func commandComplete(command *shutdownCommandState) bool {
	for nodeID := range command.TargetNodes {
		update, ok := command.NodeUpdates[nodeID]
		if !ok || !isCompleteShutdownStatus(update.Status) {
			return false
		}
	}
	return len(command.TargetNodes) > 0
}

func isCompleteShutdownStatus(status string) bool {
	switch status {
	case protocol.ShutdownStatusExecuted, protocol.ShutdownStatusFailed, protocol.ShutdownStatusTimeout:
		return true
	default:
		return false
	}
}

func isFinalShutdownStatus(status string) bool {
	switch status {
	case protocol.ShutdownStatusExecuted, protocol.ShutdownStatusFailed:
		return true
	default:
		return false
	}
}

func shouldReplaceShutdownUpdate(existing, next protocol.ShutdownAckMessage) bool {
	if isFinalShutdownStatus(existing.Status) {
		return false
	}
	if existing.Status == protocol.ShutdownStatusTimeout {
		return isFinalShutdownStatus(next.Status)
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode response failed: %v", err)
	}
}
