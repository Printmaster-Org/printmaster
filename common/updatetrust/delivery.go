package updatetrust

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// PublicationStore must distinguish an absent object (fs.ErrNotExist) from
// unreadable/hidden/corrupt state. Put must not hide an ambiguous upload failure.
type PublicationStore interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
}

// PublicationIdentity contains public recovery pins, never signing material.
type PublicationIdentity struct {
	RootSHA256      string `json:"root_sha256"`
	TimestampSHA256 string `json:"timestamp_sha256"`
	RecordSHA256    string `json:"record_sha256"`
	Version         int64  `json:"timestamp_version"`
}

// PublicationCustody reserves a single exact publication before any remote
// delivery writes. It is not a distributed lock on the metadata buckets.
type PublicationCustody interface {
	Prepare(context.Context, PublicationIdentity, string, bool) (string, error)
	Complete(context.Context, string, PublicationIdentity) error
}

type DeliveryOptions struct {
	Directory string
	Root      []byte
	// ExpectedTimestampSHA256 pins the exact sealed publication being delivered.
	ExpectedTimestampSHA256 string
	// Optional caller pin used when independently recording the entire sealed
	// inventory, including historical files outside the current signed chain.
	ExpectedRecordSHA256 string
	// ExpectedPreviousSHA256 must come from the independently held last remote
	// publication record. Local signing may be ahead of remote distribution.
	ExpectedPreviousSHA256 string
	Bootstrap              bool
	// ExternalSerialization is an explicit caller assertion, not a remote lock.
	ExternalSerialization bool
	Recovery, Public      PublicationStore
	Custody               PublicationCustody
}

