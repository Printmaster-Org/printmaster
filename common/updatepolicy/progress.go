package updatepolicy

import (
	"fmt"
	"time"
)

// ProgressSnapshot is the durable, replayable state of one agent operation.
// Sequence numbers order events within a run; RunStartedAt orders different runs.
type ProgressSnapshot struct {
	RunID         string    `json:"run_id"`
	RunStartedAt  time.Time `json:"run_started_at"`
	Sequence      int64     `json:"sequence"`
	Status        string    `json:"status"`
	Progress      int       `json:"progress"`
	Message       string    `json:"message"`
	TargetVersion string    `json:"target_version,omitempty"`
	Error         string    `json:"error,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (p ProgressSnapshot) Data() map[string]interface{} {
	return map[string]interface{}{
		"run_id": p.RunID, "run_started_at": p.RunStartedAt, "sequence": p.Sequence,
		"status": p.Status, "progress": p.Progress, "message": p.Message,
		"target_version": p.TargetVersion, "error": p.Error, "updated_at": p.UpdatedAt,
	}
}

func (p ProgressSnapshot) Terminal() bool {
	switch p.Status {
	case "idle", "succeeded", "failed", "skipped", "rolled_back", "cancelled":
		return true
	}
	return false
}

func (p ProgressSnapshot) Validate() error {
	if p.RunID == "" || p.RunStartedAt.IsZero() || p.Sequence <= 0 || p.UpdatedAt.IsZero() {
		return fmt.Errorf("update progress requires run identity, sequence and timestamps")
	}
	if p.Progress < -1 || p.Progress > 100 {
		return fmt.Errorf("update progress must be between -1 and 100")
	}
	switch p.Status {
	case "idle", "checking", "pending", "downloading", "staging", "applying", "restarting",
		"verifying", "succeeded", "failed", "skipped", "rolled_back", "cancelled":
		return nil
	}
	return fmt.Errorf("invalid update status %q", p.Status)
}

func (p ProgressSnapshot) NewerThan(previous ProgressSnapshot) bool {
	if previous.RunID == "" {
		return true
	}
	if p.RunID == previous.RunID {
		return !previous.Terminal() && p.Sequence > previous.Sequence
	}
	return p.RunStartedAt.After(previous.RunStartedAt)
}
