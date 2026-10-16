package updatetrust

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// PublishTarget ties a local release artifact to its signed identity. File is
// build/publishing input only and never appears in remote metadata.
type PublishTarget struct {
	Target Target `json:"target"`
	File   string `json:"file"`
}

type PublishOptions struct {
	Directory string
	Root      []byte
	Keys      map[string]ed25519.PrivateKey
	Targets   []PublishTarget
	Now       time.Time
}

// Publish updates routine metadata without accessing a root private key.
// The caller must exclusively own the publishing workspace. Retain its versioned
// files and publish timestamp.json last when uploading to the remote repository.
func Publish(opts PublishOptions) error {
	if opts.Directory == "" {
		return fmt.Errorf("publisher metadata directory required")
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	root, err := metadata.Root().FromBytes(opts.Root)
	if err != nil {
		return err
	}
	if err := root.VerifyDelegate("root", root); err != nil {
		return err
	}
	if !root.Signed.Expires.After(now) {
		return fmt.Errorf("publisher root expired; offline root maintenance required")
	}
	if !root.Signed.ConsistentSnapshot {
		return fmt.Errorf("publisher requires TUF consistent snapshots")
	}
	targets := metadata.Targets(now.Add(180 * 24 * time.Hour))
	snapshot := metadata.Snapshot(now.Add(30 * 24 * time.Hour))
	timestamp := metadata.Timestamp(now.Add(7 * 24 * time.Hour))
	if err := restorePublication(opts.Directory, root, targets, snapshot, timestamp); err != nil {
		return fmt.Errorf("read publisher state: %w", err)
	}
	for role, version := range map[string]*int64{"targets": &targets.Signed.Version, "snapshot": &snapshot.Signed.Version} {
		next, err := unusedVersion(opts.Directory, role, *version)
		if err != nil {
			return err
		}
		*version = next
	}
	for _, entry := range opts.Targets {
		path, err := entry.Target.Path()
		if err != nil {
			return err
		}
		info, err := metadata.TargetFile().FromFile(entry.File, "sha256")
		if err != nil {
			return err
		}
		if info.Length <= 0 || info.Length != entry.Target.SizeBytes || hex.EncodeToString(info.Hashes["sha256"]) != entry.Target.SHA256 {
			return fmt.Errorf("publisher input does not match artifact %s", path)
		}
		if old := targets.Signed.Targets[path]; old != nil &&
			(old.Length != info.Length || hex.EncodeToString(old.Hashes["sha256"]) != entry.Target.SHA256) {
			return fmt.Errorf("refusing to replace immutable publisher target %s", path)
		}
		targets.Signed.Targets[path] = info
	}
	if err := signMetadata(targets, opts.Keys["targets"]); err != nil {
		return err
	}
	targetBytes, err := targets.ToBytes(false)
	if err != nil {
		return err
	}
	snapshot.Signed.Meta["targets.json"] = metadataInfo(targets.Signed.Version, targetBytes)
	if err := signMetadata(snapshot, opts.Keys["snapshot"]); err != nil {
		return err
	}
	snapshotBytes, err := snapshot.ToBytes(false)
	if err != nil {
		return err
	}
	timestamp.Signed.Meta["snapshot.json"] = metadataInfo(snapshot.Signed.Version, snapshotBytes)
	if err := signMetadata(timestamp, opts.Keys["timestamp"]); err != nil {
		return err
	}
	// Verify authorization before any output is published; a valid signature
	// by an unrelated key must never be mistaken for an authorized release.
	if err := root.VerifyDelegate("targets", targets); err != nil {
		return err
	}
	if err := root.VerifyDelegate("snapshot", snapshot); err != nil {
		return err
	}
	if err := root.VerifyDelegate("timestamp", timestamp); err != nil {
		return err
	}
	timestampBytes, err := timestamp.ToBytes(false)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Directory, 0o700); err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{
		{fmt.Sprintf("%d.root.json", root.Signed.Version), opts.Root},
		{fmt.Sprintf("%d.targets.json", targets.Signed.Version), targetBytes},
		{fmt.Sprintf("%d.snapshot.json", snapshot.Signed.Version), snapshotBytes},
		{"timestamp.json", timestampBytes},
	} {
		path := filepath.Join(opts.Directory, file.name)
		if file.name != "timestamp.json" {
			previous, err := os.ReadFile(path)
			if err == nil {
				if string(previous) != string(file.data) {
					return fmt.Errorf("refusing to overwrite immutable metadata %s", file.name)
				}
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := writeMetadata(path, file.data); err != nil {
			return err
		}
	}
	return nil
}

// Timestamp is the publication commit point. Reconstruct state from its signed
// versioned dependencies, never from independently overwritten working copies.
func restorePublication(directory string, root *metadata.Metadata[metadata.RootType], targets *metadata.Metadata[metadata.TargetsType], snapshot *metadata.Metadata[metadata.SnapshotType], timestamp *metadata.Metadata[metadata.TimestampType]) error {
	data, err := os.ReadFile(filepath.Join(directory, "timestamp.json"))
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), "targets.json") || strings.HasSuffix(entry.Name(), "snapshot.json") {
				return fmt.Errorf("timestamp missing from existing publisher state; restore the last publication")
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	oldTimestamp, err := metadata.Timestamp().FromBytes(data)
	if err != nil {
		return err
	}
	if err := root.VerifyDelegate("timestamp", oldTimestamp); err != nil {
		return err
	}
	readDependency := func(role string, info *metadata.MetaFiles) ([]byte, error) {
		if info == nil || info.Version <= 0 {
			return nil, fmt.Errorf("missing %s publication reference", role)
		}
		data, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("%d.%s.json", info.Version, role)))
		if err != nil {
			return nil, err
		}
		if err := info.VerifyLengthHashes(data); err != nil {
			return nil, err
		}
		return data, nil
	}
	snapshotInfo := oldTimestamp.Signed.Meta["snapshot.json"]
	data, err = readDependency("snapshot", snapshotInfo)
	if err != nil {
		return err
	}
	oldSnapshot, err := metadata.Snapshot().FromBytes(data)
	if err != nil {
		return err
	}
	if err := root.VerifyDelegate("snapshot", oldSnapshot); err != nil {
		return err
	}
	if oldSnapshot.Signed.Version != snapshotInfo.Version {
		return fmt.Errorf("snapshot publication version mismatch")
	}
	targetsInfo := oldSnapshot.Signed.Meta["targets.json"]
	data, err = readDependency("targets", targetsInfo)
	if err != nil {
		return err
	}
	oldTargets, err := metadata.Targets().FromBytes(data)
	if err != nil {
		return err
	}
	if err := root.VerifyDelegate("targets", oldTargets); err != nil {
		return err
	}
	if oldTargets.Signed.Version != targetsInfo.Version {
		return fmt.Errorf("targets publication version mismatch")
	}
	targets.Signed.Targets = oldTargets.Signed.Targets
	targets.Signed.Version, snapshot.Signed.Version = oldTargets.Signed.Version+1, oldSnapshot.Signed.Version+1
	timestamp.Signed.Version = oldTimestamp.Signed.Version + 1
	if targets.Signed.Version <= 0 || snapshot.Signed.Version <= 0 || timestamp.Signed.Version <= 0 {
		return fmt.Errorf("publisher metadata version overflow")
	}
	return nil
}

