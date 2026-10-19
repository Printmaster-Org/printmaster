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
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type memoryPinnedReader struct {
	store  *memoryPublicationStore
	reads  int
	before func(string) error
}

type pinnedReaderFunc func(context.Context, string, string) ([]byte, error)

func (f pinnedReaderFunc) GetPinned(ctx context.Context, name, pin string) ([]byte, error) {
	return f(ctx, name, pin)
}

func (r *memoryPinnedReader) GetPinned(ctx context.Context, name, hash string) ([]byte, error) {
	r.reads++
	if r.before != nil {
		if err := r.before(name); err != nil {
			return nil, err
		}
	}
	data, err := r.store.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if digest(data) != hash {
		return nil, fmt.Errorf("pinned object not found")
	}
	return data, nil
}

func restoreFixture(t *testing.T) (*testRepository, RestoreOptions, *memoryPinnedReader) {
	t.Helper()
	repo, delivery, recovery, _ := deliveryFixture(t)
	delivery.Bootstrap, delivery.ExpectedPreviousSHA256 = true, ""
	if err := DeliverPublication(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	recordData := recovery.files["records/"+delivery.ExpectedTimestampSHA256+".json"]
	reader := &memoryPinnedReader{store: recovery}
	return repo, RestoreOptions{
		Directory: t.TempDir(), RecordDirectory: t.TempDir(), Root: delivery.Root,
		ExpectedTimestampSHA256: delivery.ExpectedTimestampSHA256, ExpectedRecordSHA256: digest(recordData),
		ExternalSerialization: true, Recovery: reader,
	}, reader
}

func TestPinnedRestorePreservesHistoryAndAdvancesPublisher(t *testing.T) {
	t.Parallel()
	repo, opts, reader := restoreFixture(t)
	writes := len(reader.store.writes)
	if err := RestorePublication(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if writes != len(reader.store.writes) {
		t.Fatal("restore wrote remote objects")
	}
	recordData := readPublication(t, opts.Directory, "record.json")
	if digest(recordData) != opts.ExpectedRecordSHA256 ||
		!bytes.Equal(recordData, readPublication(t, opts.RecordDirectory, opts.ExpectedTimestampSHA256+".json")) {
		t.Fatal("workspace and high-water records differ from independent pin")
	}
	var record publicationRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	for name, hash := range record.Files {
		if digest(readPublication(t, opts.Directory, name)) != hash {
			t.Fatalf("restored history changed: %s", name)
		}
	}
	if err := checkPublicationHighWater(opts.Directory, opts.RecordDirectory); err != nil {
		t.Fatal(err)
	}
	managed := TransactionOptions{
		Publish:         PublishOptions{Directory: opts.Directory, Root: opts.Root, Keys: repo.keys},
		BackupDirectory: t.TempDir(), RecordDirectory: opts.RecordDirectory, MirrorDirectory: t.TempDir(),
	}
	if err := PublishTransaction(managed); err != nil {
		t.Fatal(err)
	}
	version, err := metadataTimestampVersion(readPublication(t, opts.Directory, "timestamp.json"))
	if err != nil || version != record.TimestampVersion+1 {
		t.Fatalf("restored publisher reset or skipped timestamp version: %d, %v", version, err)
	}
}

func TestPinnedRestoreRejectsInvalidStateWithoutOutput(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing timestamp pin", "missing record pin", "wrong record pin",
		"wrong root", "not serialized", "missing object", "tampered object", "unsafe record name",
		"wrong record timestamp", "wrong record version", "wrong authority version", "missing root history", "missing current dependency",
		"invalid signature", "too many files",
		"mismatched retained records", "corrupt json", "nonempty workspace", "nonempty records",
		"overlapping directories", "linked directory", "lock conflict", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			_, opts, reader := restoreFixture(t)
			prefix := "publications/" + opts.ExpectedTimestampSHA256 + "/"
			recordData := reader.store.files["records/"+opts.ExpectedTimestampSHA256+".json"]
			var record publicationRecord
			if err := json.Unmarshal(recordData, &record); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch scenario {
			case "missing timestamp pin":
				opts.ExpectedTimestampSHA256 = ""
			case "missing record pin":
				opts.ExpectedRecordSHA256 = ""
			case "wrong record pin":
				opts.ExpectedRecordSHA256 = strings.Repeat("a", 64)
			case "wrong root":
				opts.Root = readPublication(t, newTestRepository(t).dir, "1.root.json")
			case "not serialized":
				opts.ExternalSerialization = false
			case "missing object":
				delete(reader.store.files, prefix+"1.root.json")
			case "tampered object":
				reader.store.files[prefix+"1.root.json"] = []byte("{}")
			case "unsafe record name":
				record.Files["../root.pem"] = strings.Repeat("a", 64)
			case "wrong record timestamp":
				record.TimestampSHA256 = strings.Repeat("a", 64)
			case "wrong record version":
				record.TimestampVersion++
			case "wrong authority version":
				opts.ExpectedTimestampVersion = record.TimestampVersion + 1
			case "missing root history":
				delete(record.Files, "1.root.json")
			case "missing current dependency":
				for name := range record.Files {
					if strings.HasSuffix(name, ".snapshot.json") {
						delete(record.Files, name)
					}
				}
			case "invalid signature":
				timestamp, err := metadata.Timestamp().FromBytes(reader.store.files[prefix+"timestamp.json"])
				if err != nil {
					t.Fatal(err)
				}
				timestamp.Signatures = nil
				data, err := timestamp.ToBytes(false)
				if err != nil {
					t.Fatal(err)
				}
				record.TimestampSHA256 = digest(data)
				record.Files["timestamp.json"] = digest(data)
				newPrefix := "publications/" + record.TimestampSHA256 + "/"
				for name := range record.Files {
					reader.store.files[newPrefix+name] = reader.store.files[prefix+name]
				}
				reader.store.files[newPrefix+"timestamp.json"] = data
				prefix = newPrefix
				opts.ExpectedTimestampSHA256 = record.TimestampSHA256
			case "too many files":
				for i := 1; i <= 4097; i++ {
					record.Files[fmt.Sprintf("%d.root.json", i)] = strings.Repeat("a", 64)
				}
			case "mismatched retained records":
				reader.store.files[prefix+"record.json"] = []byte("different")
			case "corrupt json":
				recordData = []byte("{")
				opts.ExpectedRecordSHA256 = digest(recordData)
				reader.store.files["records/"+opts.ExpectedTimestampSHA256+".json"] = recordData
				reader.store.files[prefix+"record.json"] = recordData
			case "nonempty workspace":
				if err := os.WriteFile(filepath.Join(opts.Directory, "existing"), []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "nonempty records":
				if err := os.WriteFile(filepath.Join(opts.RecordDirectory, "existing"), []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "overlapping directories":
				opts.RecordDirectory = opts.Directory
			case "linked directory":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(opts.Directory, link); err != nil {
					t.Skipf("directory symlinks unavailable: %v", err)
				}
				opts.Directory = link
			case "lock conflict":
				if err := os.WriteFile(filepath.Join(opts.Directory, ".publisher.lock"), []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			switch scenario {
			case "unsafe record name", "wrong record timestamp", "wrong record version", "missing root history", "missing current dependency", "invalid signature", "too many files":
				changed, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				opts.ExpectedRecordSHA256 = digest(changed)
				reader.store.files["records/"+opts.ExpectedTimestampSHA256+".json"] = changed
				reader.store.files[prefix+"record.json"] = changed
			}
			if err := RestorePublication(ctx, opts); err == nil {
				t.Fatal("invalid restoration accepted")
			}
			if _, err := os.Stat(filepath.Join(opts.Directory, "timestamp.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failure left an apparently restored timestamp: %v", err)
			}
			if scenario == "lock conflict" && string(readPublication(t, opts.Directory, ".publisher.lock")) != "foreign" {
				t.Fatal("foreign lock altered")
			}
		})
	}
}

func TestPinnedRestoreExpiredRoutineCanRefreshButCannotDeliver(t *testing.T) {
	t.Parallel()
	repo, opts, reader := restoreFixture(t)
	prefix := "publications/" + opts.ExpectedTimestampSHA256 + "/"
	var record publicationRecord
	if err := json.Unmarshal(reader.store.files[prefix+"record.json"], &record); err != nil {
		t.Fatal(err)
	}
	timestamp, err := metadata.Timestamp().FromBytes(reader.store.files[prefix+"timestamp.json"])
	if err != nil {
		t.Fatal(err)
	}
	timestamp.Signatures = nil
	timestamp.Signed.Expires = time.Now().Add(-time.Hour)
	if err := signMetadata(timestamp, repo.keys["timestamp"]); err != nil {
		t.Fatal(err)
	}
	data, err := timestamp.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	record.TimestampSHA256 = digest(data)
	record.Files["timestamp.json"] = digest(data)
	recordData, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	newPrefix := "publications/" + record.TimestampSHA256 + "/"
	for name := range record.Files {
		reader.store.files[newPrefix+name] = reader.store.files[prefix+name]
	}
	reader.store.files[newPrefix+"timestamp.json"] = data
	reader.store.files[newPrefix+"record.json"] = recordData
	reader.store.files["records/"+record.TimestampSHA256+".json"] = recordData
	opts.ExpectedTimestampSHA256, opts.ExpectedRecordSHA256 = record.TimestampSHA256, digest(recordData)
	if err := RestorePublication(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := DeliverPublication(context.Background(), DeliveryOptions{
		Directory: opts.Directory, Root: opts.Root, ExpectedTimestampSHA256: opts.ExpectedTimestampSHA256,
		Bootstrap: true, ExternalSerialization: true, Recovery: newMemoryStore(), Public: newMemoryStore(),
	}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("restored expired metadata was deliverable: %v", err)
	}
	if err := PublishTransaction(TransactionOptions{
		Publish:         PublishOptions{Directory: opts.Directory, Root: opts.Root, Keys: repo.keys},
		BackupDirectory: t.TempDir(), RecordDirectory: opts.RecordDirectory, MirrorDirectory: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIncompleteRestoreBlocksEveryPublishPath(t *testing.T) {
	t.Parallel()
	repo, opts, reader := restoreFixture(t)
	reader.before = func(name string) error {
		if strings.HasSuffix(name, "/timestamp.json") {
			// Obstruct the first local write after remote authentication.
			return os.Mkdir(filepath.Join(opts.RecordDirectory, opts.ExpectedTimestampSHA256+".json"), 0o700)
		}
		return nil
	}
	if err := RestorePublication(context.Background(), opts); err == nil {
		t.Fatal("local write failure ignored")
	}
	if _, err := os.Stat(filepath.Join(opts.Directory, ".restore-incomplete")); err != nil {
		t.Fatal("missing durable incomplete marker", err)
	}
	publish := PublishOptions{Directory: opts.Directory, Root: opts.Root, Keys: repo.keys}
	if err := Publish(publish); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("low-level publisher bypassed incomplete restore: %v", err)
	}
	if err := PublishTransaction(TransactionOptions{
		Publish: publish, BackupDirectory: t.TempDir(), RecordDirectory: opts.RecordDirectory, MirrorDirectory: t.TempDir(),
	}); err == nil {
		t.Fatal("managed publisher bypassed incomplete restore")
	}
	if err := RestorePublication(context.Background(), opts); err == nil {
		t.Fatal("partial state silently reset by retry")
	}
}

func TestRestoreRejectsAggregateBytesBeforeLocalWrites(t *testing.T) {
	t.Parallel()
	_, opts, reader := restoreFixture(t)
	prefix := "publications/" + opts.ExpectedTimestampSHA256 + "/"
	var record publicationRecord
	if err := json.Unmarshal(reader.store.files[prefix+"record.json"], &record); err != nil {
		t.Fatal(err)
	}
	// Reuse one immutable buffer to exercise the 256 MiB accounting without
	// allocating or writing a full oversized publication to the shared host.
	large := bytes.Repeat([]byte("x"), int(MaxMetadataBytes))
	largePin := digest(large)
	for i := 100; i < 116; i++ {
		record.Files[fmt.Sprintf("%d.targets.json", i)] = largePin
	}
	recordData, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	opts.ExpectedRecordSHA256 = digest(recordData)
	largeReads := 0
	opts.Recovery = pinnedReaderFunc(func(ctx context.Context, name, pin string) ([]byte, error) {
		if name == prefix+"record.json" || name == "records/"+opts.ExpectedTimestampSHA256+".json" {
			return recordData, nil
		}
		if pin == largePin {
			largeReads++
			return large, nil
		}
		return reader.GetPinned(ctx, name, pin)
	})
	if err := RestorePublication(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "256 MiB") {
		t.Fatalf("aggregate limit not enforced: %v", err)
	}
	if largeReads != 16 {
		t.Fatalf("aggregate limit triggered at wrong boundary: %d large reads", largeReads)
	}
	for _, directory := range []string{opts.Directory, opts.RecordDirectory} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("oversized restore wrote local output: %v, %d entries", err, len(entries))
		}
	}
}
