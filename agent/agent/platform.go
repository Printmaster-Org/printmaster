package agent

import (
	"runtime"
	"strings"
)

// GetPlatformInfo returns a human-friendly platform string for display.
// Examples: "Windows 11 (24H2)", "Ubuntu 24.04", "macOS 15.2", "FreeBSD 14.1"
// Falls back to normalized OS name if detailed info unavailable.
func GetPlatformInfo() string {
	detailed := getPlatformDetailed()
	if detailed != "" {
		return detailed
	}
	return normalizePlatformName(runtime.GOOS)
}

// GetOSVersionDetailed returns the detailed OS version string.
// This is what goes in the os_version field.
func GetOSVersionDetailed() string {
	return getOSVersionDetailed()
}

// normalizePlatformName converts runtime.GOOS to a human-friendly display name.
func normalizePlatformName(goos string) string {
	switch goos {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	case "openbsd":
		return "OpenBSD"
	case "netbsd":
		return "NetBSD"
	case "dragonfly":
		return "DragonFly BSD"
	case "solaris":
		return "Solaris"
	case "illumos":
		return "illumos"
	case "aix":
		return "AIX"
	default:
		// Capitalize first letter
		if len(goos) > 0 {
			return strings.ToUpper(goos[:1]) + goos[1:]
		}
		return goos
	}
}
