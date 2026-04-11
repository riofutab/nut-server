package slave

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"

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
		log.Printf("load slave state failed: %v", err)
		return
	}
	var state map[string]protocol.ShutdownAckMessage
	if err := json.Unmarshal(content, &state); err != nil {
		log.Printf("decode slave state failed: %v", err)
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
		log.Printf("encode slave state failed: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.cfg.StateFile), 0o755); err != nil {
		log.Printf("create slave state dir failed: %v", err)
		return
	}
	if err := os.WriteFile(c.cfg.StateFile, content, 0o644); err != nil {
		log.Printf("write slave state failed: %v", err)
	}
}
