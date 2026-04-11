package master

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
)

type persistedState struct {
	CommandSeq    uint64                           `json:"command_seq"`
	ActiveCommand string                           `json:"active_command,omitempty"`
	Commands      map[string]*shutdownCommandState `json:"commands,omitempty"`
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
		log.Printf("load master state failed: %v", err)
		return
	}
	var state persistedState
	if err := json.Unmarshal(content, &state); err != nil {
		log.Printf("decode master state failed: %v", err)
		return
	}
	if state.Commands == nil {
		state.Commands = make(map[string]*shutdownCommandState)
	}
	s.commands = state.Commands
	s.activeCommand = state.ActiveCommand
	s.commandSeq.Store(state.CommandSeq)
	s.shutdownIssued.Store(state.ActiveCommand != "")
}

func (s *Server) saveStateLocked() {
	if s.cfg.StateFile == "" {
		return
	}
	state := persistedState{
		CommandSeq:    s.commandSeq.Load(),
		ActiveCommand: s.activeCommand,
		Commands:      s.commands,
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("encode master state failed: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.StateFile), 0o755); err != nil {
		log.Printf("create master state dir failed: %v", err)
		return
	}
	if err := os.WriteFile(s.cfg.StateFile, content, 0o644); err != nil {
		log.Printf("write master state failed: %v", err)
	}
}
