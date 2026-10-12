package slave

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"nut-server/internal/atomicfile"
	"nut-server/internal/protocol"
)

type persistedState struct {
	// IssuedToken is the per-node token the master issued at enrollment; when
	// set it is used to register instead of the config token.
	IssuedToken string                                 `json:"issued_token,omitempty"`
	Commands    map[string]protocol.ShutdownAckMessage `json:"commands,omitempty"`
}

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
	var state persistedState
	if err := json.Unmarshal(content, &state); err != nil {
		c.moveCorruptState(err)
		return
	}
	if state.Commands != nil {
		c.commandStates = state.Commands
	}
	c.issuedToken = state.IssuedToken
}

// moveCorruptState renames an undecodable state file aside so the next persist
// does not overwrite it: it may hold the issued token, which an operator can
// then recover by hand instead of re-enrolling the node.
func (c *Client) moveCorruptState(decodeErr error) {
	movedTo := fmt.Sprintf("%s.corrupt-%s", c.cfg.StateFile, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(c.cfg.StateFile, movedTo); err != nil {
		slog.Error("slave state file is corrupt and could not be moved aside; starting with empty state",
			"path", c.cfg.StateFile, "decode_err", decodeErr, "err", err)
		return
	}
	slog.Error("slave state file is corrupt; moved it aside and starting with empty state, so a previously issued token is not used",
		"path", c.cfg.StateFile, "moved_to", movedTo, "err", decodeErr)
}

// setIssuedToken stores (or with "" clears) the issued token and writes it to
// disk before returning. On a write failure the previous token is restored, so
// the slave never registers with a token it could lose on restart.
func (c *Client) setIssuedToken(token string) error {
	c.commandMu.Lock()
	previous := c.issuedToken
	c.issuedToken = token
	seq, content := c.snapshotStateLocked()
	if content == nil {
		c.issuedToken = previous
		c.commandMu.Unlock()
		return fmt.Errorf("cannot persist issued token: no usable state_file")
	}
	c.commandMu.Unlock()
	if err := c.persistState(seq, content); err != nil {
		c.commandMu.Lock()
		if c.issuedToken == token {
			c.issuedToken = previous
		}
		c.commandMu.Unlock()
		return err
	}
	return nil
}

// registerToken returns the token to register with and whether it is the
// issued token.
func (c *Client) registerToken() (string, bool) {
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	if c.issuedToken != "" {
		return c.issuedToken, true
	}
	return c.cfg.Token, false
}

// snapshotStateLocked encodes command state while commandMu is held and returns
// a monotonic sequence plus the bytes to write; the caller releases commandMu
// before calling persistState so the fsync stays off the lock. content is nil
// when no state file is configured or encoding fails.
func (c *Client) snapshotStateLocked() (uint64, []byte) {
	if c.cfg.StateFile == "" {
		return 0, nil
	}
	content, err := json.Marshal(persistedState{IssuedToken: c.issuedToken, Commands: c.commandStates})
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
// A write error is logged and returned; command-state callers rely on the log.
func (c *Client) persistState(seq uint64, content []byte) error {
	if content == nil {
		return nil
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	if seq <= c.persistedSeq {
		return nil
	}
	if err := atomicfile.WriteFile(c.cfg.StateFile, content, 0o600); err != nil {
		slog.Error("write slave state failed", "path", c.cfg.StateFile, "err", err)
		return err
	}
	c.persistedSeq = seq
	return nil
}

// saveStateLocked persists synchronously while commandMu is held; retained for
// callers already holding the lock that are not contended (currently tests).
func (c *Client) saveStateLocked() {
	seq, content := c.snapshotStateLocked()
	_ = c.persistState(seq, content)
}
