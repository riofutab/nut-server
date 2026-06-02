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
	seq, content := s.snapshotStateLocked()
	s.commandMu.Unlock()
	s.persistState(seq, content)
}

// snapshotStateLocked encodes the full server state while commandMu is held and
// returns a monotonically increasing sequence number plus the encoded bytes. The
// caller must release commandMu and hand the result to persistState, so the
// expensive double fsync in atomicfile.WriteFile happens off the critical
// section and no longer blocks readers (Status) or the 1s command watcher tick.
// content is nil when no state file is configured or encoding fails.
func (s *Server) snapshotStateLocked() (uint64, []byte) {
	if s.cfg.StateFile == "" {
		return 0, nil
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
		return 0, nil
	}
	s.persistSeq++
	return s.persistSeq, content
}

// persistState writes an encoded snapshot to disk, serialized by persistMu and
// applied in snapshot order: a snapshot older than one already written is
// dropped so a slow writer cannot clobber newer state. It is a no-op when
// content is nil. Callers MUST NOT hold commandMu (the production hot paths
// snapshot, unlock, then persist); the only exception is saveStateLocked, used
// by tests that persist synchronously while holding the lock.
func (s *Server) persistState(seq uint64, content []byte) {
	if content == nil {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if seq <= s.persistedSeq {
		return
	}
	if err := atomicfile.WriteFile(s.cfg.StateFile, content, 0o600); err != nil {
		slog.Error("write master state failed", "path", s.cfg.StateFile, "err", err)
		return
	}
	s.persistedSeq = seq
}

// saveStateLocked persists synchronously while commandMu is held. Production hot
// paths instead snapshot under the lock and call persistState after unlocking;
// this convenience remains for callers already holding the lock that are not on
// a contended path (currently only tests).
func (s *Server) saveStateLocked() {
	seq, content := s.snapshotStateLocked()
	s.persistState(seq, content)
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
	// Keep a pending local shutdown even if activeCommand was already cleared on
	// completion before the restart — the watcher still needs to power the
	// master off. Only an absent or reset command cancels it.
	if !ok || command.ReplayDisabled {
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
