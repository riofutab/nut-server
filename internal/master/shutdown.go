package master

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"nut-server/internal/protocol"
)

var errShutdownAlreadyActive = fmt.Errorf("shutdown already active")

func (s *Server) TriggerShutdown(request protocol.ShutdownRequest) (protocol.ShutdownMessage, protocol.CommandSummary, error) {
	return s.triggerShutdown(request, false)
}

func (s *Server) triggerShutdown(request protocol.ShutdownRequest, auto bool) (protocol.ShutdownMessage, protocol.CommandSummary, error) {
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
	if s.activeCommand != "" || (auto && s.autoShutdownLatched) {
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
	if auto {
		s.autoShutdownLatched = true
	}
	if s.cfg.LocalShutdown.Enabled {
		startedAt := message.IssuedAt
		deadlineAt := startedAt.Add(s.cfg.LocalShutdown.MaxWait.Duration)
		s.localShutdown = &localShutdownState{
			Phase:      protocol.LocalShutdownPhaseWaitingRemote,
			CommandID:  message.CommandID,
			StartedAt:  &startedAt,
			DeadlineAt: &deadlineAt,
		}
	}
	s.shutdownIssued.Store(true)
	s.saveStateLocked()
	s.commandMu.Unlock()

	if s.cfg.LocalShutdown.Enabled {
		log.Printf("local shutdown waiting command_id=%s deadline=%s", message.CommandID, message.IssuedAt.Add(s.cfg.LocalShutdown.MaxWait.Duration).Format(time.RFC3339))
	}

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
			if s.activeCommand == commandID && !commandHasRepairableTimeout(command) {
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
	var startLocalShutdown bool

	s.commandMu.Lock()
	command, ok := s.commands[update.CommandID]
	if !ok {
		s.commandMu.Unlock()
		return
	}
	if existing, exists := command.NodeUpdates[update.NodeID]; exists && !shouldReplaceShutdownUpdate(existing, update) {
		s.commandMu.Unlock()
		return
	}
	command.NodeUpdates[update.NodeID] = update
	if commandComplete(command) {
		now := time.Now().UTC()
		command.CompletedAt = &now
		if s.activeCommand == update.CommandID && !commandHasRepairableTimeout(command) {
			s.activeCommand = ""
			s.shutdownIssued.Store(false)
		}
	}
	if remoteShutdownFinished(command) && s.localShutdown != nil && s.localShutdown.CommandID == update.CommandID && s.localShutdown.Phase == protocol.LocalShutdownPhaseWaitingRemote {
		s.localShutdown.Trigger = protocol.LocalShutdownTriggerRemoteComplete
		startLocalShutdown = true
	}
	s.saveStateLocked()
	s.commandMu.Unlock()

	if startLocalShutdown {
		log.Printf("local shutdown triggered by remote completion command_id=%s", update.CommandID)
		s.beginLocalShutdownExecution(update.CommandID, protocol.LocalShutdownTriggerRemoteComplete)
	}
}

func (s *Server) replayPendingShutdown(client *Client) error {
	message, ok := s.replayableShutdownForNode(client.NodeID)
	if !ok {
		return nil
	}
	return client.Send(protocol.TypeShutdown, message)
}

func (s *Server) ResetActiveCommand() {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	clearedCommand := s.activeCommand
	if command, ok := s.commands[s.activeCommand]; ok && command != nil {
		command.ReplayDisabled = true
	}
	if s.localShutdown != nil && s.localShutdown.CommandID == clearedCommand && !localShutdownExecutionStarted(s.localShutdown.Phase) {
		s.localShutdown = nil
	}
	s.activeCommand = ""
	s.autoShutdownLatched = false
	s.shutdownIssued.Store(false)
	s.saveStateLocked()
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

func (s *Server) replayableShutdownForNode(nodeID string) (protocol.ShutdownMessage, bool) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()

	if command, ok := s.commands[s.activeCommand]; ok && shouldReplayShutdownForNode(command, nodeID) {
		return command.Message, true
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
	if update, ok := command.NodeUpdates[nodeID]; ok && isFinalShutdownStatus(update.Status) {
		return false
	}
	return true
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
