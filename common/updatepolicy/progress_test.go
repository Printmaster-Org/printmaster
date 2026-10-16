package updatepolicy

import (
	"testing"
	"time"
)

func TestProgressSnapshotOrdering(t *testing.T) {
	t.Parallel()
	base := ProgressSnapshot{RunID: "one", RunStartedAt: time.Now().UTC(), Sequence: 4, Status: "restarting", Progress: 80, UpdatedAt: time.Now().UTC()}
	for _, tc := range []struct {
		name string
		edit func(*ProgressSnapshot)
		want bool
	}{
		{"duplicate", func(p *ProgressSnapshot) {}, false},
		{"stale", func(p *ProgressSnapshot) { p.Sequence-- }, false},
		{"next", func(p *ProgressSnapshot) { p.Sequence++ }, true},
		{"new run", func(p *ProgressSnapshot) {
			p.RunID = "two"
			p.RunStartedAt = p.RunStartedAt.Add(time.Millisecond)
			p.Sequence = 1
		}, true},
		{"old run", func(p *ProgressSnapshot) {
			p.RunID = "old"
			p.RunStartedAt = p.RunStartedAt.Add(-time.Millisecond)
			p.Sequence = 500
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := base
			tc.edit(&next)
			if got := next.NewerThan(base); got != tc.want {
				t.Fatalf("NewerThan = %v, want %v", got, tc.want)
			}
		})
	}
	complete := base
	complete.Status = "succeeded"
	next := base
	next.Sequence = 100
	if next.NewerThan(complete) {
		t.Fatal("terminal run accepted later progress")
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	base.Progress = 101
	if err := base.Validate(); err == nil {
		t.Fatal("out-of-range progress accepted")
	}
}
