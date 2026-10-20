package autoupdate

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrivilegedRepositoryPackageRecoveryAssessment(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"deb", "rpm"} {
		for _, mode := range []string{"candidate", "previous", "different", "broken database", "missing package", "cancelled", "corrupt journal"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				journal := testPackageTransaction(t, dir)
				journal.Target.Format = format
				data, err := os.ReadFile(journal.path)
				if err != nil {
					t.Fatal(err)
				}
				if format == "rpm" {
					data = []byte(strings.Replace(string(data), `"format":"deb"`, `"format":"rpm"`, 1))
				}
				if mode == "corrupt journal" {
					data = []byte("{")
				}
				if err := os.WriteFile(journal.path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				calls := 0
				queryError := errors.New("native database unavailable")
				run := func(_ context.Context, timeout time.Duration, gotDir string, env []string, tool string, args ...string) ([]byte, error) {
					calls++
					if timeout != 30*time.Second || gotDir != dir || len(env) != 0 {
						t.Fatalf("unsafe recovery query context: %v %s %v", timeout, gotDir, env)
					}
					wantTool := "/usr/bin/dpkg-query"
					wantArgs := []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n${Status}\n", "printmaster-agent"}
					if format == "rpm" {
						wantTool = "/usr/bin/rpm"
						wantArgs = []string{"-q", "--queryformat", "%{NAME}\n%{VERSION}\n%{ARCH}\n%{EPOCHNUM}\n%{RELEASE}\n", "printmaster-agent"}
					}
					if tool != wantTool || !reflect.DeepEqual(args, wantArgs) {
						t.Fatalf("recovery ran something other than a database query: %s %v", tool, args)
					}
					if mode == "missing package" {
						return nil, queryError
					}
					version := "1.1.0"
					if mode == "previous" {
						version = "1.0.0"
					} else if mode == "different" {
						version = "2.0.0"
					}
					arch, err := nativePackageArch(journal.Target.Arch, format)
					if err != nil {
						t.Fatal(err)
					}
					fields := []string{"printmaster-agent", version, arch, "install ok installed"}
					if format == "rpm" {
						fields = []string{"printmaster-agent", version, arch, "0", "1.fc42"}
					}
					if mode == "broken database" {
						fields = append(fields, "unexpected extra package")
					}
					return []byte(strings.Join(fields, "\n") + "\n"), nil
				}
				got, err := assessPackageTransaction(ctx, dir, run)
				valid := mode == "candidate" || mode == "previous" || mode == "different"
				if (err == nil) != valid {
					t.Fatalf("assessment: %+v %v", got, err)
				}
				if mode == "missing package" && !errors.Is(err, queryError) {
					t.Fatalf("native error lost: %v", err)
				}
				wantCalls := 1
				if mode == "cancelled" || mode == "corrupt journal" {
					wantCalls = 0
				}
				if calls != wantCalls {
					t.Fatalf("query count=%d want=%d", calls, wantCalls)
				}
				if valid {
					disposition := "owner-repair-required"
					version := "2.0.0"
					if mode == "candidate" {
						disposition, version = "candidate-installed", "1.1.0"
					} else if mode == "previous" {
						disposition, version = "previous-installed", "1.0.0"
					}
					if got.Transaction.RunID != journal.RunID || got.Disposition != disposition || got.InstalledVersion != version {
						t.Fatalf("wrong recovery classification: %+v", got)
					}
				}
				after, err := os.ReadFile(journal.path)
				if err != nil || string(after) != string(data) {
					t.Fatal("assessment changed transaction evidence")
				}
				if _, err := newPackageTransaction(dir, journal.Target); err == nil {
					t.Fatal("assessment unblocked installation")
				}
			})
		}
	}
}
