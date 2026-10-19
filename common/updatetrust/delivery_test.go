package updatetrust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type memoryPublicationStore struct {
	files     map[string][]byte
	writes    []string
	beforeGet func(string) error
	beforePut func(string, []byte) error
}

func newMemoryStore() *memoryPublicationStore {
	return &memoryPublicationStore{files: make(map[string][]byte)}
}

func (s *memoryPublicationStore) Get(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.beforeGet != nil {
		if err := s.beforeGet(name); err != nil {
			return nil, err
		}
	}
	data, ok := s.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return bytes.Clone(data), nil
}

func (s *memoryPublicationStore) Put(ctx context.Context, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.beforePut != nil {
		if err := s.beforePut(name, data); err != nil {
			return err
		}
	}
	s.writes = append(s.writes, name)
	s.files[name] = bytes.Clone(data)
	return nil
}

func deliveryFixture(t *testing.T) (*testRepository, DeliveryOptions, *memoryPublicationStore, *memoryPublicationStore) {
	t.Helper()
	repo, transaction := transactionFixture(t)
	previous := digest(readPublication(t, repo.dir, "timestamp.json"))
	if err := PublishTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	pin := digest(readPublication(t, repo.dir, "timestamp.json"))
	recovery, public := newMemoryStore(), newMemoryStore()
	return repo, DeliveryOptions{
		Directory: filepath.Join(transaction.BackupDirectory, pin), Root: transaction.Publish.Root,
		ExpectedTimestampSHA256: pin, ExpectedPreviousSHA256: previous,
		ExternalSerialization: true, Recovery: recovery, Public: public,
	}, recovery, public
}

