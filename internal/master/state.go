package master

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"

	"nut-server/internal/atomicfile"
	"nut-server/internal/protocol"
)

type persistedState struct {
	CommandSeq          uint64                           `json:"command_seq"`
	ActiveCommand       string                           `json:"active_command,omitempty"`
	AutoShutdownLatched bool                             `json:"auto_shutdown_latched,omitempty"`
	LocalShutdown       *localShutdownState              `json:"local_shutdown,omitempty"`
	Commands            map[string]*shutdownCommandState `json:"commands,omitempty"`
	Nodes               map[string]*NodeMeta             `json:"nodes,omitempty"`
}

func (s *Server) loadState() {
	if s.cfg.StateFile == "" {
		return
	}
	content, err := os.ReadFile(s.cfg.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		slog.Error("load master state failed", "path", s.cfg.StateFile, "err", err)
		return
	}
	var state persistedState
	if err := json.Unmarshal(content, &state); err != nil {
		slog.Error("decode master state failed", "path", s.cfg.StateFile, "err", err)
		return
	}
	if state.Commands == nil {
		state.Commands = make(map[string]*shutdownCommandState)
	}
	s.commands = state.Commands
	s.activeCommand = state.ActiveCommand
	s.autoShutdownLatched = state.AutoShutdownLatched
	s.localShutdown = persistedLocalShutdownState(state.LocalShutdown)
	s.commandSeq.Store(state.CommandSeq)
	s.shutdownIssued.Store(state.ActiveCommand != "")
	s.normalizeLoadedLocalShutdown()
	if state.Nodes != nil {
		s.directory.replaceAll(state.Nodes)
	}
}

func (s *Server) saveStateForDirectoryChange() {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	s.saveStateLocked()
}

func (s *Server) saveStateLocked() {
	if s.cfg.StateFile == "" {
		return
	}
	state := persistedState{
		CommandSeq:          s.commandSeq.Load(),
		ActiveCommand:       s.activeCommand,
		AutoShutdownLatched: s.autoShutdownLatched,
		LocalShutdown:       persistedLocalShutdownState(s.localShutdown),
		Commands:            s.commands,
		Nodes:               s.directory.snapshotForPersist(),
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		slog.Error("encode master state failed", "err", err)
		return
	}
	if err := atomicfile.WriteFile(s.cfg.StateFile, content, 0o600); err != nil {
		slog.Error("write master state failed", "path", s.cfg.StateFile, "err", err)
	}
}

func (s *Server) normalizeLoadedLocalShutdown() {
	if !s.cfg.LocalShutdown.Enabled || s.localShutdown == nil {
		s.localShutdown = nil
		return
	}
	switch s.localShutdown.Phase {
	case protocol.LocalShutdownPhaseWaitingRemote, protocol.LocalShutdownPhaseWaitExpired, protocol.LocalShutdownPhaseEmergency:
	default:
		s.localShutdown = nil
		return
	}
	if s.localShutdown.CommandID == "" {
		s.localShutdown = nil
		return
	}
	command, ok := s.commands[s.localShutdown.CommandID]
	if !ok || s.activeCommand != s.localShutdown.CommandID || command.ReplayDisabled {
		s.localShutdown = nil
	}
}

func persistedLocalShutdownState(state *localShutdownState) *localShutdownState {
	if state == nil {
		return nil
	}
	switch state.Phase {
	case protocol.LocalShutdownPhaseWaitingRemote, protocol.LocalShutdownPhaseWaitExpired, protocol.LocalShutdownPhaseEmergency:
	default:
		return nil
	}
	return &localShutdownState{
		Phase:             state.Phase,
		CommandID:         state.CommandID,
		StartedAt:         copyTime(state.StartedAt),
		DeadlineAt:        copyTime(state.DeadlineAt),
		Trigger:           state.Trigger,
		LastRebroadcastAt: copyTime(state.LastRebroadcastAt),
		LastError:         state.LastError,
	}
}
