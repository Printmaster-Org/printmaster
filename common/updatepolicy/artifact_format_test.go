package updatepolicy

import "testing"

func TestParseArtifactFormat(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		want ArtifactFormat
		ok   bool
	}{
		"":         {ArtifactFormatBinary, true},
		"binary":   {ArtifactFormatBinary, true},
		" MSI ":    {ArtifactFormatMSI, true},
		"deb":      {ArtifactFormatDeb, true},
		"rpm":      {ArtifactFormatRPM, true},
		"exe":      {"", false},
		"tarball":  {"", false},
		"../../x":  {"", false},
		"Binary\n": {ArtifactFormatBinary, true},
	}
	for raw, tc := range cases {
		got, ok := ParseArtifactFormat(raw)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseArtifactFormat(%q) = (%q, %v), want (%q, %v)", raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestArtifactFormatFromFilename(t *testing.T) {
	t.Parallel()

	cases := map[string]ArtifactFormat{
		"printmaster-agent-v0.32.0-beta.1-linux-amd64":                                                  ArtifactFormatBinary,
		"printmaster-agent-v0.32.0-windows-amd64.exe":                                                   ArtifactFormatBinary,
		"printmaster-agent-v0.32.0-windows-amd64.msi":                                                   ArtifactFormatMSI,
		"printmaster-agent_0.32.0_amd64.deb":                                                            ArtifactFormatDeb,
		"printmaster-agent-0.32.0-1.fc43.x86_64.RPM":                                                    ArtifactFormatRPM,
		`C:\cache\agent\v0.32.0\windows-amd64\printmaster-agent-v0.32.0-windows-amd64.msi`:              ArtifactFormatMSI,
		"https://github.com/o/r/releases/download/agent-v0.32.0/printmaster-agent_0.32.0_arm64.deb?x=1": ArtifactFormatDeb,
	}
	for name, want := range cases {
		if got := ArtifactFormatFromFilename(name); got != want {
			t.Errorf("ArtifactFormatFromFilename(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestArtifactFormatFleetDeliverable(t *testing.T) {
	t.Parallel()

	for format, want := range map[ArtifactFormat]bool{
		ArtifactFormatBinary: true,
		ArtifactFormatMSI:    true,
		ArtifactFormatDeb:    false,
		ArtifactFormatRPM:    false,
	} {
		if got := format.FleetDeliverable(); got != want {
			t.Errorf("%s.FleetDeliverable() = %v, want %v", format, got, want)
		}
	}
}
