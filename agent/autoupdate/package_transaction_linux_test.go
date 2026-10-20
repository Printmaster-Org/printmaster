package autoupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"printmaster/common/updatetrust"
)

func testPackageTransaction(t *testing.T, dir string) *RepositoryPackageTransaction {
	t.Helper()
	target := updatetrust.Target{Component: "agent", Channel: "stable", Platform: "linux",
		Arch: runtime.GOARCH, Format: "deb", Version: "1.1.0", SHA256: strings.Repeat("a", 64), SizeBytes: 123}
	journal, err := newPackageTransaction(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.record("1.0.0", "installing"); err != nil {
		t.Fatal(err)
	}
	return journal
}

func TestPrivilegedRepositoryPackageInspection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := testPackageTransaction(t, dir)
	got, err := InspectPrivilegedRepositoryPackage(context.Background(), dir)
	if os.Geteuid() != 0 {
		if err == nil {
			t.Fatal("non-root inspection accepted")
		}
		return
	}
	if err != nil || !reflect.DeepEqual(got, *journal) {
		t.Fatalf("wrong inspected transaction: %+v %v", got, err)
	}
	if err := withRootPackageState(dir, false, func() error {
		if _, err := InspectPrivilegedRepositoryPackage(context.Background(), dir); err == nil {
			t.Fatal("inspection ignored intake lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectPrivilegedRepositoryPackage(ctx, dir); err == nil {
		t.Fatal("cancelled inspection accepted")
	}
	missing := filepath.Join(dir, "missing")
	if _, err := InspectPrivilegedRepositoryPackage(context.Background(), missing); err == nil {
		t.Fatal("missing state accepted")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("inspection created missing state")
	}
}

func TestPrivilegedRepositoryPackageJournalValidation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "schema", "run", "digest", "arch", "channel", "component", "format", "size", "previous", "downgrade", "timestamps", "phase", "unknown field", "duplicate field", "trailing json", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			journal := testPackageTransaction(t, dir)
			switch mode {
			case "schema":
				journal.Schema++
			case "run":
				journal.RunID = "../arbitrary"
			case "digest":
				journal.Target.SHA256 = "not-a-digest"
			case "arch":
				journal.Target.Arch = "unsupported"
			case "channel":
				journal.Target.Channel = "dev"
			case "component":
				journal.Target.Component = "server"
			case "format":
				journal.Target.Format = "binary"
			case "size":
				journal.Target.SizeBytes = maxRepositoryPackageSize + 1
			case "previous":
				journal.PreviousVersion = "1.0.0-dev"
			case "downgrade":
				journal.PreviousVersion = "2.0.0"
			case "timestamps":
				journal.UpdatedAt = journal.StartedAt.Add(-1)
			case "phase":
				journal.Phase = "success"
			}
			data, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown field":
				data = append([]byte(`{"unknown":1,`), data[1:]...)
			case "duplicate field":
				data = append([]byte(`{"schema":2,`), data[1:]...)
			case "trailing json":
				data = append(data, []byte(`{}`)...)
			case "oversized":
				data = []byte(strings.Repeat(" ", 16*1024+1))
			}
			if err := os.WriteFile(journal.path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readPackageTransaction(dir)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("validation result: %+v %v", got, err)
			}
			after, readErr := os.ReadFile(journal.path)
			if readErr != nil || string(after) != string(data) {
				t.Fatal("inspection modified existing evidence")
			}
		})
	}
}

func TestPrivilegedRepositoryPackageJournal(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"installing", "awaiting-startup", "recovery-required"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			target := updatetrust.Target{Component: "agent", Channel: "stable", Platform: "linux",
				Arch: "amd64", Format: "deb", Version: "1.1.0", SHA256: "authenticated-package-digest", SizeBytes: 123}
			journal, err := newPackageTransaction(dir, target)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.record("1.0.0", "awaiting-startup"); err == nil {
				t.Fatal("outcome accepted without intent")
			}
			if err := journal.record("1.0.0", "installing"); err != nil {
				t.Fatal(err)
			}
			if phase != "installing" {
				if err := journal.record("1.0.0", phase); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, packageTransactionFile)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got RepositoryPackageTransaction
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Schema != 1 || got.Target != target || got.RunID != journal.RunID || len(got.RunID) != 32 ||
				got.PreviousVersion != "1.0.0" || got.Phase != phase || got.StartedAt.IsZero() || got.UpdatedAt.Before(got.StartedAt) {
				t.Fatalf("wrong persisted transaction: %+v", got)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("unsafe record permissions: %v %v", info, err)
			}
			// A new helper/process must not infer completion or retry from any
			// existing phase, including successful native database validation.
			if _, err := newPackageTransaction(dir, target); err == nil {
				t.Fatal("unreconciled transaction accepted a new installation")
			}
			before := *journal
			if err := journal.record("0.9.0", "recovery-required"); err == nil {
				t.Fatal("changed previous identity accepted")
			}
			if !reflect.DeepEqual(before, *journal) {
				t.Fatal("invalid transition changed in-memory record")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("journal leaked temporary files: %v %v", entries, err)
			}
		})
	}
	t.Run("corrupt record blocks retry", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, packageTransactionFile)
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newPackageTransaction(dir, updatetrust.Target{}); err == nil {
			t.Fatal("corrupt record was reset")
		}
	})
	t.Run("persistence failure does not advance phase", func(t *testing.T) {
		dir := t.TempDir()
		journal, err := newPackageTransaction(dir, updatetrust.Target{})
		if err != nil {
			t.Fatal(err)
		}
		// Replace the destination with a directory to force rename failure.
		if err := os.Mkdir(journal.path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := journal.record("1.0.0", "installing"); err == nil {
			t.Fatal("failed persistence reported success")
		}
		if journal.Phase != "" {
			t.Fatal("failed persistence advanced transaction phase")
		}
	})
}
