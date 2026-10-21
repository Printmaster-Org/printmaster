package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPrivilegedRepositoryPackageServiceCompletion(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root service journal fixture requires root")
	}
	for _, mode := range []string{"valid", "restart failed", "wrong executable", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			journal := testPackageTransaction(t, dir)
			journal.Phase = "awaiting-startup"
			data, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journal.path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			budget := time.Second
			if mode == "wrong executable" {
				budget = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			restarts := 0
			run := func(ctx context.Context, _ time.Duration, dir string, _ []string, tool string, args ...string) ([]byte, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if tool == "/usr/bin/systemctl" {
					if len(args) > 2 && args[2] == "restart" {
						restarts++
						if !reflect.DeepEqual(args, []string{"--system", "--no-pager", "restart", "--", "printmaster-agent.service"}) {
							t.Fatalf("unexpected restart arguments: %v", args)
						}
						if mode == "restart failed" {
							return nil, errors.New("actual service start rejected")
						}
						return nil, nil
					}
					return []byte(nativeServiceTestOutput()), nil
				}
				if tool != "/usr/bin/dpkg-query" {
					t.Fatalf("completion attempted unexpected command %s %v", tool, args)
				}
				arch, err := nativePackageArch(journal.Target.Arch, "deb")
				if err != nil {
					t.Fatal(err)
				}
				return []byte("printmaster-agent\n1.1.0\n" + arch + "\ninstall ok installed\n"), nil
			}
			outcome := RepositoryPackageOutcome{Schema: 1, Status: "awaiting-startup", Target: &journal.Target, RunID: journal.RunID}
			err = completeRepositoryPackageService(ctx, dir, &outcome, run, func(int) error {
				if mode == "wrong executable" {
					return errors.New("wrong executable")
				}
				return nil
			})
			if mode == "valid" {
				if err != nil || outcome.Status != "startup-confirmed" || restarts != 1 {
					t.Fatalf("completion failed: %+v %v restarts=%d", outcome, err, restarts)
				}
				saved, err := readPackageTransactionFile(filepath.Join(dir, "transactions", journal.RunID+".json"))
				if err != nil || saved.Phase != "startup-confirmed" {
					t.Fatalf("completion lacks protected archive: %+v %v", saved, err)
				}
			} else {
				if err == nil {
					t.Fatal("service failure reported success")
				}
				saved, err := readPackageTransaction(dir)
				if err != nil || saved.Phase != "recovery-required" {
					t.Fatalf("failure lost protected blocker: %+v %v", saved, err)
				}
			}
		})
	}
}
