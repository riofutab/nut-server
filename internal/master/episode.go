package master

import (
	"log/slog"
	"slices"
	"time"

	"nut-server/internal/protocol"
)

// shutdownRetryInterval paces re-sends to failed / timed-out nodes of the
// episode command and retries of a failed local shutdown.
const shutdownRetryInterval = 30 * time.Second

// autoEpisode is one on-battery period. All automatic shutdown activity during
// the period goes through a single command whose target only ever grows: a
// policy that fires later in the same outage is merged into it instead of being
// rejected, so staged policies (e.g. shed dev early, everything at low runtime)
// both take effect.
type autoEpisode struct {
	StartedAt     time.Time `json:"started_at"`
	CommandID     string    `json:"command_id,omitempty"`
	FiredPolicies []string  `json:"fired_policies,omitempty"`
}

func (e *autoEpisode) status() *protocol.EpisodeStatus {
	if e == nil {
		return nil
	}
	return &protocol.EpisodeStatus{
		StartedAt:     e.StartedAt,
		CommandID:     e.CommandID,
		FiredPolicies: append([]string(nil), e.FiredPolicies...),
	}
}

func (s *Server) startEpisodeIfNeeded(at time.Time) {
	s.commandMu.Lock()
	if s.episode != nil {
		s.commandMu.Unlock()
		return
	}
	s.episode = &autoEpisode{StartedAt: at}
	seq, content := s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)
	slog.Warn("ups on battery, shutdown episode started", "started_at", at.Format(time.RFC3339))
}

// endEpisodeOnLinePower closes the current on-battery period: the episode
// command stops being replayed and a local shutdown that has not started yet is
// cancelled, so the master keeps running once power is back. It also releases a
// completed manual command still pinned by a repairable timeout.
func (s *Server) endEpisodeOnLinePower() {
	s.commandMu.Lock()
	changed := false
	ended := s.episode != nil
	if ended {
		commandID := s.episode.CommandID
		if command, ok := s.commands[commandID]; ok {
			command.ReplayDisabled = true
			if s.activeCommand == commandID {
				s.activeCommand = ""
				s.shutdownIssued.Store(false)
			}
		}
		if s.localShutdown != nil && s.localShutdown.CommandID == commandID && !localShutdownTerminal(s.localShutdown.Phase) {
			s.localShutdown = nil
		}
		s.episode = nil
		changed = true
	}
	if s.activeCommand != "" {
		if command, ok := s.commands[s.activeCommand]; ok && !command.Auto && commandComplete(command) {
			s.activeCommand = ""
			s.shutdownIssued.Store(false)
			changed = true
		}
	}
	var seq uint64
	var content []byte
	if changed {
		seq, content = s.snapshotStateLocked()
	}
	s.commandMu.Unlock()
	s.persistState(seq, content)
	if ended {
		slog.Info("line power restored, shutdown episode ended")
	}
}

// applyPolicyMatch turns the policies matched by a poll into shutdown activity
// for the current episode. Policies that already fired in this episode are
// ignored; newly fired ones either create the episode command or widen it.
func (s *Server) applyPolicyMatch(match PolicyMatch, now time.Time) {
	s.commandMu.Lock()
	episode := s.episode
	if episode == nil {
		s.commandMu.Unlock()
		return
	}
	var fired []string
	for _, name := range match.Names {
		if !slices.Contains(episode.FiredPolicies, name) {
			fired = append(fired, name)
		}
	}
	if len(fired) == 0 {
		s.commandMu.Unlock()
		return
	}
	reason := buildPolicyReason(s.cfg.ShutdownPolicies, fired)
	target := match.Target()

	var (
		command *shutdownCommandState
		toSend  []*Client
		created bool
	)
	if existing, ok := s.commands[episode.CommandID]; ok {
		command = existing
		wasAll := command.Message.Target.All
		command.Message.Target = mergeShutdownTargets(command.Message.Target, target)
		command.Message.Reason = command.Message.Reason + "; " + reason
		toSend = s.adoptMatchingClientsLocked(command, now)
		if !wasAll && command.Message.Target.All {
			s.armLocalShutdownLocked(command, now)
		}
	} else {
		s.supersedeManualCommandLocked()
		message := protocol.ShutdownMessage{
			CommandID: newCommandID(now),
			Reason:    reason,
			DryRun:    s.cfg.DryRun,
			IssuedAt:  now,
			Target:    target,
		}
		toSend = s.selectTargets(message.Target)
		timeoutAt := now.Add(s.cfg.CommandTimeout.Duration)
		command = &shutdownCommandState{
			Message:     message,
			TargetNodes: buildTargetNodeSet(toSend),
			NodeUpdates: make(map[string]protocol.ShutdownAckMessage),
			TimeoutAt:   &timeoutAt,
			Auto:        true,
		}
		s.commands[message.CommandID] = command
		s.activeCommand = message.CommandID
		episode.CommandID = message.CommandID
		s.lastNodeRetryAt = now
		if message.Target.All {
			s.armLocalShutdownLocked(command, now)
		}
		created = true
	}
	episode.FiredPolicies = append(episode.FiredPolicies, fired...)
	s.shutdownIssued.Store(true)
	message := command.Message
	seq, content := s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)

	if created {
		recordShutdownIssued()
	}
	slog.Warn("shutdown policies fired",
		"policies", fired,
		"command_id", message.CommandID,
		"created", created,
		"new_targets", len(toSend))
	if err := s.sendShutdown(message, toSend); err != nil {
		slog.Error("auto shutdown delivery incomplete", "command_id", message.CommandID, "err", err)
	}
}

