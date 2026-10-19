//go:build !windows

package main

func windowsServiceExecutablePath() string {
	return ""
}

func prepareWindowsServiceExecutable() error {
	return nil
}

func protectWindowsServiceDirectory(string) error {
	return nil
}
