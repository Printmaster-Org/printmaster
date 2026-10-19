package updatetrust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type TransactionOptions struct {
	Publish         PublishOptions
	BackupDirectory string
	RecordDirectory string
	MirrorDirectory string
	Resume          bool
}

type publicationRecord struct {
	RootSHA256              string            `json:"root_sha256"`
	TimestampSHA256         string            `json:"timestamp_sha256"`
	PreviousTimestampSHA256 string            `json:"previous_timestamp_sha256"`
	TimestampVersion        int64             `json:"timestamp_version"`
	Files                   map[string]string `json:"files"`
}

// PublishTransaction is a local, single-host publishing boundary. All roots
// must be pre-provisioned private/trusted directories on reliable local storage.
// Exclusive lock files deliberately survive a crash; never auto-break a lock.
func PublishTransaction(opts TransactionOptions) (result error) {
	directories := []string{opts.Publish.Directory, opts.BackupDirectory, opts.RecordDirectory, opts.MirrorDirectory}
	for i, directory := range directories {
		if directory == "" {
			return fmt.Errorf("workspace, backup, record and mirror directories required")
		}
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return err
		}
		directories[i] = absolute
		if err := ordinaryAncestors(absolute); err != nil {
			return err
		}
	}
	for i, left := range directories {
		for _, right := range directories[i+1:] {
			relative, err := filepath.Rel(left, right)
			if err != nil {
				continue
			} // Different volumes cannot overlap.
			reverse, err := filepath.Rel(right, left)
			if err != nil {
				return err
			}
			outside := func(path string) bool {
				return path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator))
			}
			if !outside(relative) || !outside(reverse) {
				return fmt.Errorf("transaction directories must be separate, non-nested locations")
			}
		}
	}
	locks := append([]string(nil), directories...)
	sort.Strings(locks)
	var acquired []string
	defer func() {
		for _, lock := range acquired {
			if err := os.Remove(lock); err != nil {
				result = errors.Join(result, fmt.Errorf("release publisher lock: %w", err))
			}
		}
	}()
	for _, directory := range locks {
		path := filepath.Join(directory, ".publisher.lock")
		if err := writeExclusivePublication(path, []byte(fmt.Sprintf("pid=%d\n", os.Getpid()))); err != nil {
			return fmt.Errorf("acquire publisher lock %s: %w; inspect ownership before manually removing a stale lock", path, err)
		}
		acquired = append(acquired, path)
	}
	workspace, backup, records, mirror := directories[0], directories[1], directories[2], directories[3]
	stage := filepath.Join(workspace, ".pending-publication")
	marker := filepath.Join(stage, "record.json")
	if !opts.Resume {
		if err := checkPublicationHighWater(workspace, records); err != nil {
			return err
		}
		if err := os.Mkdir(stage, 0o700); err != nil {
			return fmt.Errorf("create pending publication: %w; use explicit resume for a sealed pending transaction", err)
		}
		files, err := publicationFiles(workspace)
		if err != nil {
			return err
		}
		if err := copyPublication(stage, files, false); err != nil {
			return err
		}
		publish := opts.Publish
		publish.Directory = stage
		if err := Publish(publish); err != nil {
			return fmt.Errorf("prepare pending publication: %w; preserve pending output for review", err)
		}
		files, err = publicationFiles(stage)
		if err != nil {
			return err
		}
		previous, err := optionalTimestamp(workspace)
		if err != nil {
			return err
		}
		timestamp, err := metadata.Timestamp().FromBytes(files["timestamp.json"])
		if err != nil {
			return err
		}
		record := publicationRecord{RootSHA256: digest(opts.Publish.Root), TimestampSHA256: digest(files["timestamp.json"]),
			TimestampVersion:        timestamp.Signed.Version,
			PreviousTimestampSHA256: previous, Files: make(map[string]string)}
		for name, data := range files {
			record.Files[name] = digest(data)
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := writeExclusivePublication(marker, data); err != nil {
			return err
		}
	}
	now := opts.Publish.Now
	if now.IsZero() {
		now = time.Now()
	}
	data, files, record, err := readSealedPublication(stage, opts.Publish.Root, now)
	if err != nil {
		return err
	}
	if opts.Resume {
		if err := checkPublicationHighWater(workspace, records, record.PreviousTimestampSHA256, record.TimestampSHA256); err != nil {
			return err
		}
	}
	for _, directory := range []string{workspace, mirror} {
		current, err := optionalTimestamp(directory)
		if err != nil {
			return err
		}
		if current != record.TimestampSHA256 && current != record.PreviousTimestampSHA256 && !(directory == mirror && current == "") {
			return fmt.Errorf("publication destination diverged from the sealed transaction: %s", directory)
		}
	}
	backupPath := filepath.Join(backup, record.TimestampSHA256)
	if err := os.Mkdir(backupPath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := ordinaryAncestors(backupPath); err != nil {
		return err
	}
	if err := copyPublication(backupPath, files, false); err != nil {
		return fmt.Errorf("backup publication: %w", err)
	}
	if err := ensurePublication(filepath.Join(backupPath, "record.json"), data); err != nil {
		return err
	}
	if err := ensurePublication(filepath.Join(records, record.TimestampSHA256+".json"), data); err != nil {
		return fmt.Errorf("record publication: %w", err)
	}
	// No visible commit is changed before backup and record writes are synced
	// and read back. Timestamp is always the last metadata file replaced.
	if err := copyPublication(workspace, files, true); err != nil {
		return fmt.Errorf("commit workspace: %w", err)
	}
	if err := copyPublication(mirror, files, true); err != nil {
		return fmt.Errorf("distribute publication: %w; resume the sealed transaction", err)
	}
	for name := range files {
		if err := os.Remove(filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	return os.Remove(stage)
}

func readSealedPublication(directory string, trustedRoot []byte, now time.Time) ([]byte, map[string][]byte, publicationRecord, error) {
	fail := func(err error) ([]byte, map[string][]byte, publicationRecord, error) {
		return nil, nil, publicationRecord{}, err
	}
	if err := ordinaryAncestors(directory); err != nil {
		return fail(err)
	}
	data, err := readPublisherMetadata(filepath.Join(directory, "record.json"))
	if err != nil {
		return fail(fmt.Errorf("pending publication is not sealed; authorized manual recovery required: %w", err))
	}
	files, err := publicationFiles(directory)
	if err != nil {
		return fail(err)
	}
	record, err := validateSealedPublication(data, files, trustedRoot, now, false)
	if err != nil {
		return fail(err)
	}
	return data, files, record, nil
}

func validateSealedPublication(data []byte, files map[string][]byte, trustedRoot []byte, now time.Time, allowExpiredRoutine bool) (publicationRecord, error) {
	fail := func(err error) (publicationRecord, error) { return publicationRecord{}, err }
	var record publicationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return fail(err)
	}
	if record.RootSHA256 != digest(trustedRoot) || record.TimestampSHA256 != digest(files["timestamp.json"]) ||
		len(record.Files) != len(files) {
		return fail(fmt.Errorf("pending publication does not match its protected record/root"))
	}
	for name, body := range files {
		if record.Files[name] != digest(body) {
			return fail(fmt.Errorf("pending metadata changed: %s", name))
		}
	}
	if err := ValidateBootstrapRoot(trustedRoot, now); err != nil {
		return fail(err)
	}
	root, err := metadata.Root().FromBytes(trustedRoot)
	if err != nil {
		return fail(err)
	}
	read := func(name string) ([]byte, error) {
		data, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("sealed publication missing %s", name)
		}
		return data, nil
	}
	if err := verifyRootHistory(root, len(files), read); err != nil {
		return fail(err)
	}
	targets, snapshot, timestamp := metadata.Targets(), metadata.Snapshot(), metadata.Timestamp()
	if err := restorePublicationData(files["timestamp.json"], read, root, targets, snapshot, timestamp); err != nil {
		return fail(err)
	}
	sealedTimestamp, err := metadata.Timestamp().FromBytes(files["timestamp.json"])
	if err != nil {
		return fail(err)
	}
	if sealedTimestamp.Signed.Version != record.TimestampVersion {
		return fail(fmt.Errorf("sealed timestamp version does not match record"))
	}
	if !allowExpiredRoutine {
		if err := deliveryExpiry(files, now); err != nil {
			return fail(err)
		}
	}
	return record, nil
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func ordinaryAncestors(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("publisher location must be an ordinary trusted directory: %s", path)
		}
		if err := checkPublisherDirectory(path); err != nil {
			return err
		}
		if filepath.Dir(path) == path {
			return nil
		}
		path = filepath.Dir(path)
	}
}

