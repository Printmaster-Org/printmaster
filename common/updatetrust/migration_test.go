package updatetrust

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

type offlineMigrationFixture struct {
	repo       *testRepository
	client     *Client
	root       []byte
	returnedAt time.Time
	departedAt time.Time
}

func newOfflineMigrationFixture(t *testing.T) offlineMigrationFixture {
	t.Helper()
	now := time.Now().UTC()
	// Calendar months, not an approximation by a fixed number of days.
	departed := now.AddDate(0, -6, 0)
	repo := newTestRepository(t)
	repo.root.Signed.Expires = departed.Add(30 * 24 * time.Hour)
	repo.targets.Signed.Expires = departed.Add(90 * 24 * time.Hour)
	repo.snapshot.Signed.Expires = departed.Add(30 * 24 * time.Hour)
	repo.timestamp.Signed.Expires = departed.Add(7 * 24 * time.Hour)
	repo.publish(t)
	client, server := repositoryClient(t, repo)
	root := append([]byte(nil), client.options.TrustedRoot...)
	if err := client.prepareTrustState(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(server.URL, root)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LocalMetadataDir = client.options.CacheDir
	cfg.LocalTargetsDir = filepath.Join(client.options.CacheDir, "targets")
	cfg.Fetcher = &contextFetcher{ctx: t.Context(), client: server.Client()}
	historical, err := updater.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture-only: actually authenticate the old publication at departure.
	// Production Client exposes no reference-time/expiry override.
	historical.UnsafeSetRefTime(departed)
	if err := historical.Refresh(); err != nil {
		t.Fatal(err)
	}
	targetPath, err := repo.target.Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := historical.GetTargetInfo(targetPath); err != nil {
		t.Fatal(err)
	}
	if err := client.checkpointTrustState(departed); err != nil {
		t.Fatal(err)
	}
	return offlineMigrationFixture{repo: repo, client: client, root: root, returnedAt: now, departedAt: departed}
}

func (f offlineMigrationFixture) rotateAndRefresh(t *testing.T) map[string][]byte {
	t.Helper()
	_, rootKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RotateRoot(f.root, f.repo.keys["root"], rootKey, f.returnedAt.AddDate(1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	intermediate, err := metadata.Root().FromBytes(second)
	if err != nil {
		t.Fatal(err)
	}
	intermediate.Signed.Expires = f.departedAt.Add(120 * 24 * time.Hour)
	signTestMetadata(t, intermediate, f.repo.keys["root"])
	signer, err := signature.LoadSigner(rootKey, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := intermediate.Sign(signer); err != nil {
		t.Fatal(err)
	}
	second, err = intermediate.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	keys := make(map[string]ed25519.PrivateKey)
	public := make(map[string]crypto.PublicKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		pub, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		public[role], keys[role] = pub, key
	}
	third, err := RotateRoutineKeys(second, rootKey, public, f.returnedAt.AddDate(1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	currentRoot, err := metadata.Root().FromBytes(third)
	if err != nil {
		t.Fatal(err)
	}
	f.repo.root, f.repo.keys = currentRoot, keys
	f.repo.keys["root"] = rootKey
	f.repo.targets.Signed.Version = 2
	f.repo.snapshot.Signed.Version = 2
	f.repo.timestamp.Signed.Version = 2
	f.repo.targets.Signed.Expires = f.returnedAt.Add(180 * 24 * time.Hour)
	f.repo.snapshot.Signed.Expires = f.returnedAt.Add(30 * 24 * time.Hour)
	f.repo.timestamp.Signed.Expires = f.returnedAt.Add(7 * 24 * time.Hour)
	publishMigrationRoles(t, f.repo)
	for name, data := range map[string][]byte{"2.root.json": second, "3.root.json": third} {
		if err := os.WriteFile(filepath.Join(f.repo.dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := make(map[string][]byte)
	for _, name := range []string{"2.root.json", "3.root.json", "timestamp.json"} {
		result[name], err = os.ReadFile(filepath.Join(f.repo.dir, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func publishMigrationRoles(t *testing.T, repo *testRepository) {
	t.Helper()
	signTestMetadata(t, repo.targets, repo.keys["targets"])
	targets, err := repo.targets.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	repo.snapshot.Signed.Meta["targets.json"] = metadataInfo(repo.targets.Signed.Version, targets)
	signTestMetadata(t, repo.snapshot, repo.keys["snapshot"])
	snapshot, err := repo.snapshot.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	repo.timestamp.Signed.Meta["snapshot.json"] = metadataInfo(repo.snapshot.Signed.Version, snapshot)
	signTestMetadata(t, repo.timestamp, repo.keys["timestamp"])
	for name, write := range map[string]func(string, bool) error{
		fmt.Sprintf("%d.targets.json", repo.targets.Signed.Version):   repo.targets.ToFile,
		fmt.Sprintf("%d.snapshot.json", repo.snapshot.Signed.Version): repo.snapshot.ToFile,
		"timestamp.json": repo.timestamp.ToFile,
	} {
		if err := write(filepath.Join(repo.dir, name), false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSixMonthOfflineMigration(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{
		"none", "missing-intermediate", "missing-final", "untrusted-intermediate", "missing-successor-signature",
		"expired-root", "expired-timestamp", "expired-snapshot", "expired-targets", "unavailable",
	} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f := newOfflineMigrationFixture(t)
			before, err := InspectClientTrust(f.client.options.CacheDir)
			if err == nil || !before.IntegrityOK || len(before.Roles) != 4 {
				t.Fatalf("historical metadata should now be expired: %+v %v", before, err)
			}
			for _, role := range before.Roles {
				if !role.Expired {
					t.Fatalf("six-month fixture still has fresh %s", role.File)
				}
			}
			current := f.rotateAndRefresh(t)
			expectedRoot := current["3.root.json"]
			switch failure {
			case "missing-intermediate":
				if err := os.Remove(filepath.Join(f.repo.dir, "2.root.json")); err != nil {
					t.Fatal(err)
				}
			case "missing-final":
				if err := os.Remove(filepath.Join(f.repo.dir, "3.root.json")); err != nil {
					t.Fatal(err)
				}
			case "untrusted-intermediate":
				untrusted := newTestRepository(t)
				untrusted.root.Signed.Version = 2
				signTestMetadata(t, untrusted.root, untrusted.keys["root"])
				if err := untrusted.root.ToFile(filepath.Join(f.repo.dir, "2.root.json"), false); err != nil {
					t.Fatal(err)
				}
			case "missing-successor-signature":
				second, err := metadata.Root().FromBytes(current["2.root.json"])
				if err != nil {
					t.Fatal(err)
				}
				original, err := metadata.Root().FromBytes(f.root)
				if err != nil {
					t.Fatal(err)
				}
				oldID := original.Signed.Roles["root"].KeyIDs[0]
				var signatures []metadata.Signature
				for _, sig := range second.Signatures {
					if sig.KeyID == oldID {
						signatures = append(signatures, sig)
					}
				}
				if len(signatures) != 1 {
					t.Fatal("fixture lacks predecessor signature")
				}
				second.Signatures = signatures
				if err := second.ToFile(filepath.Join(f.repo.dir, "2.root.json"), false); err != nil {
					t.Fatal(err)
				}
			case "expired-root":
				f.repo.root.Signed.Expires = f.returnedAt.Add(-time.Hour)
				signTestMetadata(t, f.repo.root, f.repo.keys["root"])
				if err := f.repo.root.ToFile(filepath.Join(f.repo.dir, "3.root.json"), false); err != nil {
					t.Fatal(err)
				}
			case "expired-timestamp":
				f.repo.timestamp.Signed.Expires = f.returnedAt.Add(-time.Hour)
				publishMigrationRoles(t, f.repo)
			case "expired-snapshot":
				f.repo.snapshot.Signed.Expires = f.returnedAt.Add(-time.Hour)
				publishMigrationRoles(t, f.repo)
			case "expired-targets":
				f.repo.targets.Signed.Expires = f.returnedAt.Add(-time.Hour)
				publishMigrationRoles(t, f.repo)
			case "unavailable":
				if err := os.Remove(filepath.Join(f.repo.dir, "timestamp.json")); err != nil {
					t.Fatal(err)
				}
			}
			// A real Client restart uses the original embedded root and retained
			// historical checkpoint, not a newly bootstrapped current root.
			returning, err := NewClient(f.client.options)
			if err != nil {
				t.Fatal(err)
			}
			err = returning.Verify(t.Context(), f.repo.target)
			if failure != "none" {
				if err == nil {
					t.Fatal("broken or stale publisher history authorized an update")
				}
				var expired *metadata.ErrExpiredMetadata
				if strings.HasPrefix(failure, "expired-") && !errors.As(err, &expired) {
					t.Fatalf("expected an expiry failure for %s: %v", failure, err)
				}
				_, failedState, readErr := readClientCheckpoint(returning.options.CacheDir)
				if readErr != nil || !failedState.VerifiedAt.Equal(f.departedAt) {
					t.Fatalf("failure lowered/advanced successful time: %+v %v", failedState, readErr)
				}
				// Restore distribution only; never edit/delete the client checkpoint.
				if failure == "expired-root" {
					previous, err := f.repo.root.ToBytes(false)
					if err != nil {
						t.Fatal(err)
					}
					_, nextKey, err := ed25519.GenerateKey(nil)
					if err != nil {
						t.Fatal(err)
					}
					next, err := RotateRoot(previous, f.repo.keys["root"], nextKey, f.returnedAt.AddDate(1, 0, 0))
					if err != nil {
						t.Fatal(err)
					}
					// Published roots are immutable. Keep the expired third
					// root and repair with a fourth, never different v3 bytes.
					current["3.root.json"] = previous
					current["4.root.json"], expectedRoot = next, next
				}
				for name, data := range current {
					if err := os.WriteFile(filepath.Join(f.repo.dir, name), data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				f.repo.targets.Signed.Expires = f.returnedAt.Add(180 * 24 * time.Hour)
				f.repo.snapshot.Signed.Expires = f.returnedAt.Add(30 * 24 * time.Hour)
				f.repo.timestamp.Signed.Expires = f.returnedAt.Add(7 * 24 * time.Hour)
				f.repo.targets.Signed.Version++
				f.repo.snapshot.Signed.Version++
				f.repo.timestamp.Signed.Version++
				publishMigrationRoles(t, f.repo)
				returning, err = NewClient(returning.options)
				if err != nil {
					t.Fatal(err)
				}
				if err := returning.Verify(t.Context(), f.repo.target); err != nil {
					t.Fatalf("retry after publisher repair failed: %v", err)
				}
			} else if err != nil {
				t.Fatalf("six-month return with retained rotations failed: %v", err)
			}
			report, err := InspectClientTrust(returning.options.CacheDir)
			if err != nil {
				t.Fatal(err)
			}
			_, state, err := readClientCheckpoint(returning.options.CacheDir)
			if err != nil || state.VerifiedAt.Before(f.returnedAt) {
				t.Fatalf("successful return did not advance time floor: %+v %v", state, err)
			}
			root, err := os.ReadFile(filepath.Join(returning.options.CacheDir, "root.json"))
			if err != nil || !bytes.Equal(root, expectedRoot) {
				t.Fatalf("current authorized root not retained: %v (%+v)", err, report)
			}
			checkpoint, err := os.ReadFile(returning.statePath())
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewClient(returning.options)
			if err != nil {
				t.Fatal(err)
			}
			// Old embedded root is not permission to discard retained floors.
			stale := newTestRepository(t)
			stale.timestamp.Signed.Expires = f.returnedAt.Add(7 * 24 * time.Hour)
			signTestMetadata(t, stale.timestamp, f.repo.keys["timestamp"])
			if err := stale.timestamp.ToFile(filepath.Join(f.repo.dir, "timestamp.json"), false); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Verify(t.Context(), f.repo.target); err == nil {
				t.Fatal("signed older timestamp replay accepted after migration")
			}
			after, err := os.ReadFile(returning.statePath())
			if err != nil || !bytes.Equal(checkpoint, after) {
				t.Fatalf("replay changed retained checkpoint: %v", err)
			}
		})
	}
}

func TestSixMonthOfflineRejectsUnrefreshedPublication(t *testing.T) {
	t.Parallel()
	f := newOfflineMigrationFixture(t)
	err := f.client.Verify(t.Context(), f.repo.target)
	var expired *metadata.ErrExpiredMetadata
	if !errors.As(err, &expired) {
		t.Fatalf("old publication did not fail expiry: %v", err)
	}
	_, state, err := readClientCheckpoint(f.client.options.CacheDir)
	if err != nil || !state.VerifiedAt.Equal(f.departedAt) {
		t.Fatalf("expiry changed successful history: %+v %v", state, err)
	}
}
