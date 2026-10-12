package master

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"nut-server/internal/protocol"
)

var errShutdownAlreadyActive = fmt.Errorf("shutdown already active")

// maxManualTimeoutSeconds bounds a manual request's timeout_seconds so the
// duration arithmetic cannot overflow into an already-expired deadline.
const maxManualTimeoutSeconds = 24 * 60 * 60

// maxRetainedCompletedCommands bounds s.commands so a master that runs for
// months doesn't grow the map (and the state file / per-request Status clone)
// without limit. Completed commands beyond this count are pruned, oldest
// first, skipping the active command and the one local_shutdown is currently
// waiting on (see pruneCompletedCommandsLocked).
const maxRetainedCompletedCommands = 200

// newCommandID returns an ID that stays unique across state-file loss and
// reinstalls: slaves dedupe on command_id forever, so a reused ID would be
// acknowledged from history instead of executed.
func newCommandID(now time.Time) string {
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("shutdown-%s-%s", now.UTC().Format("20060102T150405Z"), hex.EncodeToString(suffix[:]))
}

// TriggerShutdown issues a manual shutdown command. Automatic, policy-driven
// shutdowns go through applyPolicyMatch instead.
func (s *Server) TriggerShutdown(request protocol.ShutdownRequest) (protocol.ShutdownMessage, protocol.CommandSummary, error) {
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		reason = "shutdown requested"
	}
	timeout := s.cfg.CommandTimeout.Duration
	if request.TimeoutSeconds != nil && *request.TimeoutSeconds > 0 {
		if *request.TimeoutSeconds > maxManualTimeoutSeconds {
			return protocol.ShutdownMessage{}, protocol.CommandSummary{}, fmt.Errorf("timeout_seconds must not exceed %d", maxManualTimeoutSeconds)
		}
		timeout = time.Duration(*request.TimeoutSeconds) * time.Second
	}
	now := time.Now().UTC()
	message := protocol.ShutdownMessage{
		CommandID: newCommandID(now),
		Reason:    reason,
		DryRun:    s.cfg.DryRun,
		IssuedAt:  now,
		Target: protocol.ShutdownTarget{
			All:     len(request.NodeIDs) == 0 && len(request.Tags) == 0,
			NodeIDs: append([]string(nil), request.NodeIDs...),
			Tags:    append([]string(nil), request.Tags...),
		},
	}
	if request.DryRun != nil {
		message.DryRun = *request.DryRun
	}

	targets := s.selectTargets(message.Target)
	if len(targets) == 0 {
		return protocol.ShutdownMessage{}, protocol.CommandSummary{}, fmt.Errorf("no target nodes matched request")
	}
	timeoutAt := now.Add(timeout)

	s.commandMu.Lock()
	if s.activeCommand != "" {
		s.commandMu.Unlock()
		return protocol.ShutdownMessage{}, protocol.CommandSummary{}, errShutdownAlreadyActive
	}
	command := &shutdownCommandState{
		Message:     message,
		TargetNodes: buildTargetNodeSet(targets),
		NodeUpdates: make(map[string]protocol.ShutdownAckMessage),
		TimeoutAt:   &timeoutAt,
	}
	s.commands[message.CommandID] = command
	s.activeCommand = message.CommandID
	s.armLocalShutdownLocked(command, now)
	s.shutdownIssued.Store(true)
	seq, content := s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)
	recordShutdownIssued()

	err := s.sendShutdown(message, targets)
	summary := s.commandSummary(message.CommandID)
	return message, summary, err
}

// sendShutdown delivers the command to every client concurrently: a slow or
// half-open slave can stall up to the per-Send write deadline, and a serial
// loop would make every later target wait behind it. Returns the first error.
func (s *Server) sendShutdown(message protocol.ShutdownMessage, clients []*Client) error {
	var (
		wg       sync.WaitGroup
		sendMu   sync.Mutex
		firstErr error
	)
	for _, client := range clients {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			if err := client.Send(protocol.TypeShutdown, message); err != nil {
				slog.Error("send shutdown failed",
					"command_id", message.CommandID,
					"node_id", client.NodeID,
					"err", err)
				sendMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				sendMu.Unlock()
			}
		}(client)
	}
	wg.Wait()
	return firstErr
}

func (s *Server) selectTargets(target protocol.ShutdownTarget) []*Client {
	clients := s.registry.List()
	matched := make([]*Client, 0, len(clients))
	for _, client := range clients {
		if targetMatchesNode(target, client.NodeID, client.Tags) {
			matched = append(matched, client)
		}
	}
	return matched
}

// buildTargetNodeSet collapses a client list into the set of non-empty node IDs
// used as a command's TargetNodes. Shared by triggerShutdown and the test-only
// rememberShutdownCommand so the two paths can never drift on what counts as a
// target (e.g. nil/blank-ID filtering).
func buildTargetNodeSet(targets []*Client) map[string]struct{} {
	targetNodes := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == nil || target.NodeID == "" {
			continue
		}
		targetNodes[target.NodeID] = struct{}{}
	}
	return targetNodes
}

