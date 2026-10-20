package autoupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"printmaster/common/updatetrust"
)

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
			var got packageTransaction
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
