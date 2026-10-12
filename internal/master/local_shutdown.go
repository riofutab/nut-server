package master

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"nut-server/internal/protocol"
)

func (s *Server) runCommandWatcher(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now().UTC()
		s.markTimedOutCommands(now)
		s.retryEpisodeShutdown(now)
		s.evaluateLocalShutdown(now, s.latestSuccessfulUPSStatus())
	}
}

func (s *Server) emergencyRuntimeReached(upsStatus *UPSStatus) bool {
	return upsStatus != nil && upsStatus.RuntimeMinutes >= 0 && upsStatus.RuntimeMinutes < s.cfg.LocalShutdown.EmergencyRuntimeMinutes
}

// emergencyTrigger returns why the master must power off right away, or "".
// Callers only act on it during an episode, so a silent UPS on line power never
// powers off the master.
func (s *Server) emergencyTrigger(upsStatus *UPSStatus, now time.Time) string {
	if s.emergencyRuntimeReached(upsStatus) {
		return protocol.LocalShutdownTriggerEmergencyRuntime
	}
	unreachableAfter := s.cfg.LocalShutdown.UnreachableAfter.Duration
	if silentFor, silent := s.upsSilentFor(now); silent && unreachableAfter > 0 && silentFor >= unreachableAfter {
		return protocol.LocalShutdownTriggerUPSUnreachable
	}
	return ""
}

func (s *Server) evaluateLocalShutdown(now time.Time, upsStatus *UPSStatus) {
	if !s.cfg.LocalShutdown.Enabled {
		return
	}

	var (
		commandID   string
		trigger     string
		replay      protocol.ShutdownMessage
		replayNodes map[string]struct{}
		dirty       bool
	)
	emergency := s.emergencyTrigger(upsStatus, now)
	// A silent UPS past unreachable_after is acted on even before the first
	// reading of this process; waiting for one would never end.
	trusted := s.upsConfirmed.Load() || emergency == protocol.LocalShutdownTriggerUPSUnreachable

	s.commandMu.Lock()
	if emergency == protocol.LocalShutdownTriggerUPSUnreachable && s.episode != nil {
		s.upsConfirmed.Store(true)
	}
	state := s.localShutdown
	if state == nil {
		// Emergency is a safety net independent of any "all" command: during an
		// episode the master powers itself off at critical runtime or when the
		// UPS stops answering, even when no policy targets every node (or none
		// has fired yet).
		if s.episode == nil || !trusted || emergency == "" {
			s.commandMu.Unlock()
			return
		}
		command, hasCommand := s.commands[s.episode.CommandID]
		rebroadcastAt := now
		state = &localShutdownState{
			Phase:     protocol.LocalShutdownPhaseEmergency,
			CommandID: s.episode.CommandID,
			StartedAt: &rebroadcastAt,
			Trigger:   emergency,
			DryRun:    s.cfg.DryRun,
		}
		if hasCommand {
			state.DryRun = command.Message.DryRun
			state.LastRebroadcastAt = &rebroadcastAt
			replay = command.Message
			replayNodes = replayableNodeIDs(command)
		}
		s.localShutdown = state
		commandID = state.CommandID
		trigger = state.Trigger
		dirty = true
	} else {
		command, ok := s.commands[state.CommandID]
		// Auto local shutdown restored from the state file waits for the first
		// UPS reading, which ends the episode if power came back while the
		// master was down.
		if (!ok || command.Auto) && !trusted {
			s.commandMu.Unlock()
			return
		}
		switch state.Phase {
		case protocol.LocalShutdownPhaseWaitingRemote:
			// Do NOT discard merely because activeCommand was cleared on
			// completion: recordShutdownUpdate clears activeCommand and unlocks
			// before starting local shutdown, and this watcher must not nil the
			// state in that gap. Only an absent or reset command cancels it.
			if !ok || command.ReplayDisabled {
				s.localShutdown = nil
				seq, content := s.snapshotStateLocked()
				s.commandMu.Unlock()
				s.persistState(seq, content)
				return
			}
			switch {
			case remoteShutdownFinished(command):
				// Backstop for the recordShutdownUpdate race: independently
				// drive the master power-off once every remote node is done.
				state.Trigger = protocol.LocalShutdownTriggerRemoteComplete
				commandID = state.CommandID
				trigger = state.Trigger
				dirty = true
			case emergency != "":
				rebroadcastAt := now
				state.Phase = protocol.LocalShutdownPhaseEmergency
				state.Trigger = emergency
				state.LastRebroadcastAt = &rebroadcastAt
				commandID = state.CommandID
				trigger = state.Trigger
				replay = command.Message
				replayNodes = replayableNodeIDs(command)
				dirty = true
			case state.DeadlineAt != nil && !now.Before(*state.DeadlineAt):
				state.Phase = protocol.LocalShutdownPhaseWaitExpired
				state.Trigger = protocol.LocalShutdownTriggerWaitExpired
				commandID = state.CommandID
				trigger = state.Trigger
				dirty = true
			}
		case protocol.LocalShutdownPhaseWaitExpired:
			commandID = state.CommandID
			trigger = protocol.LocalShutdownTriggerWaitExpired
		case protocol.LocalShutdownPhaseEmergency:
			commandID = state.CommandID
			trigger = state.Trigger
		case protocol.LocalShutdownPhaseFailed:
			if state.LastAttemptAt == nil || now.Sub(*state.LastAttemptAt) >= shutdownRetryInterval {
				commandID = state.CommandID
				trigger = state.Trigger
			}
		}
	}
	var seq uint64
	var content []byte
	if dirty {
		seq, content = s.snapshotStateLocked()
	}
	s.commandMu.Unlock()
	s.persistState(seq, content)

	if len(replayNodes) > 0 {
		slog.Warn("local shutdown emergency replay",
			"command_id", replay.CommandID,
			"nodes", len(replayNodes))
		s.replayShutdownToNodes(replay, replayNodes)
	}
	if trigger != "" {
		if trigger == protocol.LocalShutdownTriggerWaitExpired {
			slog.Warn("local shutdown wait expired", "command_id", commandID)
		}
		s.beginLocalShutdownExecution(commandID, trigger)
	}
}

