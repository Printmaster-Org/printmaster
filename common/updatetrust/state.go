package updatetrust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrTrustState requires explicit recovery, not an automatic bootstrap reset.
var ErrTrustState = errors.New("publisher trust state requires recovery")

var trustedFiles = [...]string{"root.json", "timestamp.json", "snapshot.json", "targets.json"}

type trustState struct {
	Version    int               `json:"version"`
	Hashes     map[string]string `json:"sha256"`
	VerifiedAt time.Time         `json:"verified_at,omitempty"`
}

func (c *Client) statePath() string {
	return c.options.CacheDir + ".state.json"
}

// The sibling record survives deletion of the metadata cache. It detects local
// loss/corruption; TUF still owns signature, expiry and version verification.
// Both locations require protected storage and one exclusive client owner.
func (c *Client) prepareTrustState() error {
	data, err := readClientTrustFile(c.statePath())
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
	state, err := decodeTrustState(data)
	if err != nil {
		return err
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
	if err := checkTrustClock(time.Now().UTC(), state.VerifiedAt); err != nil {
		return err
	}
	return nil
}

func decodeTrustState(data []byte) (trustState, error) {
	var state trustState
	fields, err := uniqueTrustObject(data)
	if err != nil {
		return state, fmt.Errorf("%w: integrity record fields: %w", ErrTrustState, err)
	}
	for name := range fields {
		if name != "version" && name != "sha256" && name != "verified_at" {
			return state, fmt.Errorf("%w: unknown integrity record field %s", ErrTrustState, name)
		}
	}
	if _, err := uniqueTrustObject(fields["sha256"]); err != nil {
		return state, fmt.Errorf("%w: integrity record hashes: %w", ErrTrustState, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("%w: decode integrity record: %w", ErrTrustState, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return state, fmt.Errorf("%w: trailing integrity record content", ErrTrustState)
	}
	if state.Version != 1 || state.Hashes["root.json"] == "" || len(state.Hashes) > len(trustedFiles) ||
		(!state.VerifiedAt.IsZero() && (state.VerifiedAt.Year() < 1 || state.VerifiedAt.Year() > 9999)) {
		return state, fmt.Errorf("%w: invalid integrity record", ErrTrustState)
	}
	for name, hash := range state.Hashes {
		known := false
		for _, expected := range trustedFiles {
			known = known || name == expected
		}
		sum, err := hex.DecodeString(hash)
		if !known || err != nil || len(sum) != sha256.Size || hash != hex.EncodeToString(sum) {
			return state, fmt.Errorf("%w: invalid integrity record entry %s", ErrTrustState, name)
		}
	}
	return state, nil
}

func uniqueTrustObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || fields[name] != nil {
			return nil, fmt.Errorf("duplicate or invalid JSON object field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON content")
	}
	return fields, nil
}

func readClientTrustFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("client trust state must be an ordinary file: %s", path)
	}
	return readPublisherMetadata(path)
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
		data, err := readClientTrustFile(path)
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

func (c *Client) checkpointTrustState(verifiedAt time.Time) error {
	state, err := c.readTrustState()
	if err != nil {
		return err
	}
	previous, err := readClientTrustFile(c.statePath())
	if err != nil {
		return fmt.Errorf("%w: retain integrity record: %w", ErrTrustState, err)
	}
	prior, err := decodeTrustState(previous)
	if err != nil {
		return err
	}
	state.VerifiedAt = prior.VerifiedAt
	if verifiedAt.After(state.VerifiedAt) {
		state.VerifiedAt = verifiedAt
	}
	return c.writeTrustState(state)
}