func publicationFiles(directory string) (map[string][]byte, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte)
	for _, entry := range entries {
		if entry.Name() == ".publisher.lock" || entry.Name() == ".pending-publication" || entry.Name() == "record.json" {
			continue
		}
		if !ValidMetadataName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unexpected publisher file %s", entry.Name())
		}
		data, err := readPublisherMetadata(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = data
	}
	return files, nil
}

func copyPublication(directory string, files map[string][]byte, replaceTimestamp bool) error {
	names := make([]string, 0, len(files))
	for name := range files {
		if name != "timestamp.json" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := files["timestamp.json"]; ok {
		names = append(names, "timestamp.json")
	}
	for _, name := range names {
		path := filepath.Join(directory, name)
		if name == "timestamp.json" && replaceTimestamp {
			if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("unsafe timestamp destination")
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := writeMetadata(path, files[name]); err != nil {
				return err
			}
			actual, err := readPublisherMetadata(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(actual, files[name]) {
				return fmt.Errorf("timestamp read-back mismatch")
			}
		} else if err := ensurePublication(path, files[name]); err != nil {
			return err
		}
	}
	return nil
}

func optionalTimestamp(directory string) (string, error) {
	data, err := readPublisherMetadata(filepath.Join(directory, "timestamp.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return digest(data), nil
}

func ensurePublication(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe publication destination %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := writeExclusivePublication(path, data); err != nil {
			return err
		}
	}
	actual, err := readPublisherMetadata(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, data) {
		return fmt.Errorf("immutable publication conflict: %s", path)
	}
	return nil
}

func writeExclusivePublication(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
