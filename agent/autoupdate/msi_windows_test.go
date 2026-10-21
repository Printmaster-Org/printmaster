//go:build windows

package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsUpdaterMSIProductOwnership(t *testing.T) {
	path := filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent", "printmaster-agent.exe")
	for _, tc := range []struct {
		name     string
		products []msiProduct
		wantMSI  bool
		wantErr  bool
	}{
		{name: "raw installation"},
		{name: "canonical component", products: []msiProduct{{code: "exact-product", path: path}}, wantMSI: true},
		{name: "case insensitive", products: []msiProduct{{code: "exact-product", path: strings.ToUpper(path)}}, wantMSI: true},
		{name: "different component path", products: []msiProduct{{code: "exact-product", path: filepath.Join(t.TempDir(), "agent.exe")}}, wantErr: true},
		{name: "multiple registrations", products: []msiProduct{{code: "one", path: path}, {code: "two", path: path}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, isMSI, err := matchMSIProduct(path, tc.products)
			if isMSI != tc.wantMSI || (err != nil) != tc.wantErr || tc.wantMSI && code != "exact-product" {
				t.Fatalf("code=%q MSI=%v err=%v", code, isMSI, err)
			}
		})
	}
	// A development/test executable is not the installed MSI component, even
	// on a workstation that also has the real Agent installed.
	if code, isMSI, err := checkMSIInstallation(filepath.Join(t.TempDir(), "agent.exe")); code != "" || isMSI || err != nil {
		t.Fatalf("unrelated executable classified as MSI: %q %v %v", code, isMSI, err)
	}
	fakeRoot := t.TempDir()
	t.Setenv("ProgramFiles", fakeRoot)
	if code, isMSI, err := checkMSIInstallation(filepath.Join(fakeRoot, "PrintMaster", "Agent", "printmaster-agent.exe")); code != "" || isMSI || err != nil {
		t.Fatalf("environment override borrowed real MSI registration: %q %v %v", code, isMSI, err)
	}
}
