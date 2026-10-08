package main

import "testing"

func TestReleaseChannelDefaults(t *testing.T) {
	original := BuildType
	t.Cleanup(func() { BuildType = original })
	for _, tc := range []struct {
		buildType string
		channel   string
		include   bool
	}{
		{"release", "stable", false},
		{"beta", "beta", true},
		{"dev", "dev", true},
	} {
		t.Run(tc.buildType, func(t *testing.T) {
			BuildType = tc.buildType
			if got := selfUpdateChannel(""); got != tc.channel {
				t.Fatalf("channel = %q, want %q", got, tc.channel)
			}
			if got := shouldIncludePrerelease(""); got != tc.include {
				t.Fatalf("include prerelease = %v, want %v", got, tc.include)
			}
			if selfUpdateChannel("stable") != "stable" || shouldIncludePrerelease("false") || !shouldIncludePrerelease("true") {
				t.Fatal("explicit configuration must override build defaults")
			}
		})
	}
}