func (s *Server) beginLocalShutdownExecution(commandID, trigger string) {
	command := append([]string(nil), s.cfg.LocalShutdown.Command...)

	s.commandMu.Lock()
	if s.localShutdown == nil || s.localShutdown.CommandID != commandID || localShutdownTerminal(s.localShutdown.Phase) {
		s.commandMu.Unlock()
		return
	}
	attemptAt := time.Now().UTC()
	s.localShutdown.Phase = protocol.LocalShutdownPhaseExecuting
	s.localShutdown.Trigger = trigger
	s.localShutdown.LastError = ""
	s.localShutdown.LastAttemptAt = &attemptAt
	dryRun := s.localShutdown.DryRun
	seq, content := s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)

	slog.Info("local shutdown starting", "command_id", commandID, "trigger", trigger, "dry_run", dryRun)
	err := s.localShutdownRunner(command, trigger, dryRun)

	s.commandMu.Lock()
	if s.localShutdown == nil || s.localShutdown.CommandID != commandID {
		s.commandMu.Unlock()
		return
	}
	if err != nil {
		s.localShutdown.Phase = protocol.LocalShutdownPhaseFailed
		s.localShutdown.LastError = err.Error()
		slog.Error("local shutdown failed, will retry",
			"command_id", commandID,
			"trigger", trigger,
			"retry_in", shutdownRetryInterval,
			"err", err)
	} else {
		s.localShutdown.Phase = protocol.LocalShutdownPhaseCompleted
		s.localShutdown.LastError = ""
	}
	seq, content = s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)
}

func (s *Server) runLocalShutdownCommand(command []string, trigger string, dryRun bool) error {
	if len(command) == 0 {
		return fmt.Errorf("local shutdown command is empty")
	}
	if dryRun {
		slog.Info("dry-run local shutdown", "trigger", trigger, "command", command)
		return nil
	}
	// Bound the command so a hung shutdown script becomes a reported failure
	// rather than pinning local shutdown in "executing" forever.
	ctx := context.Background()
	if s.cfg.LocalShutdown.CommandTimeout.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.LocalShutdown.CommandTimeout.Duration)
		defer cancel()
	}
	return exec.CommandContext(ctx, command[0], command[1:]...).Run()
}

func (s *Server) replayShutdownToNodes(message protocol.ShutdownMessage, nodeIDs map[string]struct{}) {
	for _, client := range s.registry.List() {
		if _, ok := nodeIDs[client.NodeID]; !ok {
			continue
		}
		go func(client *Client) {
			if err := client.Send(protocol.TypeShutdown, message); err != nil {
				slog.Error("rebroadcast shutdown failed",
					"command_id", message.CommandID,
					"node_id", client.NodeID,
					"err", err)
			}
		}(client)
	}
}

func replayableNodeIDs(command *shutdownCommandState) map[string]struct{} {
	nodeIDs := make(map[string]struct{}, len(command.TargetNodes))
	for nodeID := range command.TargetNodes {
		if shouldReplayShutdownForNode(command, nodeID) {
			nodeIDs[nodeID] = struct{}{}
		}
	}
	return nodeIDs
}

// localShutdownTerminal reports whether the master power-off is running or has
// succeeded. Failed is not terminal: it is retried and may still be cancelled.
func localShutdownTerminal(phase string) bool {
	switch phase {
	case protocol.LocalShutdownPhaseExecuting, protocol.LocalShutdownPhaseCompleted:
		return true
	default:
		return false
	}
}

func (s *Server) localShutdownStatusLocked() *protocol.LocalShutdownStatus {
	if !s.cfg.LocalShutdown.Enabled {
		return nil
	}
	status := &protocol.LocalShutdownStatus{
		Enabled:      true,
		Phase:        protocol.LocalShutdownPhaseIdle,
		CommandReady: copyBool(s.localCommandReady),
	}
	if s.localShutdown == nil {
		return status
	}
	status.Phase = s.localShutdown.Phase
	status.CommandID = s.localShutdown.CommandID
	status.StartedAt = copyTime(s.localShutdown.StartedAt)
	status.DeadlineAt = copyTime(s.localShutdown.DeadlineAt)
	status.Trigger = s.localShutdown.Trigger
	status.LastRebroadcastAt = copyTime(s.localShutdown.LastRebroadcastAt)
	status.LastError = s.localShutdown.LastError
	return status
}

func copyTime(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}
