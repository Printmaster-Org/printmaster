package updatepublisher

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatestate"
	"printmaster/common/updatetrust"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type memoryJournal struct {
	data         []byte
	failComplete bool
}

func (j *memoryJournal) Read(context.Context) ([]byte, string, error) {
	return bytes.Clone(j.data), fmt.Sprintf("%x", sha1.Sum(j.data)), nil
}
func (j *memoryJournal) Replace(_ context.Context, rev string, data []byte) error {
	if rev != fmt.Sprintf("%x", sha1.Sum(j.data)) {
		return errors.New("CAS conflict")
	}
	if j.failComplete && bytes.Contains(data, []byte(`"pending":null`)) {
		return errors.New("completion unavailable")
	}
	j.data = bytes.Clone(data)
	return nil
}

type memoryStore struct {
	files         map[string][]byte
	stopTimestamp bool
	ambiguous     bool
	writes        int
}

func (s *memoryStore) Get(_ context.Context, name string) ([]byte, error) {
	data, ok := s.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return bytes.Clone(data), nil
}
func (s *memoryStore) Put(_ context.Context, name string, data []byte) error {
	if name == "timestamp.json" && s.stopTimestamp {
		if s.ambiguous {
			s.files[name] = bytes.Clone(data)
		}
		return errors.New("interrupted before/at timestamp")
	}
	s.files[name] = bytes.Clone(data)
	s.writes++
	return nil
}
func (s *memoryStore) GetPinned(ctx context.Context, name, pin string) ([]byte, error) {
	data, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != pin {
		return nil, errors.New("pin mismatch")
	}
	return data, nil
}

func publisherFixture(t *testing.T) (Options, *memoryJournal, *memoryStore, *memoryStore, *int) {
	t.Helper()
	keys := make(map[string]ed25519.PrivateKey)
	pubs := make(map[string]crypto.PublicKey)
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keys[role] = key
		pubs[role] = pub
	}
	root, err := updatetrust.InitializeRoot(keys["root"], pubs, time.Now().Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	delete(keys, "root")
	j := &memoryJournal{data: []byte(`{"schema":1,"generation":0,"parent_blob":"","latest":null,"pending":null}`)}
	recovery, public := &memoryStore{files: make(map[string][]byte)}, &memoryStore{files: make(map[string][]byte)}
	loads := new(int)
	return Options{Work: t.TempDir(), Root: root, Journal: updatestate.Journal{Backend: j}, Recovery: recovery,
			Public: public, Bootstrap: true, ExternalSerialization: true, LoadKeys: func() (map[string]ed25519.PrivateKey, error) { *loads++; return keys, nil }},
		j, recovery, public, loads
}

