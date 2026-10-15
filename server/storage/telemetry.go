package storage

import (
	"fmt"
	"time"
)

// telemetryReporterSQL keeps reporting permission separate from management
// ownership. Empty tenant IDs never establish a shared reporting boundary.
func telemetryReporterSQL(owner, reporter string) string {
	return `(` + owner + ` = ` + reporter + ` OR EXISTS (
		SELECT 1 FROM agents telemetry_owner
		JOIN agents telemetry_reporter ON telemetry_reporter.tenant_id = telemetry_owner.tenant_id
		WHERE telemetry_owner.agent_id = ` + owner + `
		AND telemetry_owner.tenant_id <> ''
		AND telemetry_reporter.agent_id = ` + reporter + `))`
}

const eligibleMetricSQL = `EXISTS (SELECT 1 FROM devices telemetry_device
	WHERE telemetry_device.serial = metrics_history.serial AND `

func metricEligibilitySQL() string {
	return eligibleMetricSQL + telemetryReporterSQL("telemetry_device.agent_id", "metrics_history.agent_id") + ")"
}

func parseMetricTimestamp(value string) (time.Time, error) {
	// SQLite aggregates/window projections can lose the DATETIME column type
	// and return the stored Go timestamp text instead of a time.Time.
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05"} {
		if timestamp, err := time.Parse(layout, value); err == nil {
			return timestamp, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid metric timestamp %q", value)
}
