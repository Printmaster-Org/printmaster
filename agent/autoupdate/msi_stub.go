//go:build !windows

package autoupdate

// checkMSIInstallation is a stub for non-Windows platforms.
// MSI installation is only relevant on Windows.
func checkMSIInstallation(binaryPath string) (productCode string, isMSI bool, err error) {
	return "", false, nil
}
