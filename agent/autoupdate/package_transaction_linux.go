package autoupdate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"printmaster/common/updatetrust"
)

const packageTransactionFile = "native-transaction.json"

// RepositoryPackageTransaction is root-owned native installation evidence.
// Inspection is not authorization to retry, clear or roll back this run.
type RepositoryPackageTransaction struct {
	Schema          int                       `json:"schema"`
	RunID           string                    `json:"run_id"`
	Target          updatetrust.Target        `json:"target"`
	PreviousVersion string                    `json:"previous_version"`
	Phase           string                    `json:"phase"`
	StartedAt       time.Time                 `json:"started_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
	Startup         *RepositoryPackageStartup `json:"startup,omitempty"`
	RepairedVersion string                    `json:"repaired_version,omitempty"`
	path            string
}

// InspectPrivilegedRepositoryPackage reads existing evidence offline under the
// intake lock. The state directory comes from trusted helper configuration.
func InspectPrivilegedRepositoryPackage(ctx context.Context, stateDir string) (RepositoryPackageTransaction, error) {
	var transaction RepositoryPackageTransaction
	err := withRootPackageState(stateDir, false, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		transaction, err = readPackageTransaction(stateDir)
		return err
	})
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	return transaction, nil
}

func readPackageTransaction(stateDir string) (transaction RepositoryPackageTransaction, result error) {
	return readPackageTransactionFile(filepath.Join(stateDir, packageTransactionFile))
}

func readPackageTransactionFile(path string) (transaction RepositoryPackageTransaction, result error) {
	file, err := os.Open(path)
	if err != nil {
		return transaction, fmt.Errorf("open native package transaction: %w", err)
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	const limit = 16 * 1024
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return transaction, fmt.Errorf("read native package transaction: %w", err)
	}
	if len(data) > limit {
		return transaction, fmt.Errorf("native package transaction exceeds %d bytes", limit)
	}
	return decodePackageTransaction(data, path)
}

func decodePackageTransaction(data []byte, path string) (transaction RepositoryPackageTransaction, result error) {
	if err := json.Unmarshal(data, &transaction); err != nil {
		return transaction, fmt.Errorf("decode native package transaction: %w", err)
	}
	// Records are machine-written canonical JSON. Reject unknown/duplicate
	// fields and hand-edited ambiguous evidence rather than guessing intent.
	canonical, err := json.Marshal(transaction)
	if err != nil || !bytes.Equal(data, canonical) {
		return transaction, fmt.Errorf("native package transaction is not canonical")
	}
	id, idErr := hex.DecodeString(transaction.RunID)
	digest, digestErr := hex.DecodeString(transaction.Target.SHA256)
	target := transaction.Target
	current, currentErr := parseSemverVersion(transaction.PreviousVersion)
	candidate, candidateErr := parseSemverVersion(target.Version)
	if transaction.Schema != 1 || idErr != nil || len(id) != 16 || hex.EncodeToString(id) != transaction.RunID ||
		digestErr != nil || len(digest) != 32 || hex.EncodeToString(digest) != target.SHA256 ||
		target.Component != "agent" || target.Channel != "stable" || target.Platform != "linux" ||
		target.Arch != runtime.GOARCH || (target.Format != "deb" && target.Format != "rpm") ||
		target.SizeBytes <= 0 || target.SizeBytes > maxRepositoryPackageSize ||
		!stablePackageVersion.MatchString(target.Version) || !stablePackageVersion.MatchString(transaction.PreviousVersion) ||
		currentErr != nil || candidateErr != nil || !candidate.GreaterThan(current) ||
		transaction.StartedAt.IsZero() || transaction.UpdatedAt.Before(transaction.StartedAt) {
		return transaction, fmt.Errorf("native package transaction identity or ordering is invalid")
	}
	switch transaction.Phase {
	case "installing", "awaiting-startup", "recovery-required":
		if transaction.Startup != nil {
			return transaction, fmt.Errorf("unconfirmed native package transaction contains startup evidence")
		}
	case "startup-confirmed":
		if transaction.Startup == nil || !transaction.Startup.valid() {
			return transaction, fmt.Errorf("confirmed native package transaction lacks valid startup evidence")
		}
	case "owner-repaired":
		if transaction.Startup == nil || !transaction.Startup.valid() ||
			(transaction.RepairedVersion != transaction.PreviousVersion && transaction.RepairedVersion != transaction.Target.Version) {
			return transaction, fmt.Errorf("owner-repaired native transaction lacks exact package/service evidence")
		}
	default:
		return transaction, fmt.Errorf("unknown native package transaction phase %q", transaction.Phase)
	}
	if transaction.Phase != "owner-repaired" && transaction.RepairedVersion != "" {
		return transaction, fmt.Errorf("unrepaired transaction contains repair evidence")
	}
	transaction.path = path
	return transaction, nil
}

// The caller holds the root intake lock for this entire transaction. Existing
// records, even malformed ones, require explicit reconciliation, never replay.
func newPackageTransaction(stateDir string, target updatetrust.Target) (*RepositoryPackageTransaction, error) {
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
	return &RepositoryPackageTransaction{Schema: 1, RunID: hex.EncodeToString(id[:]), Target: target, path: path}, nil
}

func (j *RepositoryPackageTransaction) record(previous, phase string) error {
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
	if filepath.Dir(j.path) == packageHelperStateDir {
		return writeRepositoryPackageObservation(packageHelperResultPath, *j)
	}
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
