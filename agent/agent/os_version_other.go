//go:build !windows

package agent

func getOSVersionDetailed() string {
	return getPlatformDetailed()
}