func (s *Server) rememberShutdownCommand(message protocol.ShutdownMessage, targets []*Client) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	s.commands[message.CommandID] = &shutdownCommandState{
		Message:     message,
		TargetNodes: buildTargetNodeSet(targets),
		NodeUpdates: make(map[string]protocol.ShutdownAckMessage),
	}
	s.activeCommand = message.CommandID
	s.saveStateLocked()
}

func (s *Server) markTimedOutCommands(now time.Time) {
	s.commandMu.Lock()
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
			if s.activeCommand == commandID && !command.Auto && !commandHasRepairableTimeout(command) {
				s.activeCommand = ""
				s.shutdownIssued.Store(false)
			}
			changed = true
		}
	}
	if s.pruneCompletedCommandsLocked() {
		changed = true
	}
	var seq uint64
	var content []byte
	if changed {
		seq, content = s.snapshotStateLocked()
	}
	s.commandMu.Unlock()
	s.persistState(seq, content)
}

// pruneCompletedCommandsLocked evicts the oldest completed commands once
// s.commands exceeds maxRetainedCompletedCommands, so a long-running master
// doesn't accumulate shutdown history forever. It must be called with
// commandMu held. A command is only ever looked up again by ID while it is
// s.activeCommand (replay) or referenced by s.localShutdown.CommandID (the
// "wait for remote" state machine in evaluateLocalShutdown / state.go), so
// both are exempt regardless of age. Returns true if anything was deleted.
func (s *Server) pruneCompletedCommandsLocked() bool {
	excess := len(s.commands) - maxRetainedCompletedCommands
	if excess <= 0 {
		return false
	}
	type candidate struct {
		id string
		at time.Time
	}
	candidates := make([]candidate, 0, excess)
	for id, command := range s.commands {
		if command.CompletedAt == nil || id == s.activeCommand {
			continue
		}
		if s.localShutdown != nil && s.localShutdown.CommandID == id {
			continue
		}
		candidates = append(candidates, candidate{id: id, at: *command.CompletedAt})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].at.Before(candidates[j].at) })
	if excess > len(candidates) {
		excess = len(candidates)
	}
	for i := 0; i < excess; i++ {
		delete(s.commands, candidates[i].id)
	}
	return excess > 0
}

func (s *Server) recordShutdownUpdate(update protocol.ShutdownAckMessage) {
	var startLocalShutdown bool

	s.commandMu.Lock()
	command, ok := s.commands[update.CommandID]
	if !ok {
		s.commandMu.Unlock()
		return
	}
	if _, isTarget := command.TargetNodes[update.NodeID]; !isTarget {
		// Ignore acks for nodes that were never part of this command, so a
		// stale or fabricated node_id cannot pollute per-command state.
		s.commandMu.Unlock()
		return
	}
	if existing, exists := command.NodeUpdates[update.NodeID]; exists && !shouldReplaceShutdownUpdate(existing, update) {
		s.commandMu.Unlock()
		return
	}
	command.NodeUpdates[update.NodeID] = update
	if !update.AckedAt.IsZero() && !command.Message.IssuedAt.IsZero() {
		recordShutdownAckLatency(update.Status, update.AckedAt.Sub(command.Message.IssuedAt).Seconds())
	}
	if commandComplete(command) {
		now := time.Now().UTC()
		command.CompletedAt = &now
		if s.activeCommand == update.CommandID && !command.Auto && !commandHasRepairableTimeout(command) {
			s.activeCommand = ""
			s.shutdownIssued.Store(false)
		}
	}
	if remoteShutdownFinished(command) && s.localShutdown != nil && s.localShutdown.CommandID == update.CommandID && s.localShutdown.Phase == protocol.LocalShutdownPhaseWaitingRemote {
		s.localShutdown.Trigger = protocol.LocalShutdownTriggerRemoteComplete
		startLocalShutdown = true
	}
	seq, content := s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)

	if startLocalShutdown {
		slog.Info("local shutdown triggered",
			"command_id", update.CommandID,
			"trigger", protocol.LocalShutdownTriggerRemoteComplete)
		s.beginLocalShutdownExecution(update.CommandID, protocol.LocalShutdownTriggerRemoteComplete)
	}
}

func (s *Server) replayPendingShutdown(client *Client) error {
	message, ok := s.replayableShutdownForNode(client)
	if !ok {
		return nil
	}
	return client.Send(protocol.TypeShutdown, message)
}

