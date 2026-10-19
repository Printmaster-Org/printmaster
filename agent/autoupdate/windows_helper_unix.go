//go:build !windows

package autoupdate

import "fmt"

func launchDetachedWindowsHelper(string) error {
	return fmt.Errorf("Windows update helpers are unavailable on this platform")
}