// supersedeManualCommandLocked ends an active manual command so a real power
// event is never blocked behind an operator test that is still pending.
func (s *Server) supersedeManualCommandLocked() {
	command, ok := s.commands[s.activeCommand]
	if !ok || command.Auto {
		return
	}
	slog.Warn("auto shutdown supersedes active manual command", "command_id", s.activeCommand)
	command.ReplayDisabled = true
	if s.localShutdown != nil && s.localShutdown.CommandID == s.activeCommand && !localShutdownTerminal(s.localShutdown.Phase) {
		s.localShutdown = nil
	}
	s.activeCommand = ""
}

// adoptMatchingClientsLocked adds every connected client that matches the
// command target but is not yet tracked, and returns them so the caller can
// deliver the command. Adopted nodes get a fresh timeout window.
func (s *Server) adoptMatchingClientsLocked(command *shutdownCommandState, now time.Time) []*Client {
	var adopted []*Client
	for _, client := range s.selectTargets(command.Message.Target) {
		if _, tracked := command.TargetNodes[client.NodeID]; tracked {
			continue
		}
		command.TargetNodes[client.NodeID] = struct{}{}
		adopted = append(adopted, client)
	}
	if len(adopted) > 0 {
		s.reopenCommandLocked(command, now)
	}
	return adopted
}

// reopenCommandLocked marks a command as having outstanding targets again and
// extends its timeout so newly added nodes are not timed out immediately.
func (s *Server) reopenCommandLocked(command *shutdownCommandState, now time.Time) {
	command.CompletedAt = nil
	timeoutAt := now.Add(s.cfg.CommandTimeout.Duration)
	if command.TimeoutAt == nil || command.TimeoutAt.Before(timeoutAt) {
		command.TimeoutAt = &timeoutAt
	}
}

// armLocalShutdownLocked starts the "wait for remotes, then power off the
// master" flow for a command that targets every node. Commands scoped to tags
// or node IDs never power off the master.
func (s *Server) armLocalShutdownLocked(command *shutdownCommandState, now time.Time) {
	if !s.cfg.LocalShutdown.Enabled || s.localShutdown != nil || !command.Message.Target.All {
		return
	}
	startedAt := now
	deadlineAt := now.Add(s.cfg.LocalShutdown.MaxWait.Duration)
	s.localShutdown = &localShutdownState{
		Phase:      protocol.LocalShutdownPhaseWaitingRemote,
		CommandID:  command.Message.CommandID,
		StartedAt:  &startedAt,
		DeadlineAt: &deadlineAt,
		DryRun:     command.Message.DryRun,
	}
	slog.Info("local shutdown waiting",
		"command_id", command.Message.CommandID,
		"deadline", deadlineAt.Format(time.RFC3339))
}

// retryEpisodeShutdown re-sends the episode command to targets whose last
// status is failed or timeout, at most once per shutdownRetryInterval.
func (s *Server) retryEpisodeShutdown(now time.Time) {
	s.commandMu.Lock()
	if s.episode == nil || !s.upsConfirmed.Load() || now.Sub(s.lastNodeRetryAt) < shutdownRetryInterval {
		s.commandMu.Unlock()
		return
	}
	command, ok := s.commands[s.episode.CommandID]
	if !ok || command.ReplayDisabled {
		s.commandMu.Unlock()
		return
	}
	nodeIDs := make(map[string]struct{})
	for nodeID := range command.TargetNodes {
		update, ok := command.NodeUpdates[nodeID]
		if ok && (update.Status == protocol.ShutdownStatusFailed || update.Status == protocol.ShutdownStatusTimeout) {
			nodeIDs[nodeID] = struct{}{}
		}
	}
	if len(nodeIDs) == 0 {
		s.commandMu.Unlock()
		return
	}
	s.lastNodeRetryAt = now
	message := command.Message
	s.commandMu.Unlock()

	slog.Warn("retrying shutdown for unfinished nodes", "command_id", message.CommandID, "nodes", len(nodeIDs))
	s.replayShutdownToNodes(message, nodeIDs)
}

func mergeShutdownTargets(a, b protocol.ShutdownTarget) protocol.ShutdownTarget {
	if a.All || b.All {
		return protocol.ShutdownTarget{All: true}
	}
	return protocol.ShutdownTarget{
		NodeIDs: sortedUnion(a.NodeIDs, b.NodeIDs),
		Tags:    sortedUnion(a.Tags, b.Tags),
	}
}

func sortedUnion(a, b []string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for _, v := range a {
		set[v] = struct{}{}
	}
	for _, v := range b {
		set[v] = struct{}{}
	}
	return sortedKeys(set)
}

func targetMatchesNode(target protocol.ShutdownTarget, nodeID string, tags []string) bool {
	if target.All || slices.Contains(target.NodeIDs, nodeID) {
		return true
	}
	for _, tag := range tags {
		if slices.Contains(target.Tags, tag) {
			return true
		}
	}
	return false
}