// ResetActiveCommand cancels the active command and any local shutdown that is
// not already running. During an outage the episode is kept with its fired
// policies, so they do not fire again at the next poll; policies that have not
// fired yet and the emergency still apply.
func (s *Server) ResetActiveCommand() {
	s.commandMu.Lock()
	if command, ok := s.commands[s.activeCommand]; ok && command != nil {
		command.ReplayDisabled = true
	}
	if s.episode != nil {
		if command, ok := s.commands[s.episode.CommandID]; ok {
			command.ReplayDisabled = true
		}
		s.episode.CommandID = ""
	}
	if s.localShutdown != nil && s.localShutdown.Phase != protocol.LocalShutdownPhaseExecuting {
		s.localShutdown = nil
	}
	s.activeCommand = ""
	s.shutdownIssued.Store(false)
	seq, content := s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)
}

func (s *Server) commandSummary(commandID string) protocol.CommandSummary {
	s.commandMu.Lock()
	copied := s.commands[commandID].clone()
	s.commandMu.Unlock()
	return summarizeCommand(copied)
}

// clone returns a deep copy of the command state that is safe to read after
// commandMu is released: the mutable maps are duplicated so later mutations
// under the lock cannot race a snapshot reader. The *time.Time fields are
// shared because their pointees are only ever replaced, never mutated in place.
func (c *shutdownCommandState) clone() *shutdownCommandState {
	if c == nil {
		return nil
	}
	copied := &shutdownCommandState{
		Message:        c.Message,
		TargetNodes:    make(map[string]struct{}, len(c.TargetNodes)),
		NodeUpdates:    make(map[string]protocol.ShutdownAckMessage, len(c.NodeUpdates)),
		TimeoutAt:      c.TimeoutAt,
		CompletedAt:    c.CompletedAt,
		ReplayDisabled: c.ReplayDisabled,
		Auto:           c.Auto,
	}
	for nodeID := range c.TargetNodes {
		copied.TargetNodes[nodeID] = struct{}{}
	}
	for nodeID, update := range c.NodeUpdates {
		copied.NodeUpdates[nodeID] = update
	}
	return copied
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
		case protocol.ShutdownStatusSimulated:
			summary.Simulated++
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

func (s *Server) replayableShutdownForNode(client *Client) (protocol.ShutdownMessage, bool) {
	s.commandMu.Lock()

	command, ok := s.commands[s.activeCommand]
	if !ok {
		s.commandMu.Unlock()
		return protocol.ShutdownMessage{}, false
	}
	// An auto command restored from the state file is not replayed until this
	// process has read the UPS once: after the master powers itself off and
	// boots on restored line power, slaves may reconnect before the UPS card is
	// up and must not be shut down again by a stale episode.
	if command.Auto && !s.upsConfirmed.Load() {
		s.commandMu.Unlock()
		return protocol.ShutdownMessage{}, false
	}
	// A node that was offline when the command was issued is adopted when it
	// reconnects, as long as it matches the command target (all, node_id or
	// tag), so it is shut down and tracked to completion.
	var seq uint64
	var content []byte
	if !command.ReplayDisabled && targetMatchesNode(command.Message.Target, client.NodeID, client.Tags) {
		if _, already := command.TargetNodes[client.NodeID]; !already {
			command.TargetNodes[client.NodeID] = struct{}{}
			s.reopenCommandLocked(command, time.Now().UTC())
			seq, content = s.snapshotStateLocked()
		}
	}
	replay := shouldReplayShutdownForNode(command, client.NodeID)
	message := command.Message
	s.commandMu.Unlock()
	s.persistState(seq, content)
	if replay {
		return message, true
	}
	return protocol.ShutdownMessage{}, false
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

func remoteShutdownFinished(command *shutdownCommandState) bool {
	for nodeID := range command.TargetNodes {
		update, ok := command.NodeUpdates[nodeID]
		if !ok || !isFinalShutdownStatus(update.Status) {
			return false
		}
	}
	return len(command.TargetNodes) > 0
}

func commandHasRepairableTimeout(command *shutdownCommandState) bool {
	for nodeID := range command.TargetNodes {
		update, ok := command.NodeUpdates[nodeID]
		if ok && update.Status == protocol.ShutdownStatusTimeout {
			return true
		}
	}
	return false
}

func shouldReplayShutdownForNode(command *shutdownCommandState, nodeID string) bool {
	if command == nil {
		return false
	}
	if command.ReplayDisabled {
		return false
	}
	if _, wasTarget := command.TargetNodes[nodeID]; !wasTarget {
		return false
	}
	// Replay until the node reports a SUCCESSFUL shutdown. A Failed (or Timeout)
	// status is not terminal for replay purposes, so a transiently-failed
	// shutdown is retried on the next reconnect/rebroadcast instead of leaving
	// the node powered on through the outage.
	if update, ok := command.NodeUpdates[nodeID]; ok && isReplayDoneStatus(update.Status) {
		return false
	}
	return true
}

// shouldReplaceShutdownUpdate keeps a confirmed outcome (executed or
// simulated) and otherwise takes the newer report, so a node that failed and
// then succeeded on retry ends up recorded as executed.
func shouldReplaceShutdownUpdate(existing, next protocol.ShutdownAckMessage) bool {
	return !isReplayDoneStatus(existing.Status)
}
