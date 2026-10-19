package updatetrust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type PinnedPublicationReader interface {
	GetPinned(context.Context, string, string) ([]byte, error)
}

type RestoreOptions struct {
	Directory, RecordDirectory string
	Root                       []byte
	// Both pins must come from independently held latest-safe owner records.
	// The record pin also authenticates retained historical/orphan file hashes
	// that the current timestamp's signed dependency chain does not cover.
	ExpectedTimestampSHA256 string
	ExpectedRecordSHA256    string
	ExternalSerialization   bool
	Recovery                PinnedPublicationReader
}

// RestorePublication reads remote public metadata only. It requires two existing,
// empty protected local directories; it never repairs or resets existing state.
// Expired routine metadata may be restored for authenticated refresh, not served.
func RestorePublication(ctx context.Context, opts RestoreOptions) (result error) {
	if !opts.ExternalSerialization || opts.Recovery == nil ||
		!validPin(opts.ExpectedTimestampSHA256) || !validPin(opts.ExpectedRecordSHA256) {
		return fmt.Errorf("restore requires external serialization, recovery reader and independently held latest timestamp/record SHA256 pins")
	}
	if err := ValidateBootstrapRoot(opts.Root, time.Now()); err != nil {
		return err
	}
	dirs := []string{opts.Directory, opts.RecordDirectory}
	for i, directory := range dirs {
		if directory == "" {
			return fmt.Errorf("restore workspace and records directories required")
		}
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return err
		}
		if err := ordinaryAncestors(absolute); err != nil {
			return err
		}
		dirs[i] = absolute
	}
	for _, pair := range [][2]string{{dirs[0], dirs[1]}, {dirs[1], dirs[0]}} {
		if !strings.EqualFold(filepath.VolumeName(pair[0]), filepath.VolumeName(pair[1])) {
			continue
		}
		relative, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("restore workspace and records must be separate, non-nested directories")
		}
	}
	locks := append([]string(nil), dirs...)
	sort.Strings(locks)
	var acquired []string
	defer func() {
		for _, path := range acquired {
			if err := os.Remove(path); err != nil {
				result = errors.Join(result, fmt.Errorf("release restore lock: %w", err))
			}
		}
	}()
	for _, directory := range locks {
		path := filepath.Join(directory, ".publisher.lock")
		if err := writeExclusivePublication(path, []byte(fmt.Sprintf("restore pid=%d\n", os.Getpid()))); err != nil {
			return fmt.Errorf("acquire restore lock; do not auto-break stale locks: %w", err)
		}
		acquired = append(acquired, path)
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		if len(entries) != 1 || entries[0].Name() != ".publisher.lock" {
			return fmt.Errorf("restore requires empty directories; preserve existing/partial state for owner review")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	read := func(name, pin string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := opts.Recovery.GetPinned(ctx, name, pin)
		if err != nil {
			return nil, fmt.Errorf("read pinned recovery object %s: %w", name, err)
		}
		if len(data) == 0 || len(data) > int(MaxMetadataBytes) || digest(data) != pin {
			return nil, fmt.Errorf("recovery object length/hash mismatch: %s", name)
		}
		return data, nil
	}
	recordData, err := read("records/"+opts.ExpectedTimestampSHA256+".json", opts.ExpectedRecordSHA256)
	if err != nil {
		return err
	}
	prefix := "publications/" + opts.ExpectedTimestampSHA256 + "/"
	copyRecord, err := read(prefix+"record.json", opts.ExpectedRecordSHA256)
	if err != nil {
		return err
	}
	if !bytes.Equal(recordData, copyRecord) {
		return fmt.Errorf("retained recovery records disagree")
	}
	var record publicationRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		return fmt.Errorf("decode pinned recovery record: %w", err)
	}
	if record.TimestampSHA256 != opts.ExpectedTimestampSHA256 || record.RootSHA256 != digest(opts.Root) ||
		record.TimestampVersion <= 0 || len(record.Files) == 0 || len(record.Files) > 4096 {
		return fmt.Errorf("pinned record identity/version/file count invalid")
	}
	names := make([]string, 0, len(record.Files))
	for name, hash := range record.Files {
		if !ValidMetadataName(name) || !validPin(hash) {
			return fmt.Errorf("pinned record contains invalid metadata name or hash")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	files := make(map[string][]byte, len(names))
	total := len(recordData) + len(copyRecord)
	for _, name := range names {
		data, err := read(prefix+name, record.Files[name])
		if err != nil {
			return err
		}
		total += len(data)
		if total > 256<<20 {
			return fmt.Errorf("restored publication exceeds 256 MiB aggregate limit")
		}
		files[name] = data
	}
	if _, err := validateSealedPublication(recordData, files, opts.Root, time.Now(), true); err != nil {
		return fmt.Errorf("authenticate restored publication: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A marker makes partial local output ineligible for normal publishing.
	// Never auto-delete it or reinterpret a partial restore as an empty repo.
	marker := filepath.Join(dirs[0], ".restore-incomplete")
	if err := writeExclusivePublication(marker, []byte(opts.ExpectedRecordSHA256+"\n")); err != nil {
		return err
	}
	if err := ensurePublication(filepath.Join(dirs[1], record.TimestampSHA256+".json"), recordData); err != nil {
		return fmt.Errorf("restore high-water record; preserve incomplete output: %w", err)
	}
	if err := copyPublication(dirs[0], files, false); err != nil {
		return fmt.Errorf("restore workspace; preserve incomplete output: %w", err)
	}
	if err := ensurePublication(filepath.Join(dirs[0], "record.json"), recordData); err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil {
		return fmt.Errorf("finish restore marker; authorized owner review required: %w", err)
	}
	return nil
}