// DeliverPublication transfers an already sealed signed publication. It cannot
// sign, select the latest safe recovery state, or serialize cross-host writers.
func DeliverPublication(ctx context.Context, opts DeliveryOptions) error {
	if !opts.ExternalSerialization || opts.Recovery == nil || opts.Public == nil {
		return fmt.Errorf("remote delivery requires externally serialized writers and separate recovery/public stores")
	}
	if !validPin(opts.ExpectedTimestampSHA256) || (opts.Bootstrap && opts.ExpectedPreviousSHA256 != "") ||
		(!opts.Bootstrap && !validPin(opts.ExpectedPreviousSHA256)) {
		return fmt.Errorf("exact publication pin and either explicit bootstrap or independently recorded previous remote pin required")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Read once; only these in-memory, authenticated bytes are ever uploaded.
	recordData, files, record, err := readSealedPublication(opts.Directory, opts.Root, time.Now())
	if err != nil {
		return err
	}
	if record.TimestampSHA256 != opts.ExpectedTimestampSHA256 {
		return fmt.Errorf("sealed publication differs from the explicitly pinned delivery")
	}
	if opts.ExpectedRecordSHA256 != "" && (!validPin(opts.ExpectedRecordSHA256) || digest(recordData) != opts.ExpectedRecordSHA256) {
		return fmt.Errorf("sealed publication record differs from the explicitly pinned inventory")
	}
	current, err := opts.Public.Get(ctx, "timestamp.json")
	if errors.Is(err, fs.ErrNotExist) {
		if !opts.Bootstrap {
			return fmt.Errorf("remote timestamp missing; do not reset established publisher state")
		}
		current = nil
	} else if err != nil {
		return fmt.Errorf("read remote commit: %w", err)
	} else if digest(current) != record.TimestampSHA256 &&
		(opts.Bootstrap || digest(current) != opts.ExpectedPreviousSHA256) {
		return fmt.Errorf("remote timestamp diverged from independently pinned previous/current publication")
	}
	if len(current) > 0 && digest(current) != record.TimestampSHA256 {
		previous, parseErr := metadataTimestampVersion(current)
		if parseErr != nil || previous >= record.TimestampVersion {
			return fmt.Errorf("remote timestamp version must advance; rollback/conflict requires owner recovery")
		}
	}
	identity := PublicationIdentity{
		RootSHA256: record.RootSHA256, TimestampSHA256: record.TimestampSHA256,
		RecordSHA256: digest(recordData), Version: record.TimestampVersion,
	}
	var reservation string
	if opts.Custody != nil {
		reservation, err = opts.Custody.Prepare(ctx, identity, opts.ExpectedPreviousSHA256, opts.Bootstrap)
		if err != nil {
			return fmt.Errorf("reserve independent publication record: %w", err)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if name != "timestamp.json" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	all := append(append([]string(nil), names...), "timestamp.json")
	for _, name := range all {
		if err := ensureRemote(ctx, opts.Recovery, "publications/"+record.TimestampSHA256+"/"+name, files[name]); err != nil {
			return fmt.Errorf("retain recovery metadata %s: %w", name, err)
		}
	}
	if err := ensureRemote(ctx, opts.Recovery, "publications/"+record.TimestampSHA256+"/record.json", recordData); err != nil {
		return fmt.Errorf("retain sealed recovery record: %w", err)
	}
	// A content-addressed retained record is not an independently held latest
	// pointer. The owner/orchestrator must retain that pin separately.
	if err := ensureRemote(ctx, opts.Recovery, "records/"+record.TimestampSHA256+".json", recordData); err != nil {
		return fmt.Errorf("retain publication record: %w", err)
	}
	for _, name := range names {
		if err := ensureRemote(ctx, opts.Public, name, files[name]); err != nil {
			return fmt.Errorf("distribute immutable metadata %s: %w", name, err)
		}
	}
	// Recheck before the commit point. This detects intervening writes but is
	// not compare-and-swap; external single-writer ownership is mandatory.
	check, err := opts.Public.Get(ctx, "timestamp.json")
	if errors.Is(err, fs.ErrNotExist) && opts.Bootstrap && len(current) == 0 {
		check, err = nil, nil
	}
	if err != nil || !bytes.Equal(current, check) {
		return fmt.Errorf("remote commit changed during delivery; preserve sealed state for explicit retry")
	}
	if err := ValidateBootstrapRoot(opts.Root, time.Now()); err != nil {
		return err
	}
	// The initial validation may predate a slow upload. Revalidate all metadata
	// expiry immediately before committing, without rereading mutable inputs.
	if err := deliveryExpiry(files, time.Now()); err != nil {
		return err
	}
	if !bytes.Equal(current, files["timestamp.json"]) {
		if err := opts.Public.Put(ctx, "timestamp.json", files["timestamp.json"]); err != nil {
			return fmt.Errorf("timestamp commit failed or ambiguous; explicitly retry the same sealed publication: %w", err)
		}
	}
	actual, err := opts.Public.Get(ctx, "timestamp.json")
	if err != nil {
		return fmt.Errorf("timestamp read-back failed; explicitly retry the same publication: %w", err)
	}
	if !bytes.Equal(actual, files["timestamp.json"]) {
		return fmt.Errorf("timestamp read-back mismatch; preserve sealed publication for owner review")
	}
	if opts.Custody != nil {
		if err := opts.Custody.Complete(ctx, reservation, identity); err != nil {
			return fmt.Errorf("remote delivery verified but independent record completion failed or ambiguous; retry only this same sealed publication: %w", err)
		}
	}
	return nil
}

func validPin(pin string) bool {
	if len(pin) != 64 {
		return false
	}
	for _, c := range pin {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func ensureRemote(ctx context.Context, store PublicationStore, name string, data []byte) error {
	actual, err := store.Get(ctx, name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := store.Put(ctx, name, data); err != nil {
			return err
		}
		actual, err = store.Get(ctx, name)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, data) {
		return fmt.Errorf("immutable object conflict or read-back mismatch")
	}
	return nil
}

func metadataTimestampVersion(data []byte) (int64, error) {
	timestamp, err := metadata.Timestamp().FromBytes(data)
	if err != nil {
		return 0, err
	}
	return timestamp.Signed.Version, nil
}

func deliveryExpiry(files map[string][]byte, now time.Time) error {
	timestamp, err := metadata.Timestamp().FromBytes(files["timestamp.json"])
	if err != nil {
		return err
	}
	info := timestamp.Signed.Meta["snapshot.json"]
	if info == nil {
		return fmt.Errorf("sealed timestamp missing snapshot")
	}
	snapshot, err := metadata.Snapshot().FromBytes(files[fmt.Sprintf("%d.snapshot.json", info.Version)])
	if err != nil {
		return err
	}
	info = snapshot.Signed.Meta["targets.json"]
	if info == nil {
		return fmt.Errorf("sealed snapshot missing targets")
	}
	targets, err := metadata.Targets().FromBytes(files[fmt.Sprintf("%d.targets.json", info.Version)])
	if err != nil {
		return err
	}
	if !timestamp.Signed.Expires.After(now) || !snapshot.Signed.Expires.After(now) || !targets.Signed.Expires.After(now) {
		return fmt.Errorf("sealed publication expired; do not distribute stale metadata")
	}
	return nil
}