func TestBootstrapInterruptedRetryRefreshAndMirror(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"before timestamp", "ambiguous timestamp", "authority completion"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			opts, j, recovery, public, loads := publisherFixture(t)
			switch scenario {
			case "before timestamp":
				public.stopTimestamp = true
			case "ambiguous timestamp":
				public.stopTimestamp, public.ambiguous = true, true
			case "authority completion":
				j.failComplete = true
			}
			if _, err := Run(context.Background(), opts); err == nil {
				t.Fatal("interruption hidden")
			}
			state, _, err := opts.Journal.Read(context.Background())
			if err != nil || state.Pending == nil {
				t.Fatalf("lost pending authority: %v", err)
			}
			pending := state.Pending.Publication
			if len(recovery.files) == 0 || *loads != 1 {
				t.Fatal("missing retained payload or signing count")
			}
			public.stopTimestamp, j.failComplete = false, false
			opts.Bootstrap = false
			opts.Work = t.TempDir()
			opts.LoadKeys = func() (map[string]ed25519.PrivateKey, error) {
				t.Fatal("pending recovery accessed signing keys")
				return nil, nil
			}
			retried, err := Run(context.Background(), opts)
			if err != nil || !retried.RecoveredPending || retried.Publication != pending {
				t.Fatalf("exact pending retry failed: %+v %v", retried, err)
			}
			latest, err := opts.Journal.Latest(context.Background())
			if err != nil || latest != pending {
				t.Fatalf("latest mismatch: %v", err)
			}
			mirror := t.TempDir()
			if err := updatetrust.MirrorPublication(context.Background(), mirror, opts.Root, latest, publicLayout{public}); err != nil {
				t.Fatal(err)
			}
			if err := updatetrust.CheckMirrorFreshness(mirror, opts.Root, 48*time.Hour); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type publicLayout struct{ *memoryStore }

func (p publicLayout) GetPinned(ctx context.Context, name, pin string) ([]byte, error) {
	parts := strings.Split(name, "/")
	if len(parts) == 3 {
		if parts[2] == "record.json" {
			name = "records/" + parts[1] + ".json"
		} else {
			name = parts[2]
		}
	}
	return p.memoryStore.GetPinned(ctx, name, pin)
}

func TestNormalRefreshAndFailClosedInputs(t *testing.T) {
	t.Parallel()
	opts, _, recovery, public, loads := publisherFixture(t)
	artifact := []byte("disposable artifact for retained inventory")
	file := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(file, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	target := updatetrust.Target{Component: "agent", Channel: "beta", Version: "0.32.0-beta.test",
		Platform: "windows", Arch: "amd64", Format: "exe", SHA256: fmt.Sprintf("%x", sha256.Sum256(artifact)), SizeBytes: int64(len(artifact))}
	opts.Targets = []updatetrust.PublishTarget{{Target: target, File: file}}
	first, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Bootstrap = false
	opts.Targets = nil
	opts.Work = t.TempDir()
	second, err := Run(context.Background(), opts)
	if err != nil || second.Publication.Version != first.Publication.Version+1 || second.RecoveredPending || *loads != 2 {
		t.Fatalf("refresh did not advance: %+v %v", second, err)
	}
	// The refreshed targets version is deliberately found through its signed
	// dependencies, not an assumed timestamp/targets version equality.
	timestamp, err := metadata.Timestamp().FromBytes(public.files["timestamp.json"])
	if err != nil {
		t.Fatal(err)
	}
	snap, err := metadata.Snapshot().FromBytes(public.files[fmt.Sprintf("%d.snapshot.json", timestamp.Signed.Meta["snapshot.json"].Version)])
	if err != nil {
		t.Fatal(err)
	}
	targets, err := metadata.Targets().FromBytes(public.files[fmt.Sprintf("%d.targets.json", snap.Signed.Meta["targets.json"].Version)])
	if err != nil {
		t.Fatal(err)
	}
	path, err := target.Path()
	if err != nil {
		t.Fatal(err)
	}
	if targets.Signed.Targets[path] == nil || targets.Signed.Targets[path].Length != target.SizeBytes {
		t.Fatal("freshness refresh discarded retained release inventory")
	}
	// A lost retained dependency cannot trigger fresh signing or reset.
	delete(recovery.files, "publications/"+second.Publication.TimestampSHA256+"/1.root.json")
	opts.Work = t.TempDir()
	if _, err := Run(context.Background(), opts); err == nil || *loads != 2 {
		t.Fatal("missing retained state signed again")
	}
	if len(public.files["timestamp.json"]) == 0 {
		t.Fatal("public commit lost")
	}
}

func TestPendingCorruptionAndOwnershipFailBeforeSigning(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"not serialized", "bootstrap missing", "unexpected bootstrap", "corrupt authority", "wrong root", "missing pending payload"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			opts, j, recovery, public, loads := publisherFixture(t)
			switch scenario {
			case "not serialized":
				opts.ExternalSerialization = false
			case "bootstrap missing":
				opts.Bootstrap = false
			case "corrupt authority":
				j.data = []byte("{}")
			default:
				public.stopTimestamp = true
				if _, err := Run(context.Background(), opts); err == nil {
					t.Fatal("setup interruption missing")
				}
				opts.Work = t.TempDir()
				switch scenario {
				case "wrong root":
					other, _, _, _, _ := publisherFixture(t)
					opts.Root = other.Root
					opts.Bootstrap = false
				case "missing pending payload":
					opts.Bootstrap = false
					for name := range recovery.files {
						delete(recovery.files, name)
						break
					}
				}
			}
			before := *loads
			if _, err := Run(context.Background(), opts); err == nil || *loads != before {
				t.Fatal("invalid authority/ownership silently signed")
			}
		})
	}
}

func TestResultContainsOnlyPublicIdentity(t *testing.T) {
	t.Parallel()
	opts, _, _, _, _ := publisherFixture(t)
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("PRIVATE")) || !bytes.Contains(data, []byte("timestamp_sha256")) {
		t.Fatal("invalid public result")
	}
}
