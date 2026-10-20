package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"printmaster/common/updatepolicy"
)

func TestPrivilegedRepositoryPackagePolicyDecoding(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"enabled", `{"schema":1,"enabled":true}`, true},
		{"formatted", "{\n \"schema\": 1,\n \"enabled\": true\n}", true},
		{"pins", `{"schema":1,"enabled":true,"version_pin_strategy":"minor","allow_major_upgrade":false,"target_version":"1.2.3","emergency_abort":false}`, true},
		{"missing enablement", `{"schema":1}`, false},
		{"disabled", `{"schema":1,"enabled":false}`, false},
		{"schema", `{"schema":2,"enabled":true}`, false},
		{"abort", `{"schema":1,"enabled":true,"emergency_abort":true}`, false},
		{"invalid strategy", `{"schema":1,"enabled":true,"version_pin_strategy":"unknown"}`, false},
		{"dev target", `{"schema":1,"enabled":true,"target_version":"1.2.3-dev"}`, false},
		{"invalid target", `{"schema":1,"enabled":true,"target_version":"v1.2.3"}`, false},
		{"unknown", `{"schema":1,"enabled":true,"command":"arbitrary"}`, false},
		{"duplicate", `{"schema":1,"enabled":false,"enabled":true}`, false},
		{"null", `{"schema":1,"enabled":true,"emergency_abort":null}`, false},
		{"wrong type", `{"schema":1,"enabled":"true"}`, false},
		{"array", `[]`, false},
		{"trailing", `{"schema":1,"enabled":true}{}`, false},
		{"truncated", `{"schema":1,"enabled":true`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeRootPackagePolicy([]byte(test.body))
			if (err == nil) != test.valid {
				t.Fatalf("policy result: %+v %v", got, err)
			}
			if test.name == "pins" && (got.VersionPinStrategy != updatepolicy.VersionPinMinor || got.TargetVersion != "1.2.3") {
				t.Fatalf("policy pins lost: %+v", got)
			}
		})
	}
}

func TestPrivilegedRepositoryPackagePolicyFile(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "public read", "missing", "symlink", "hardlink", "writable", "executable", "foreign owner", "fifo", "directory", "oversized", "relative"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "policy.json")
			if err := os.WriteFile(path, []byte(`{"schema":1,"enabled":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				path += ".missing"
			case "relative":
				path = "policy.json"
			case "symlink":
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "hardlink":
				if err := os.Link(path, path+".link"); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(path, 0o620); err != nil {
					t.Fatal(err)
				}
			case "executable":
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "public read":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "foreign owner":
				if os.Geteuid() == 0 {
					if err := os.Chown(path, 65534, 65534); err != nil {
						t.Fatal(err)
					}
				}
			case "fifo", "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if mode == "fifo" {
					if err := unix.Mkfifo(path, 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat(" ", 4097)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readRootPackagePolicy(path)
			valid := os.Geteuid() == 0 && (mode == "valid" || mode == "public read")
			if (err == nil) != valid {
				t.Fatalf("file policy result: %+v %v", got, err)
			}
			if valid {
				// An administrative disable must be observed by the next
				// boundary read, not hidden by a cached PolicySpec.
				if err := os.WriteFile(path, []byte(`{"schema":1,"enabled":false}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := readRootPackagePolicy(path); err == nil {
					t.Fatal("disabled policy was not reread")
				}
			}
		})
	}
}
