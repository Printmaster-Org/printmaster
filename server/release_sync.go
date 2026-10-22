package main

import (
	"time"

	"printmaster/server/releases"
)

// The last manual sync survives browser reload, not a Server process restart.
type releaseSyncSnapshot struct {
	releases.SyncProgress
	RunID        string    `json:"run_id"`
	RunStartedAt time.Time `json:"run_started_at"`
	Sequence     int64     `json:"sequence"`
	UpdatedAt    time.Time `json:"updated_at"`
	Deadline     time.Time `json:"deadline"`
}

func copyReleaseSyncStateLocked() *releaseSyncSnapshot {
	if releaseSyncState == nil {
		return nil
	}
	state := *releaseSyncState
	return &state
}

func recordReleaseSyncProgress(runID string, progress releases.SyncProgress) {
	releaseSyncMu.Lock()
	defer releaseSyncMu.Unlock()
	if releaseSyncState == nil || releaseSyncState.RunID != runID ||
		releaseSyncState.Phase == "complete" || releaseSyncState.Phase == "error" {
		return
	}
	releaseSyncState.SyncProgress = progress
	releaseSyncState.Sequence++
	releaseSyncState.UpdatedAt = time.Now().UTC()
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{Type: "release_sync_progress", Data: map[string]interface{}{
			"run_id": releaseSyncState.RunID, "run_started_at": releaseSyncState.RunStartedAt,
			"sequence": releaseSyncState.Sequence, "updated_at": releaseSyncState.UpdatedAt,
			"deadline": releaseSyncState.Deadline,
			"phase":    progress.Phase, "message": progress.Message, "error": progress.Error,
			"total_files": progress.TotalFiles, "completed_files": progress.CompletedFiles,
			"total_bytes": progress.TotalBytes, "completed_bytes": progress.CompletedBytes,
			"current_file": progress.CurrentFile, "current_file_size": progress.CurrentFileSize,
			"percent_complete": progress.PercentComplete,
		}})
	}
}
