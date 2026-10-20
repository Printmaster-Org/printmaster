package autoupdate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"printmaster/common/updatetrust"
)

const packageTransactionFile = "native-transaction.json"

type packageTransaction struct {
	Schema          int                `json:"schema"`
	RunID           string             `json:"run_id"`
	Target          updatetrust.Target `json:"target"`
	PreviousVersion string             `json:"previous_version"`
	Phase           string             `json:"phase"`
	StartedAt       time.Time          `json:"started_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	path            string
}

// The caller holds the root intake lock for this entire transaction. Existing
// records, even malformed ones, require explicit reconciliation, never replay.
func newPackageTransaction(stateDir string, target updatetrust.Target) (*packageTransaction, error) {
	path := filepath.Join(stateDir, packageTransactionFile)
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("native package transaction requires reconciliation before another install: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect native package transaction: %w", err)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("create native transaction identifier: %w", err)
	}
	return &packageTransaction{Schema: 1, RunID: hex.EncodeToString(id[:]), Target: target, path: path}, nil
}

func (j *packageTransaction) record(previous, phase string) error {
	switch phase {
	case "installing":
		if j.Phase != "" || !stablePackageVersion.MatchString(previous) {
			return fmt.Errorf("invalid native installation intent transition")
		}
	case "awaiting-startup", "recovery-required":
		if j.Phase != "installing" || j.PreviousVersion != previous {
			return fmt.Errorf("invalid native installation outcome transition")
		}
	default:
		return fmt.Errorf("unknown native installation phase %q", phase)
	}
	next := *j
	next.PreviousVersion, next.Phase, next.UpdatedAt = previous, phase, time.Now().UTC()
	if next.StartedAt.IsZero() {
		next.StartedAt = next.UpdatedAt
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := writePackageTransaction(j.path, data); err != nil {
		return err
	}
	*j = next
	return nil
}

// File and directory sync failures are fatal. If rename succeeded but the
// directory sync failed, the visible record is retained and blocks retry.
func writePackageTransaction(path string, data []byte) (result error) {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, dir.Close()) }()
	file, err := os.CreateTemp(filepath.Dir(path), ".native-transaction-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return dir.Sync()
}
