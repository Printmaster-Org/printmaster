//go:build !windows

package autoupdate

import (
	"fmt"
	"testing"
)

func TestWindowsHelperUnavailableOnUnix(t *testing.T) {
	t.Parallel()
	if got := fmt.Sprint(launchDetachedWindowsHelper("not-a-script")); got != "windows update helpers are unavailable on this platform" {
		t.Fatalf("unsupported helper did not fail explicitly: %s", got)
	}
}
