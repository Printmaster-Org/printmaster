package updatetrust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrTrustState requires explicit recovery, not an automatic bootstrap reset.
var ErrTrustState = errors.New("publisher trust state requires recovery")

var trustedFiles = [...]string{"root.json", "timestamp.json", "snapshot.json", "targets.json"}

type trustState struct {
	Version int               `json:"version"`
	Hashes  map[string]string `json:"sha256"`
}

func (c *Client) statePath() string {
	return c.options.CacheDir + ".state.json"
}

// The sibling record survives deletion of the metadata cache. It detects local
// loss/corruption; TUF still owns signature, expiry and version verification.
// Both locations require protected storage and one exclusive client owner.
func (c *Client) prepareTrustState() error {
	data, err := os.ReadFile(c.statePath())
	if errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(c.options.CacheDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect metadata directory: %w", ErrTrustState, err)
		}
		if len(entries) != 0 {
			return fmt.Errorf("%w: integrity record missing for existing metadata", ErrTrustState)
		}
		if err := os.MkdirAll(c.options.CacheDir, 0o700); err != nil {
			return fmt.Errorf("%w: create metadata directory: %w", ErrTrustState, err)
		}
		sum := sha256.Sum256(c.options.TrustedRoot)
		state := trustState{Version: 1, Hashes: map[string]string{"root.json": hex.EncodeToString(sum[:])}}
		// Record initialization before creating cache state. A failed root write
		// must not turn the next invocation into a fresh trust bootstrap.
		if err := c.writeTrustState(state); err != nil {
			return err
		}
		if err := writeMetadata(filepath.Join(c.options.CacheDir, "root.json"), c.options.TrustedRoot); err != nil {
			return fmt.Errorf("%w: initialize root cache: %w", ErrTrustState, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read integrity record: %w", ErrTrustState, err)
	}
	var state trustState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%w: decode integrity record: %w", ErrTrustState, err)
	}
	if state.Version != 1 || state.Hashes["root.json"] == "" {
		return fmt.Errorf("%w: invalid integrity record", ErrTrustState)
	}
	actual, err := c.readTrustState()
	if err != nil {
		return err
	}
	if len(actual.Hashes) != len(state.Hashes) {
		return fmt.Errorf("%w: trusted metadata files missing or unexpectedly present", ErrTrustState)
	}
	for name, expected := range state.Hashes {
		if actual.Hashes[name] != expected {
			return fmt.Errorf("%w: trusted metadata changed: %s", ErrTrustState, name)
		}
	}
	return nil
}

func (c *Client) readTrustState() (trustState, error) {
	state := trustState{Version: 1, Hashes: make(map[string]string)}
	for _, name := range trustedFiles {
		path := filepath.Join(c.options.CacheDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return state, fmt.Errorf("%w: inspect %s: %w", ErrTrustState, name, err)
		}
		if !info.Mode().IsRegular() {
			return state, fmt.Errorf("%w: metadata must be a regular file: %s", ErrTrustState, name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return state, fmt.Errorf("%w: read %s: %w", ErrTrustState, name, err)
		}
		sum := sha256.Sum256(data)
		state.Hashes[name] = hex.EncodeToString(sum[:])
	}
	if state.Hashes["root.json"] == "" {
		return state, fmt.Errorf("%w: trusted root missing", ErrTrustState)
	}
	return state, nil
}

func (c *Client) writeTrustState(state trustState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: encode integrity record: %w", ErrTrustState, err)
	}
	if err := writeMetadata(c.statePath(), data); err != nil {
		return fmt.Errorf("%w: persist integrity record: %w", ErrTrustState, err)
	}
	return nil
}

func (c *Client) checkpointTrustState() error {
	state, err := c.readTrustState()
	if err != nil {
		return err
	}
	return c.writeTrustState(state)
}
