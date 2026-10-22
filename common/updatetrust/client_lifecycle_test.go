package updatetrust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func copyClientMetadata(t *testing.T, client *Client) string {
	t.Helper()
	backup := t.TempDir()
	for _, name := range trustedFiles {
		data, err := readClientTrustFile(filepath.Join(client.options.CacheDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backup, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return backup
}

func TestClientTrustClockHistory(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	before := time.Now().UTC()
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	_, state, err := readClientCheckpoint(client.options.CacheDir)
	if err != nil || state.VerifiedAt.Before(before) || state.VerifiedAt.After(time.Now().UTC()) {
		t.Fatalf("successful authorization did not persist verification time: %+v %v", state, err)
	}
	for _, offset := range []time.Duration{0, -TrustClockTolerance, -TrustClockTolerance - time.Nanosecond} {
		err := checkTrustClock(state.VerifiedAt.Add(offset), state.VerifiedAt)
		if errors.Is(err, ErrTrustClock) != (offset < -TrustClockTolerance) {
			t.Fatalf("clock boundary %s: %v", offset, err)
		}
	}
	state.VerifiedAt = time.Now().UTC().Add(10 * time.Minute)
	if err := client.writeTrustState(state); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(client.statePath())
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingTransport{base: http.DefaultTransport}
	opts := client.options
	opts.HTTPClient = &http.Client{Transport: counter}
	restarted, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	err = restarted.Verify(context.Background(), repo.target)
	var diagnostic *TrustDiagnostic
	if !errors.Is(err, ErrTrustClock) || !errors.As(err, &diagnostic) ||
		diagnostic.Code != "trust-clock-rollback" || counter.requests.Load() != 0 {
		t.Fatalf("clock rollback not rejected before network: %v requests=%d", err, counter.requests.Load())
	}
	after, err := os.ReadFile(client.statePath())
	if err != nil || !bytes.Equal(record, after) {
		t.Fatalf("clock failure changed checkpoint: %v", err)
	}
	if _, err := InspectClientTrust(opts.CacheDir); !errors.Is(err, ErrTrustClock) {
		t.Fatalf("inspection hid clock rollback: %v", err)
	}
}

func TestClientTrustFailedAuthorizationRetainsClockHistory(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	_, before, err := readClientCheckpoint(client.options.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	wrong := repo.target
	wrong.SHA256 = strings.Repeat("0", 64)
	if err := client.Verify(context.Background(), wrong); err == nil {
		t.Fatal("wrong target accepted")
	}
	_, after, err := readClientCheckpoint(client.options.CacheDir)
	if err != nil || !before.VerifiedAt.Equal(after.VerifiedAt) {
		t.Fatalf("failed authorization changed last successful time: %v", err)
	}
}

func TestClientTrustSmallClockCorrectionNeverLowersHistory(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	_, state, err := readClientCheckpoint(client.options.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	state.VerifiedAt = time.Now().UTC().Add(2 * time.Minute)
	if err := client.writeTrustState(state); err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("small correction inside tolerance blocked: %v", err)
	}
	_, after, err := readClientCheckpoint(client.options.CacheDir)
	if err != nil || !after.VerifiedAt.Equal(state.VerifiedAt) {
		t.Fatalf("successful verification lowered time high-water: %+v %v", after, err)
	}
}

func TestClientTrustLegacyCheckpointMigration(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	_, state, err := readClientCheckpoint(client.options.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(map[string]any{"version": state.Version, "sha256": state.Hashes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client.statePath(), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("intact legacy checkpoint rejected: %v", err)
	}
	_, upgraded, err := readClientCheckpoint(client.options.CacheDir)
	if err != nil || upgraded.VerifiedAt.IsZero() {
		t.Fatalf("legacy clock history not initialized: %+v %v", upgraded, err)
	}
}

func TestClientTrustExpiryDiagnosticAndRefreshRecovery(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"root", "timestamp", "snapshot", "targets"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			switch role {
			case "root":
				repo.root.Signed.Expires = time.Now().Add(-time.Hour)
			case "timestamp":
				repo.timestamp.Signed.Expires = time.Now().Add(-time.Hour)
			case "snapshot":
				repo.snapshot.Signed.Expires = time.Now().Add(-time.Hour)
			case "targets":
				repo.targets.Signed.Expires = time.Now().Add(-time.Hour)
			}
			repo.publish(t)
			client, _ := repositoryClient(t, repo)
			err := client.Verify(context.Background(), repo.target)
			var expired *metadata.ErrExpiredMetadata
			var diagnostic *TrustDiagnostic
			if !errors.As(err, &expired) || !errors.As(err, &diagnostic) ||
				diagnostic.Code != "trust-metadata-expired" || diagnostic.LocalUTC.IsZero() ||
				!strings.Contains(err.Error(), "independently") {
				t.Fatalf("missing actionable expiry diagnostic: %v", err)
			}
			// No state deletion or local expiry override: publisher issues
			// the next signed versions (including a successor root).
			repo.root.Signed.Version++
			repo.root.Signed.Expires = time.Now().Add(365 * 24 * time.Hour)
			repo.timestamp.Signed.Version++
			repo.snapshot.Signed.Version++
			repo.targets.Signed.Version++
			repo.timestamp.Signed.Expires = time.Now().Add(7 * 24 * time.Hour)
			repo.snapshot.Signed.Expires = time.Now().Add(30 * 24 * time.Hour)
			repo.targets.Signed.Expires = time.Now().Add(180 * 24 * time.Hour)
			repo.publish(t)
			for name, value := range map[string]interface{ ToFile(string, bool) error }{
				"2.root.json": repo.root, "2.snapshot.json": repo.snapshot, "2.targets.json": repo.targets,
			} {
				if err := value.ToFile(filepath.Join(repo.dir, name), false); err != nil {
					t.Fatal(err)
				}
			}
			if err := client.Verify(context.Background(), repo.target); err != nil {
				t.Fatalf("signed refresh/rotation did not recover expired %s: %v", role, err)
			}
		})
	}
}

func TestClientTrustExactRepair(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing root", "corrupt timestamp", "missing cache"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			client, _ := repositoryClient(t, repo)
			if err := client.Verify(context.Background(), repo.target); err != nil {
				t.Fatal(err)
			}
			backup := copyClientMetadata(t, client)
			checkpoint, err := os.ReadFile(client.statePath())
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing root":
				if err := os.Remove(filepath.Join(client.options.CacheDir, "root.json")); err != nil {
					t.Fatal(err)
				}
			case "corrupt timestamp":
				if err := os.WriteFile(filepath.Join(client.options.CacheDir, "timestamp.json"), []byte("damaged"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing cache":
				if err := os.RemoveAll(client.options.CacheDir); err != nil {
					t.Fatal(err)
				}
			}
			if report, err := InspectClientTrust(client.options.CacheDir); !errors.Is(err, ErrTrustState) || report.IntegrityOK {
				t.Fatalf("inspection hid damaged metadata: %+v %v", report, err)
			}
			evidence, err := RepairClientTrust(context.Background(), ClientTrustRepairOptions{
				CacheDir: client.options.CacheDir, BackupDir: backup, ExternalSerialization: true,
			})
			if err != nil || evidence == "" {
				t.Fatalf("exact repair failed: %s %v", evidence, err)
			}
			saved, err := os.ReadFile(filepath.Join(evidence, "checkpoint.json"))
			if err != nil || !bytes.Equal(checkpoint, saved) {
				t.Fatalf("checkpoint evidence lost: %v", err)
			}
			if mode == "corrupt timestamp" {
				saved, err := os.ReadFile(filepath.Join(evidence, "timestamp.json"))
				if err != nil || string(saved) != "damaged" {
					t.Fatalf("damaged evidence lost: %q %v", saved, err)
				}
			}
			current, err := os.ReadFile(client.statePath())
			if err != nil || !bytes.Equal(checkpoint, current) {
				t.Fatalf("repair replaced checkpoint: %v", err)
			}
			report, err := InspectClientTrust(client.options.CacheDir)
			if err != nil || !report.IntegrityOK || len(report.Roles) != 4 {
				t.Fatalf("repaired inspection failed: %+v %v", report, err)
			}
			if err := client.Verify(context.Background(), repo.target); err != nil {
				t.Fatalf("normal authorization after repair failed: %v", err)
			}
		})
	}
}

func TestClientTrustRepairPreservesRollbackFloor(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	repo.timestamp.Signed.Version = 2
	repo.publish(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	backup := copyClientMetadata(t, client)
	if err := os.Remove(filepath.Join(client.options.CacheDir, "timestamp.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairClientTrust(context.Background(), ClientTrustRepairOptions{
		CacheDir: client.options.CacheDir, BackupDir: backup, ExternalSerialization: true,
	}); err != nil {
		t.Fatal(err)
	}
	repo.timestamp.Signed.Version = 1
	repo.publish(t)
	err := client.Verify(context.Background(), repo.target)
	var version *metadata.ErrBadVersionNumber
	var diagnostic *TrustDiagnostic
	if !errors.As(err, &version) || !errors.As(err, &diagnostic) || diagnostic.Code != "trust-metadata-version" {
		t.Fatalf("exact repair lost version floor or rollback diagnostic: %v", err)
	}
}

func TestClientTrustInspectionIsReadOnlyOnExpiry(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	repo.root.Signed.Expires = time.Now().Add(-time.Hour)
	repo.publish(t)
	client, _ := repositoryClient(t, repo)
	if err := client.prepareTrustState(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(client.statePath())
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectClientTrust(client.options.CacheDir)
	var expiry *metadata.ErrExpiredMetadata
	if !errors.As(err, &expiry) || !report.IntegrityOK || len(report.Roles) != 1 || !report.Roles[0].Expired {
		t.Fatalf("expiry inspection hidden or treated as integrity loss: %+v %v", report, err)
	}
	after, err := os.ReadFile(client.statePath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read-only inspection changed checkpoint: %v", err)
	}
}
func TestClientTrustRepairRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"stale", "missing checkpoint", "corrupt checkpoint", "unknown role", "bad hash", "not serialized", "cancelled", "nested", "future clock"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			client, _ := repositoryClient(t, repo)
			if err := client.Verify(context.Background(), repo.target); err != nil {
				t.Fatal(err)
			}
			opts := ClientTrustRepairOptions{CacheDir: client.options.CacheDir, BackupDir: copyClientMetadata(t, client), ExternalSerialization: true}
			ctx := context.Background()
			switch mode {
			case "stale":
				repo.timestamp.Signed.Version++
				repo.publish(t)
				if err := client.Verify(ctx, repo.target); err != nil {
					t.Fatal(err)
				}
			case "missing checkpoint":
				if err := os.Remove(client.statePath()); err != nil {
					t.Fatal(err)
				}
			case "corrupt checkpoint", "unknown role", "bad hash":
				data := []byte("{")
				if mode != "corrupt checkpoint" {
					_, state, err := readClientCheckpoint(opts.CacheDir)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "unknown role" {
						state.Hashes["other.json"] = strings.Repeat("0", 64)
					} else {
						state.Hashes["root.json"] = "bad"
					}
					data, err = json.Marshal(state)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(client.statePath(), data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "not serialized":
				opts.ExternalSerialization = false
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nested":
				opts.BackupDir = filepath.Join(opts.CacheDir, "nested")
			case "future clock":
				_, state, err := readClientCheckpoint(opts.CacheDir)
				if err != nil {
					t.Fatal(err)
				}
				state.VerifiedAt = time.Now().Add(time.Hour)
				if err := client.writeTrustState(state); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeErr := os.ReadFile(client.statePath())
			if evidence, err := RepairClientTrust(ctx, opts); err == nil || evidence != "" {
				t.Fatalf("unsafe repair accepted or mutated destination: %s %v", evidence, err)
			}
			after, afterErr := os.ReadFile(client.statePath())
			if !bytes.Equal(before, after) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("rejected repair changed checkpoint")
			}
		})
	}
}
