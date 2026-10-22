package updatetrust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type ClientTrustRole struct {
	File    string    `json:"file"`
	Version int64     `json:"version"`
	Expires time.Time `json:"expires"`
	Expired bool      `json:"expired"`
}

type ClientTrustInspection struct {
	CacheDir         string            `json:"cache_dir"`
	CheckpointSHA256 string            `json:"checkpoint_sha256"`
	VerifiedAt       time.Time         `json:"verified_at"`
	LocalUTC         time.Time         `json:"local_utc"`
	IntegrityOK      bool              `json:"integrity_ok"`
	Roles            []ClientTrustRole `json:"roles"`
}

func clientTrustLocation(cacheDir string) (string, error) {
	if strings.TrimSpace(cacheDir) == "" {
		return "", fmt.Errorf("client metadata directory required")
	}
	path, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", err
	}
	if filepath.Dir(path) == path {
		return "", fmt.Errorf("client metadata directory cannot be a filesystem root")
	}
	if err := ordinaryAncestors(filepath.Dir(path)); err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := ordinaryAncestors(path); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func readClientCheckpoint(cacheDir string) ([]byte, trustState, error) {
	data, err := readClientTrustFile(cacheDir + ".state.json")
	if err != nil {
		return nil, trustState{}, fmt.Errorf("%w: retained checkpoint required: %w", ErrTrustState, err)
	}
	state, err := decodeTrustState(data)
	return data, state, err
}

func exactClientMetadata(directory string, state trustState) (map[string][]byte, error) {
	actual, err := (&Client{options: Options{CacheDir: directory}}).readTrustState()
	if err != nil {
		return nil, err
	}
	if len(actual.Hashes) != len(state.Hashes) {
		return nil, fmt.Errorf("%w: metadata presence differs from retained checkpoint", ErrTrustState)
	}
	files := make(map[string][]byte)
	for name, hash := range state.Hashes {
		data, err := readClientTrustFile(filepath.Join(directory, name))
		if err != nil || digest(data) != hash {
			return nil, fmt.Errorf("%w: %s does not match retained checkpoint: %v", ErrTrustState, name, err)
		}
		files[name] = data
	}
	return files, nil
}

// InspectClientTrust is offline and read-only. Integrity is not authorization:
// cached expiry is diagnostic and never substitutes for a normal TUF refresh.
func InspectClientTrust(cacheDir string) (report ClientTrustInspection, result error) {
	report.LocalUTC = time.Now().UTC()
	defer func() { result = diagnoseTrust(result, report.LocalUTC) }()
	path, err := clientTrustLocation(cacheDir)
	if err != nil {
		return report, err
	}
	report.CacheDir = path
	data, state, err := readClientCheckpoint(path)
	if err != nil {
		return report, err
	}
	report.CheckpointSHA256, report.VerifiedAt = digest(data), state.VerifiedAt
	files, err := exactClientMetadata(path, state)
	if err != nil {
		return report, err
	}
	var expired error
	for _, name := range trustedFiles {
		data, exists := files[name]
		if !exists {
			continue
		}
		var envelope struct {
			Signed struct {
				Version int64     `json:"version"`
				Expires time.Time `json:"expires"`
			} `json:"signed"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil || envelope.Signed.Version <= 0 || envelope.Signed.Expires.IsZero() {
			return report, fmt.Errorf("%w: invalid cached metadata header %s: %v", ErrTrustState, name, err)
		}
		report.Roles = append(report.Roles, ClientTrustRole{File: name, Version: envelope.Signed.Version,
			Expires: envelope.Signed.Expires, Expired: report.LocalUTC.After(envelope.Signed.Expires)})
		if report.LocalUTC.After(envelope.Signed.Expires) {
			expired = errors.Join(expired, &metadata.ErrExpiredMetadata{Msg: "cached " + name + " is expired; online refresh may recover"})
		}
	}
	report.IntegrityOK = true
	return report, errors.Join(checkTrustClock(report.LocalUTC, report.VerifiedAt), expired)
}

type ClientTrustRepairOptions struct {
	CacheDir              string
	BackupDir             string
	ExternalSerialization bool
}

// RepairClientTrust restores only bytes already pinned by the retained local
// checkpoint. Callers must stop all verifier writers and protect both locations.
// An interrupted repair can be rerun; the checkpoint and version floors never
// change, and partial writes cannot pass ordinary integrity verification.
func RepairClientTrust(ctx context.Context, opts ClientTrustRepairOptions) (evidence string, result error) {
	defer func() { result = diagnoseTrust(result, time.Now().UTC()) }()
	if !opts.ExternalSerialization {
		return "", fmt.Errorf("stop all verifier writers and assert external serialization before repair")
	}
	cache, err := clientTrustLocation(opts.CacheDir)
	if err != nil {
		return "", err
	}
	backup, err := clientTrustLocation(opts.BackupDir)
	if err != nil {
		return "", err
	}
	for _, pair := range [][2]string{{cache, backup}, {backup, cache}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("repair source and destination must be separate, non-nested directories")
		}
	}
	checkpoint, state, err := readClientCheckpoint(cache)
	if err != nil {
		return "", err
	}
	if err := checkTrustClock(time.Now().UTC(), state.VerifiedAt); err != nil {
		return "", err
	}
	files, err := exactClientMetadata(backup, state)
	if err != nil {
		return "", fmt.Errorf("repair source rejected: %w", err)
	}
	originals := make(map[string][]byte)
	for _, name := range trustedFiles {
		data, err := readClientTrustFile(filepath.Join(cache, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("preserve damaged metadata %s: %w", name, err)
		}
		if _, expected := state.Hashes[name]; !expected {
			return "", fmt.Errorf("%w: unexpected local metadata %s; preserve and investigate, do not discard", ErrTrustState, name)
		}
		originals[name] = data
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	evidence, err = os.MkdirTemp(filepath.Dir(cache), filepath.Base(cache)+".repair-evidence-")
	if err != nil {
		return "", err
	}
	originals["checkpoint.json"] = checkpoint
	for name, data := range originals {
		if err := writeMetadata(filepath.Join(evidence, name), data); err != nil {
			return evidence, fmt.Errorf("preserve repair evidence: %w", err)
		}
	}
	// Recheck the authority immediately before writing. External serialization
	// is still required: this is not a cross-process lock or hostile-FS defense.
	current, _, err := readClientCheckpoint(cache)
	if err != nil || !bytes.Equal(checkpoint, current) {
		return evidence, fmt.Errorf("%w: checkpoint changed during repair: %v", ErrTrustState, err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return evidence, err
	}
	for _, name := range trustedFiles {
		data, exists := files[name]
		if !exists {
			continue
		}
		if err := ctx.Err(); err != nil {
			return evidence, err
		}
		if err := writeMetadata(filepath.Join(cache, name), data); err != nil {
			return evidence, fmt.Errorf("restore %s: %w", name, err)
		}
	}
	_, err = exactClientMetadata(cache, state)
	return evidence, err
}
