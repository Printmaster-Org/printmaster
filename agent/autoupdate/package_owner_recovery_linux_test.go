package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivilegedRepositoryPackageOwnerRepair(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("owner repair confirmation requires root")
	}
	for _, mode := range []string{"previous", "candidate", "wrong run", "installing", "unknown version", "service stopped", "wrong executable", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			journal := testPackageTransaction(t, dir)
			journal.Phase = "recovery-required"
			if mode == "installing" {
				journal.Phase = "installing"
			}
			data, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journal.path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			version := "1.0.0"
			if mode == "candidate" {
				version = "1.1.0"
			}
			if mode == "unknown version" {
				version = "1.2.0"
			}
			run := func(ctx context.Context, _ time.Duration, _ string, _ []string, tool string, args ...string) ([]byte, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if tool == "/usr/bin/systemctl" {
					if len(args) < 3 || args[2] != "show" {
						t.Fatal("repair confirmation attempted service modification")
					}
					output := nativeServiceTestOutput()
					if mode == "service stopped" {
						output = strings.Replace(output, "ActiveState=active", "ActiveState=inactive", 1)
					}
					return []byte(output), nil
				}
				if tool != "/usr/bin/dpkg-query" {
					t.Fatalf("repair confirmation attempted package modification: %s %v", tool, args)
				}
				arch, err := nativePackageArch(journal.Target.Arch, "deb")
				if err != nil {
					t.Fatal(err)
				}
				return []byte("printmaster-agent\n" + version + "\n" + arch + "\ninstall ok installed\n"), nil
			}
			runID := journal.RunID
			if mode == "wrong run" {
				runID = strings.Repeat("b", 32)
			}
			var confirmed RepositoryPackageTransaction
			err = withRootPackageState(dir, false, func() error {
				var err error
				confirmed, err = confirmOwnerPackageRepair(ctx, dir, runID, run, func(int) error {
					if mode == "wrong executable" {
						return errors.New("wrong executable")
					}
					return nil
				})
				return err
			})
			valid := mode == "previous" || mode == "candidate"
			if (err == nil) != valid {
				t.Fatalf("repair result mode=%s: %+v %v", mode, confirmed, err)
			}
			if valid {
				archived, err := readPackageTransactionFile(filepath.Join(dir, "transactions", runID+".json"))
				if err != nil || archived.Phase != "owner-repaired" || archived.RepairedVersion != version {
					t.Fatalf("owner repair lacks durable exact evidence: %+v %v", archived, err)
				}
				if _, err := os.Stat(journal.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("confirmed repair left blocker: %v", err)
				}
			} else {
				unchanged, err := os.ReadFile(journal.path)
				if err != nil || string(unchanged) != string(data) {
					t.Fatalf("rejected owner repair changed active evidence: %v", err)
				}
			}
		})
	}
}
