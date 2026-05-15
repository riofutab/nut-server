package master

import (
	"fmt"
	"log"
	"os/exec"
	"time"

	"nut-server/internal/protocol"
)

func (s *Server) runCommandWatcher() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now().UTC()
		s.markTimedOutCommands(now)
		s.evaluateLocalShutdown(now, s.latestSuccessfulUPSStatus())
	}
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
	)

	s.commandMu.Lock()
	if s.localShutdown == nil {
		s.commandMu.Unlock()
		return
	}

	state := s.localShutdown
	switch state.Phase {
	case protocol.LocalShutdownPhaseWaitingRemote:
		command, ok := s.commands[state.CommandID]
		if !ok || s.activeCommand != state.CommandID || command.ReplayDisabled {
			s.localShutdown = nil
			s.saveStateLocked()
			s.commandMu.Unlock()
			return
		}
		if upsStatus != nil && upsStatus.RuntimeMinutes > 0 && upsStatus.RuntimeMinutes < s.cfg.LocalShutdown.EmergencyRuntimeMinutes {
			rebroadcastAt := now
			state.Phase = protocol.LocalShutdownPhaseEmergency
			state.Trigger = protocol.LocalShutdownTriggerEmergencyRuntime
			state.LastRebroadcastAt = &rebroadcastAt
			commandID = state.CommandID
			trigger = state.Trigger
			replay = command.Message
			replayNodes = replayableNodeIDs(command)
			s.saveStateLocked()
		} else if state.DeadlineAt != nil && !now.Before(*state.DeadlineAt) {
			state.Phase = protocol.LocalShutdownPhaseWaitExpired
			state.Trigger = protocol.LocalShutdownTriggerWaitExpired
			commandID = state.CommandID
			trigger = state.Trigger
			s.saveStateLocked()
		}
	case protocol.LocalShutdownPhaseWaitExpired:
		commandID = state.CommandID
		trigger = protocol.LocalShutdownTriggerWaitExpired
	case protocol.LocalShutdownPhaseEmergency:
		commandID = state.CommandID
		trigger = protocol.LocalShutdownTriggerEmergencyRuntime
	}
	s.commandMu.Unlock()

	if len(replayNodes) > 0 {
		log.Printf("local shutdown emergency replay command_id=%s nodes=%d", replay.CommandID, len(replayNodes))
		s.replayShutdownToNodes(replay, replayNodes)
	}
	if commandID != "" {
		if trigger == protocol.LocalShutdownTriggerWaitExpired {
			log.Printf("local shutdown wait expired command_id=%s", commandID)
		}
		s.beginLocalShutdownExecution(commandID, trigger)
	}
}

func (s *Server) beginLocalShutdownExecution(commandID, trigger string) {
	command := append([]string(nil), s.cfg.LocalShutdown.Command...)

	s.commandMu.Lock()
	if s.localShutdown == nil || s.localShutdown.CommandID != commandID || localShutdownExecutionStarted(s.localShutdown.Phase) {
		s.commandMu.Unlock()
		return
	}
	s.localShutdown.Phase = protocol.LocalShutdownPhaseExecuting
	s.localShutdown.Trigger = trigger
	s.localShutdown.LastError = ""
	s.saveStateLocked()
	s.commandMu.Unlock()

	log.Printf("local shutdown starting command_id=%s trigger=%s", commandID, trigger)
	err := s.localShutdownRunner(command, trigger)

	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	if s.localShutdown == nil || s.localShutdown.CommandID != commandID {
		return
	}
	if err != nil {
		s.localShutdown.Phase = protocol.LocalShutdownPhaseFailed
		s.localShutdown.LastError = err.Error()
		log.Printf("local shutdown failed command_id=%s trigger=%s err=%v", commandID, trigger, err)
	} else {
		s.localShutdown.Phase = protocol.LocalShutdownPhaseCompleted
		s.localShutdown.LastError = ""
	}
	s.saveStateLocked()
}

func (s *Server) runLocalShutdownCommand(command []string, trigger string) error {
	if len(command) == 0 {
		return fmt.Errorf("local shutdown command is empty")
	}
	if s.cfg.DryRun {
		log.Printf("dry-run local shutdown trigger=%s command=%v", trigger, command)
		return nil
	}
	return exec.Command(command[0], command[1:]...).Run()
}

func (s *Server) replayShutdownToNodes(message protocol.ShutdownMessage, nodeIDs map[string]struct{}) {
	for _, client := range s.registry.List() {
		if _, ok := nodeIDs[client.NodeID]; !ok {
			continue
		}
		go func(client *Client) {
			if err := client.Send(protocol.TypeShutdown, message); err != nil {
				log.Printf("rebroadcast shutdown to %s failed: %v", client.NodeID, err)
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

func localShutdownExecutionStarted(phase string) bool {
	switch phase {
	case protocol.LocalShutdownPhaseExecuting, protocol.LocalShutdownPhaseCompleted, protocol.LocalShutdownPhaseFailed:
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
		Enabled: true,
		Phase:   protocol.LocalShutdownPhaseIdle,
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