func unusedVersion(directory, role string, next int64) (int64, error) {
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	for _, entry := range entries {
		suffix := "." + role + ".json"
		if !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		version, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), suffix), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid versioned metadata filename %s", entry.Name())
		}
		if version >= next {
			next = version + 1
			if next <= 0 {
				return 0, fmt.Errorf("publisher metadata version overflow")
			}
		}
	}
	return next, nil
}

func metadataInfo(version int64, data []byte) *metadata.MetaFiles {
	hash := sha256.Sum256(data)
	return &metadata.MetaFiles{Version: version, Length: int64(len(data)), Hashes: metadata.Hashes{"sha256": hash[:]}}
}

func signMetadata[T metadata.Roles](value *metadata.Metadata[T], key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("missing or invalid routine publisher signing key")
	}
	signer, err := signature.LoadSigner(key, crypto.Hash(0))
	if err != nil {
		return err
	}
	_, err = value.Sign(signer)
	return err
}

func writeMetadata(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".tuf-publish-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// LoadSigningKey accepts an explicitly provided PKCS#8 Ed25519 PEM key.
// Key provisioning/encryption belongs to the release owner's secret store.
func LoadSigningKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		return nil, fmt.Errorf("publisher key must be a single PKCS#8 PRIVATE KEY PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("publisher key must be Ed25519")
	}
	return edKey, nil
}