func TestRemoteDeliveryOrderingRetryAndRealClient(t *testing.T) {
	t.Parallel()
	repo, opts, recovery, public := deliveryFixture(t)
	opts.ExpectedPreviousSHA256, opts.Bootstrap = "", true
	recordName := "records/" + opts.ExpectedTimestampSHA256 + ".json"
	public.beforePut = func(name string, data []byte) error {
		if _, ok := recovery.files[recordName]; !ok {
			t.Fatal("public write preceded retained recovery record")
		}
		var record publicationRecord
		if err := json.Unmarshal(recovery.files[recordName], &record); err != nil {
			t.Fatal(err)
		}
		for filename, hash := range record.Files {
			if digest(recovery.files["publications/"+opts.ExpectedTimestampSHA256+"/"+filename]) != hash {
				t.Fatal("public write preceded exact recovery bytes")
			}
			if name == "timestamp.json" && filename != "timestamp.json" && digest(public.files[filename]) != hash {
				t.Fatal("timestamp preceded public dependencies")
			}
		}
		return nil
	}
	if err := DeliverPublication(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if public.writes[len(public.writes)-1] != "timestamp.json" {
		t.Fatal("public commit not last")
	}
	recoveryCount, publicCount := len(recovery.writes), len(public.writes)
	if err := DeliverPublication(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if recoveryCount != len(recovery.writes) || publicCount != len(public.writes) {
		t.Fatal("identical retry uploaded new versions")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := public.files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	client, err := NewClient(Options{TrustedRoot: opts.Root, MetadataURL: server.URL, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteDeliveryFailureAndResume(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"backup failure", "record failure", "backup conflict", "backup readback",
		"public dependency", "public conflict", "timestamp before commit", "timestamp ambiguous",
		"timestamp readback", "timestamp mismatch", "divergent commit"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			_, opts, recovery, public := deliveryFixture(t)
			opts.ExpectedPreviousSHA256, opts.Bootstrap = "", true
			stop := errors.New("injected failure")
			switch scenario {
			case "backup failure":
				recovery.beforePut = func(string, []byte) error { return stop }
			case "record failure":
				recovery.beforePut = func(name string, _ []byte) error {
					if strings.HasPrefix(name, "records/") {
						return stop
					}
					return nil
				}
			case "backup conflict":
				recovery.files["publications/"+opts.ExpectedTimestampSHA256+"/1.root.json"] = []byte("changed")
			case "backup readback":
				recovery.beforeGet = func(name string) error {
					if len(recovery.writes) > 0 {
						recovery.files[name] = []byte("changed")
					}
					return nil
				}
			case "public dependency":
				public.beforePut = func(name string, _ []byte) error {
					if name != "timestamp.json" {
						return stop
					}
					return nil
				}
			case "public conflict":
				public.files["1.root.json"] = []byte("changed")
			case "timestamp before commit":
				public.beforePut = func(name string, _ []byte) error {
					if name == "timestamp.json" {
						return stop
					}
					return nil
				}
			case "timestamp ambiguous":
				public.beforePut = func(name string, data []byte) error {
					if name == "timestamp.json" {
						public.files[name] = bytes.Clone(data)
						return stop
					}
					return nil
				}
			case "timestamp readback", "timestamp mismatch":
				public.beforeGet = func(name string) error {
					if name == "timestamp.json" && len(public.files[name]) > 0 {
						if scenario == "timestamp readback" {
							return stop
						}
						public.files[name] = []byte("replaced commit")
					}
					return nil
				}
			case "divergent commit":
				public.beforeGet = func(name string) error {
					if name == "timestamp.json" && len(public.writes) > 0 {
						public.files[name] = []byte("unexpected competing commit")
					}
					return nil
				}
			}
			if err := DeliverPublication(context.Background(), opts); err == nil {
				t.Fatal("failure not surfaced")
			}
			if scenario != "timestamp ambiguous" && scenario != "divergent commit" &&
				scenario != "timestamp readback" && scenario != "timestamp mismatch" {
				if _, ok := public.files["timestamp.json"]; ok {
					t.Fatal("failure advanced public timestamp")
				}
			}
			if strings.HasPrefix(scenario, "backup") || scenario == "record failure" {
				if len(public.writes) != 0 {
					t.Fatal("recovery failure allowed public writes")
				}
			}
			if scenario == "timestamp ambiguous" || scenario == "timestamp before commit" ||
				scenario == "timestamp readback" || scenario == "public dependency" {
				public.beforePut = nil
				public.beforeGet = nil
				if err := DeliverPublication(context.Background(), opts); err != nil {
					t.Fatalf("explicit retry failed: %v", err)
				}
			}
		})
	}
}

func TestRemoteDeliveryRejectsInvalidInputWithoutWrites(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"not serialized", "missing pin", "wrong pin", "wrong record pin", "missing previous",
		"bootstrap and previous", "missing remote", "wrong remote", "rollback", "tampered", "expired", "canceled", "unsealed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			repo, opts, recovery, public := deliveryFixture(t)
			ctx := context.Background()
			switch scenario {
			case "not serialized":
				opts.ExternalSerialization = false
			case "missing pin":
				opts.ExpectedTimestampSHA256 = ""
			case "wrong pin":
				opts.ExpectedTimestampSHA256 = strings.Repeat("a", 64)
			case "wrong record pin":
				opts.ExpectedRecordSHA256 = strings.Repeat("a", 64)
			case "missing previous":
				opts.ExpectedPreviousSHA256 = ""
			case "bootstrap and previous":
				opts.Bootstrap = true
			case "missing remote":
			case "wrong remote":
				public.files["timestamp.json"] = []byte("wrong")
			case "rollback":
				current := metadata.Timestamp()
				current.Signed.Version = 1000
				data, err := current.ToBytes(false)
				if err != nil {
					t.Fatal(err)
				}
				public.files["timestamp.json"], opts.ExpectedPreviousSHA256 = data, digest(data)
			case "tampered":
				if err := os.WriteFile(filepath.Join(opts.Directory, "1.root.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "expired":
				// Re-sign a sealed timestamp and update its record, so this tests
				// expiry enforcement rather than merely the file-digest check.
				ts := metadata.Timestamp()
				if _, err := ts.FromBytes(readPublication(t, opts.Directory, "timestamp.json")); err != nil {
					t.Fatal(err)
				}
				ts.Signed.Expires = time.Now().Add(-time.Hour)
				ts.Signatures = nil
				if err := signMetadata(ts, repo.keys["timestamp"]); err != nil {
					t.Fatal(err)
				}
				data, err := ts.ToBytes(false)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(opts.Directory, "timestamp.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
				var record publicationRecord
				if err := json.Unmarshal(readPublication(t, opts.Directory, "record.json"), &record); err != nil {
					t.Fatal(err)
				}
				record.TimestampSHA256 = digest(data)
				record.Files["timestamp.json"] = digest(data)
				recordData, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(opts.Directory, "record.json"), recordData, 0o600); err != nil {
					t.Fatal(err)
				}
				opts.ExpectedTimestampSHA256 = digest(data)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unsealed":
				if err := os.Remove(filepath.Join(opts.Directory, "record.json")); err != nil {
					t.Fatal(err)
				}
			}
			if err := DeliverPublication(ctx, opts); err == nil {
				t.Fatal("invalid delivery accepted")
			} else if scenario == "expired" && !strings.Contains(err.Error(), "expired") {
				t.Fatalf("expiry test failed for a different reason: %v", err)
			}

			if len(recovery.writes) != 0 || len(public.writes) != 0 {
				t.Fatal("invalid input produced remote writes")
			}
		})
	}
}

func TestRemoteDeliveryAdvancesPinnedPreviousAndRejectsReplay(t *testing.T) {
	t.Parallel()
	repo, opts, _, public := deliveryFixture(t)
	opts.Bootstrap, opts.ExpectedPreviousSHA256 = true, ""
	if err := DeliverPublication(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	first := opts
	backups := t.TempDir()
	if err := PublishTransaction(TransactionOptions{
		Publish:         PublishOptions{Directory: repo.dir, Root: opts.Root, Keys: repo.keys},
		BackupDirectory: backups, RecordDirectory: t.TempDir(), MirrorDirectory: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	opts.ExpectedPreviousSHA256, opts.Bootstrap = opts.ExpectedTimestampSHA256, false
	opts.ExpectedTimestampSHA256 = digest(readPublication(t, repo.dir, "timestamp.json"))
	opts.Directory = filepath.Join(backups, opts.ExpectedTimestampSHA256)
	if err := DeliverPublication(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if digest(public.files["timestamp.json"]) != opts.ExpectedTimestampSHA256 {
		t.Fatal("public commit did not advance")
	}
	first.Bootstrap, first.ExpectedPreviousSHA256 = false, opts.ExpectedTimestampSHA256
	writes := len(public.writes)
	if err := DeliverPublication(context.Background(), first); err == nil {
		t.Fatal("older sealed publication replayed over newer version")
	}
	if len(public.writes) != writes {
		t.Fatal("replay changed public metadata")
	}
}

func TestDeliveryExpiryBoundary(t *testing.T) {
	t.Parallel()
	_, opts, _, _ := deliveryFixture(t)
	files, err := publicationFiles(opts.Directory)
	if err != nil {
		t.Fatal(err)
	}
	timestamp, err := metadata.Timestamp().FromBytes(files["timestamp.json"])
	if err != nil {
		t.Fatal(err)
	}
	if err := deliveryExpiry(files, timestamp.Signed.Expires.Add(-time.Nanosecond)); err != nil {
		t.Fatal("rejected just-before-expiry publication", err)
	}
	if err := deliveryExpiry(files, timestamp.Signed.Expires); err == nil {
		t.Fatal("accepted publication at exact expiry")
	}
}
