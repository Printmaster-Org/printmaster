package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"printmaster/common/updatepolicy"
)

const progressFile = "progress.json"

func writeUpdateState(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".update-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (m *Manager) progressSnapshotLocked() *updatepolicy.ProgressSnapshot {
	if m.progress.RunID == "" {
		return nil
	}
	snapshot := m.progress
	return &snapshot
}

func (m *Manager) Progress() updatepolicy.ProgressSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.progress
}

func (m *Manager) restoreProgress() error {
	data, err := os.ReadFile(filepath.Join(m.stateDir, progressFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read update progress: %w", err)
	}
	if err := json.Unmarshal(data, &m.progress); err != nil {
		return m.recoverInvalidProgress(err)
	}
	if err := m.progress.Validate(); err != nil {
		return m.recoverInvalidProgress(err)
	}
	m.status = Status(m.progress.Status)
	if !m.progress.Terminal() {
		if _, err := os.Stat(filepath.Join(m.stateDir, pendingUpdateFile)); errors.Is(err, os.ErrNotExist) {
			return m.publishProgress(StatusFailed, -1, "Update interrupted before restart; safe to retry", "UPDATE_INTERRUPTED")
		} else if err != nil {
			return fmt.Errorf("inspect pending update: %w", err)
		}
	}
	return nil
}

func (m *Manager) recoverInvalidProgress(cause error) error {
	m.logError("Invalid update journal; preserving it and allowing a new operation", "error", cause)
	path := filepath.Join(m.stateDir, progressFile)
	if err := os.Rename(path, path+fmt.Sprintf(".corrupt-%d", time.Now().UnixNano())); err != nil {
		return fmt.Errorf("preserve invalid update journal: %w", err)
	}
	m.progress = updatepolicy.ProgressSnapshot{}
	return m.publishProgress(StatusFailed, -1, "Invalid update journal; safe to retry", cause.Error())
}

func (m *Manager) beginOperation(ctx context.Context) error {
	channel := m.operationChannel(ctx)
	m.mu.Lock()
	now := m.clock().UTC().Truncate(time.Millisecond)
	if !now.After(m.progress.RunStartedAt) {
		now = m.progress.RunStartedAt.Add(time.Millisecond)
	}
	m.progress = updatepolicy.ProgressSnapshot{
		RunID: fmt.Sprintf("run-%d", now.UnixMilli()), RunStartedAt: now,
		Progress: -1,
	}
	m.currentRun = &UpdateRun{
		ID: m.progress.RunID, RequestedAt: now, CurrentVersion: m.currentVersion,
		Platform: m.platform, Arch: m.arch, Channel: channel,
	}
	m.cancelled = false
	m.progressErr = nil
	m.mu.Unlock()
	if err := m.publishProgress(StatusChecking, -1, "Checking for an applicable update...", ""); err != nil {
		m.setStatusWithError(StatusFailed, ErrCodeServerError, err.Error())
		return err
	}
	return nil
}

func (m *Manager) finishOperation(err error) (operationErr error) {
	defer func() {
		m.mu.RLock()
		persistenceErr := m.progressErr
		m.mu.RUnlock()
		operationErr = errors.Join(operationErr, persistenceErr)
	}()
	if err != nil {
		if !m.Progress().Terminal() {
			if m.IsCancelled() {
				m.setStatusWithProgress(StatusCancelled, -1, "Update cancelled")
			} else {
				m.setStatusWithError(StatusFailed, ErrCodeServerError, err.Error())
			}
		}
		if removeErr := os.Remove(filepath.Join(m.stateDir, pendingUpdateFile)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			m.logError("Failed to clear pending update after failure", "error", removeErr)
		}
		return err
	}
	if m.Progress().Status == string(StatusChecking) {
		m.setStatusWithProgress(StatusIdle, -1, "Update check complete; no applicable update")
	}
	return nil
}

func (m *Manager) operationContext(ctx context.Context) (context.Context, func()) {
	ctx = context.WithValue(ctx, operationChannelKey{}, m.operationChannel(ctx))
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.operationCancel = cancel
	m.mu.Unlock()
	return ctx, func() {
		cancel()
		m.mu.Lock()
		m.operationCancel = nil
		m.mu.Unlock()
	}
}

// publishProgress serializes persistence and callbacks so observers see the
// same ordering as the journal. Delivery remains best-effort; heartbeats replay.
func (m *Manager) publishProgress(status Status, percent int, message, failure string) error {
	m.progressMu.Lock()
	defer m.progressMu.Unlock()
	m.mu.Lock()
	next := m.progress
	now := time.Now().UTC()
	if m.clock != nil {
		now = m.clock().UTC()
	}
	if next.RunID == "" {
		next.RunID = fmt.Sprintf("run-%d", now.UnixMilli())
		next.RunStartedAt = now.Truncate(time.Millisecond)
	}
	if next.Sequence > 0 && next.Terminal() {
		m.mu.Unlock()
		return nil
	}
	next.Sequence++
	next.Status, next.Message, next.Error, next.UpdatedAt = string(status), message, failure, now
	if percent >= 0 {
		if percent > next.Progress {
			next.Progress = percent
		}
	}
	if m.currentRun != nil {
		next.TargetVersion = m.currentRun.TargetVersion
	} else if m.latestManifest != nil {
		next.TargetVersion = m.latestManifest.Version
	}
	m.status, m.progress = status, next
	m.mu.Unlock()

	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	// Literal Managers in unit tests need not have a state directory.
	var persistErr error
	if m.stateDir != "" {
		persistErr = writeUpdateState(filepath.Join(m.stateDir, progressFile), data)
	}
	if persistErr != nil {
		next.Status = string(StatusFailed)
		next.Message = "Could not persist update lifecycle; update cannot proceed"
		next.Error = persistErr.Error()
		m.mu.Lock()
		m.progressErr = persistErr
		m.progress, m.status = next, StatusFailed
		m.mu.Unlock()
	}
	if m.snapshotCallback != nil {
		m.snapshotCallback(next)
	}
	if m.progressCallback != nil {
		var callbackErr error
		if next.Error != "" {
			callbackErr = errors.New(next.Error)
		}
		m.progressCallback(Status(next.Status), next.TargetVersion, next.Progress, next.Message, callbackErr)
	}
	return persistErr
}
