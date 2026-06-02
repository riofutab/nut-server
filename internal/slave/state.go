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

// snapshotStateLocked encodes command state while commandMu is held and returns
// a monotonic sequence plus the bytes to write; the caller releases commandMu
// before calling persistState so the fsync stays off the lock. content is nil
// when no state file is configured or encoding fails.
func (c *Client) snapshotStateLocked() (uint64, []byte) {
	if c.cfg.StateFile == "" {
		return 0, nil
	}
	content, err := json.MarshalIndent(c.commandStates, "", "  ")
	if err != nil {
		slog.Error("encode slave state failed", "err", err)
		return 0, nil
	}
	c.persistSeq++
	return c.persistSeq, content
}

// persistState writes a snapshot to disk, serialized by persistMu and applied in
// snapshot order so a slow writer cannot clobber newer state. No-op when content
// is nil. Callers must not hold commandMu (except saveStateLocked, used by tests).
func (c *Client) persistState(seq uint64, content []byte) {
	if content == nil {
		return
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	if seq <= c.persistedSeq {
		return
	}
	if err := atomicfile.WriteFile(c.cfg.StateFile, content, 0o600); err != nil {
		slog.Error("write slave state failed", "path", c.cfg.StateFile, "err", err)
		return
	}
	c.persistedSeq = seq
}

// saveStateLocked persists synchronously while commandMu is held; retained for
// callers already holding the lock that are not contended (currently tests).
func (c *Client) saveStateLocked() {
	seq, content := c.snapshotStateLocked()
	c.persistState(seq, content)
}
