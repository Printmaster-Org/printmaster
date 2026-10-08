package agent

import (
	"printmaster/agent/scanner"
	pmsettings "printmaster/common/settings"
)

type SNMPConfig = scanner.SNMPConfig

// RetentionConfig holds data retention settings.
type RetentionConfig struct {
	ScanHistoryDays   int
	HiddenDevicesDays int
}

func SetSNMPSettings(settings pmsettings.SNMPSettings) {
	scanner.SetSNMPSettings(settings)
}

func GetSNMPConfig() (*SNMPConfig, error) {
	return scanner.GetSNMPConfig()
}

func GetRetentionConfig() *RetentionConfig {
	return &RetentionConfig{
		ScanHistoryDays:   30,
		HiddenDevicesDays: 30,
	}
}
