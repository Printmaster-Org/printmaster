package main

import (
	"testing"
	"time"

	commonstorage "printmaster/common/storage"
	"printmaster/server/storage"
)

func TestDeriveDeviceStatusRequiresRecentObservation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		age      time.Duration
		missing  bool
		messages []string
		want     string
	}{
		{"recent", time.Minute, false, nil, "healthy"},
		{"removed", time.Hour, false, []string{"Ready"}, "offline"},
		{"stale fault", time.Hour, false, []string{"paper jam"}, "offline"},
		{"missing timestamp", 0, true, nil, "unknown"},
		{"future", -time.Hour, false, nil, "unknown"},
		{"jam", time.Minute, false, []string{"paper jam"}, "jam"},
		{"warning", time.Minute, false, []string{"warning"}, "warning"},
		{"explicit offline", time.Minute, false, []string{"offline"}, "offline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &storage.Device{Device: commonstorage.Device{LastSeen: time.Now().Add(-tc.age), StatusMessages: tc.messages}}
			if tc.missing {
				d.LastSeen = time.Time{}
			}
			if got := deriveDeviceStatus(d); got != tc.want {
				t.Fatalf("status=%s, want %s", got, tc.want)
			}
		})
	}
}
