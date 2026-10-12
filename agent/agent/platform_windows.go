//go:build windows
// +build windows

package agent

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	ntdll             = syscall.NewLazyDLL("ntdll.dll")
	procRtlGetVersion = ntdll.NewProc("RtlGetVersion")
)

// RTL_OSVERSIONINFOEXW structure
type rtlOSVersionInfoExW struct {
	dwOSVersionInfoSize uint32
	dwMajorVersion      uint32
	dwMinorVersion      uint32
	dwBuildNumber       uint32
	dwPlatformId        uint32
	szCSDVersion        [128]uint16
	wServicePackMajor   uint16
	wServicePackMinor   uint16
	wSuiteMask          uint16
	wProductType        uint8
	wReserved           uint8
}

// getPlatformDetailed returns detailed Windows version info.
// Examples: "Windows 11 (24H2)", "Windows 10 (22H2)", "Windows Server 2022"
func getPlatformDetailed() string {
	info := rtlOSVersionInfoExW{}
	info.dwOSVersionInfoSize = uint32(unsafe.Sizeof(info))

	ret, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
	if ret != 0 {
		// RtlGetVersion failed, fall back to basic
		return "Windows"
	}

	major := info.dwMajorVersion
	minor := info.dwMinorVersion
	build := info.dwBuildNumber
	productType := info.wProductType

	// Determine Windows version name
	var versionName string
	var displayVersion string

	// Windows Server detection (product type 2 = domain controller, 3 = server)
	if productType == 2 || productType == 3 {
		versionName = getWindowsServerVersion(major, minor, build)
	} else {
		// Desktop Windows
		versionName = getWindowsDesktopVersion(major, minor, build)
		displayVersion = getWindowsDisplayVersion(build)
	}

	if versionName == "" {
		versionName = fmt.Sprintf("Windows %d.%d", major, minor)
	}

	// Add display version (e.g., "24H2") if available
	if displayVersion != "" {
		return fmt.Sprintf("%s (%s)", versionName, displayVersion)
	}

	return versionName
}

// getWindowsDesktopVersion returns the desktop Windows version name
func getWindowsDesktopVersion(major, minor, build uint32) string {
	if major == 10 && minor == 0 {
		// Windows 10/11 - differentiated by build number
		// Windows 11 starts at build 22000
		if build >= 22000 {
			return "Windows 11"
		}
		return "Windows 10"
	}
	if major == 6 {
		switch minor {
		case 3:
			return "Windows 8.1"
		case 2:
			return "Windows 8"
		case 1:
			return "Windows 7"
		case 0:
			return "Windows Vista"
		}
	}
	return ""
}

// getWindowsServerVersion returns the Windows Server version name
func getWindowsServerVersion(major, minor, build uint32) string {
	if major == 10 && minor == 0 {
		switch {
		case build >= 26100: // Windows Server 2025
			return "Windows Server 2025"
		case build >= 20348: // Windows Server 2022
			return "Windows Server 2022"
		case build >= 17763: // Windows Server 2019
			return "Windows Server 2019"
		default:
			return "Windows Server 2016"
		}
	}
	if major == 6 {
		switch minor {
		case 3:
			return "Windows Server 2012 R2"
		case 2:
			return "Windows Server 2012"
		case 1:
			return "Windows Server 2008 R2"
		case 0:
			return "Windows Server 2008"
		}
	}
	return "Windows Server"
}

// getWindowsDisplayVersion returns the display version (e.g., "24H2", "23H2")
// based on build number
func getWindowsDisplayVersion(build uint32) string {
	// Map build numbers to display versions
	// Windows 11 versions
	if build >= 26100 {
		return "24H2"
	}
	if build >= 22631 {
		return "23H2"
	}
	if build >= 22621 {
		return "22H2"
	}
	if build >= 22000 {
		return "21H2"
	}
	// Windows 10 versions
	if build >= 19045 {
		return "22H2"
	}
	if build >= 19044 {
		return "21H2"
	}
	if build >= 19043 {
		return "21H1"
	}
	if build >= 19042 {
		return "20H2"
	}
	if build >= 19041 {
		return "2004"
	}
	if build >= 18363 {
		return "1909"
	}
	if build >= 18362 {
		return "1903"
	}
	if build >= 17763 {
		return "1809"
	}
	if build >= 17134 {
		return "1803"
	}
	if build >= 16299 {
		return "1709"
	}
	if build >= 15063 {
		return "1703"
	}
	if build >= 14393 {
		return "1607"
	}
	if build >= 10586 {
		return "1511"
	}
	if build >= 10240 {
		return "1507"
	}
	return ""
}

func getOSVersionDetailed() string {
	version := getPlatformDetailed()
	if build := GetWindowsBuildNumber(); build != 0 {
		return fmt.Sprintf("%s (build %d)", version, build)
	}
	return version
}

// GetWindowsBuildNumber returns the Windows build number for diagnostics
func GetWindowsBuildNumber() uint32 {
	info := rtlOSVersionInfoExW{}
	info.dwOSVersionInfoSize = uint32(unsafe.Sizeof(info))

	ret, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
	if ret != 0 {
		return 0
	}
	return info.dwBuildNumber
}
