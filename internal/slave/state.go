package slave

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"

	"nut-server/internal/atomicfile"
	"nut-server/internal/protocol"
)

func (c *Client) loadState() {
	if c.cfg.StateFile == "" {
		return
	}
	content, err := os.ReadFile(c.cfg.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		slog.Error("load slave state failed", "path", c.cfg.StateFile, "err", err)
		return
	}
	var state map[string]protocol.ShutdownAckMessage
	if err := json.Unmarshal(content, &state); err != nil {
		slog.Error("decode slave state failed", "path", c.cfg.StateFile, "err", err)
		return
	}
	if state == nil {
		state = make(map[string]protocol.ShutdownAckMessage)
	}
	c.commandStates = state
}

func (c *Client) saveStateLocked() {
	if c.cfg.StateFile == "" {
		return
	}
	content, err := json.MarshalIndent(c.commandStates, "", "  ")
	if err != nil {
		slog.Error("encode slave state failed", "err", err)
		return
	}
	if err := atomicfile.WriteFile(c.cfg.StateFile, content, 0o600); err != nil {
		slog.Error("write slave state failed", "path", c.cfg.StateFile, "err", err)
	}
}
