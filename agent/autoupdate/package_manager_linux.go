package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"printmaster/common/updatetrust"
)

func (m *Manager) executeRepositoryPackage(ctx context.Context, manifest *UpdateManifest) error {
	if err := validateRepositoryPackageSocket(repositoryPackageSocketPath); err != nil {
		err = fmt.Errorf("%w: protected native package helper is not provisioned: %w", ErrPublisherTrust, err)
		m.setStatusWithError(StatusFailed, ErrCodeManifestError, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeManifestError, err.Error())
		return err
	}
	return m.executeRepositoryPackageWith(ctx, manifest, m.runUpdateCommandIn, SubmitRepositoryPackage)
}

func (m *Manager) executeRepositoryPackageWith(ctx context.Context, manifest *UpdateManifest, run repositoryCommand,
	submit func(context.Context, *os.File, updatetrust.Target) (RepositoryPackageOutcome, error)) error {
	channel := m.operationChannel(ctx)
	m.mu.Lock()
	m.currentRun = &UpdateRun{ID: m.progress.RunID, CurrentVersion: m.currentVersion, TargetVersion: manifest.Version,
		Channel: channel, Platform: m.platform, Arch: m.arch}
	m.mu.Unlock()
	m.setStatusWithProgress(StatusDownloading, 0, "Acquiring exact authenticated repository package...")
	_, err := m.withRepositoryPackage(ctx, manifest, run, func(input *os.File, authorization updatetrust.Authorization) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.IsCancelled() {
			return fmt.Errorf("repository update cancelled before privileged handoff")
		}
		m.mu.RLock()
		progressErr := m.progressErr
		m.mu.RUnlock()
		if progressErr != nil {
			return fmt.Errorf("cannot hand off native package without durable progress: %w", progressErr)
		}
		target := authorization.Target
		state := PendingUpdateState{RunID: m.Progress().RunID, TargetVersion: target.Version, FromVersion: m.currentVersion,
			StartedAt: m.clock(), Method: target.Format, RepositoryTarget: &target}
		if state.RunID == "" {
			return fmt.Errorf("native package handoff requires a durable operation identifier")
		}
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if err := writeUpdateState(filepath.Join(m.stateDir, pendingUpdateFile), data); err != nil {
			return fmt.Errorf("persist pending native package before submission: %w", err)
		}
		m.setStatusWithProgress(StatusApplying, 68, "Independent root helper is installing the authenticated package...")
		outcome, err := submit(ctx, input, target)
		if err != nil {
			return fmt.Errorf("native package handoff unresolved; inspect root evidence before owner repair or retry: %w", err)
		}
		if err := validateRepositoryPackageOutcome(outcome); err != nil {
			return fmt.Errorf("native helper returned an invalid completion observation: %w", err)
		}
		if outcome.Status != "startup-confirmed" || outcome.Target == nil || *outcome.Target != target {
			return fmt.Errorf("native helper did not confirm the exact package/service startup; protected inspection required")
		}
		m.setStatusWithProgress(StatusRestarting, 85, "Root helper confirmed startup; waiting for the replacement Agent to reconcile...")
		return nil
	})
	if err != nil {
		m.logError("Repository package update did not complete", "package_manager", m.packageManager, "error", err)
		m.setStatusWithError(StatusFailed, ErrCodeApplyFailed, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeApplyFailed, err.Error())
	}
	return err
}

func (m *Manager) validateRepositoryPostUpdate(state PendingUpdateState) (bool, string, error) {
	return m.validateRepositoryPostUpdateFrom(state, packageHelperResultPath, os.Getpid())
}

func (m *Manager) validateRepositoryPostUpdateFrom(state PendingUpdateState, path string, pid int) (bool, string, error) {
	transaction, err := readRepositoryPackageObservation(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && transaction.StartedAt.Before(state.StartedAt) {
		m.startRepositoryReconciliation(state)
		return true, state.FromVersion, nil
	}
	if err == nil && (state.RepositoryTarget == nil || transaction.Target != *state.RepositoryTarget ||
		state.Method != transaction.Target.Format || state.TargetVersion != transaction.Target.Version ||
		state.FromVersion != transaction.PreviousVersion || state.StartedAt.IsZero()) {
		err = fmt.Errorf("root native observation does not match the pending package")
	}
	if err == nil {
		switch transaction.Phase {
		case "installing", "awaiting-startup":
			m.startRepositoryReconciliation(state)
			return true, state.FromVersion, nil
		case "startup-confirmed":
			if m.currentVersion != transaction.Target.Version || transaction.Startup.PID != pid {
				err = fmt.Errorf("root startup confirmation does not describe this running Agent")
			} else {
				if err := m.publishProgress(StatusSucceeded, 100, "Root-confirmed native package update; running version "+m.currentVersion, ""); err != nil {
					return true, state.FromVersion, err
				}
				return true, state.FromVersion, os.Remove(filepath.Join(m.stateDir, pendingUpdateFile))
			}
		case "owner-repaired":
			if m.currentVersion != transaction.RepairedVersion || transaction.Startup.PID != pid {
				err = fmt.Errorf("owner repair confirmation does not describe this running Agent")
			} else {
				message := "Original package update failed; owner repair confirmed version " + transaction.RepairedVersion
				if err := m.publishProgress(StatusFailed, -1, message, message); err != nil {
					return true, state.FromVersion, err
				}
				m.logWarn(message, "root_run_id", transaction.RunID)
				return true, state.FromVersion, os.Remove(filepath.Join(m.stateDir, pendingUpdateFile))
			}
		default:
			err = fmt.Errorf("native package run %s requires owner repair; inspect /var/lib/printmaster-update", transaction.RunID)
		}
	}
	m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
	m.logError("Native package reconciliation failed; pending state retained", "run_id", state.RunID, "error", err)
	return true, state.FromVersion, err
}

func (m *Manager) repositoryHandoffBlocker(state PendingUpdateState) error {
	transaction, err := readRepositoryPackageObservation(packageHelperResultPath)
	if err == nil && transaction.Phase == "owner-repaired" {
		if _, _, err := m.validateRepositoryPostUpdate(state); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(m.stateDir, pendingUpdateFile)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return errors.Join(fmt.Errorf("cannot start another update until the native package handoff is reconciled; inspect protected root transaction evidence"), err)
}

func (m *Manager) startRepositoryReconciliation(state PendingUpdateState) {
	m.mu.Lock()
	if m.helperReconcile {
		m.mu.Unlock()
		return
	}
	m.helperReconcile = true
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			m.helperReconcile = false
			m.mu.Unlock()
		}()
		deadline := time.NewTimer(16 * time.Minute)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-deadline.C:
				err := fmt.Errorf("native helper outcome remains unresolved after sixteen minutes; owner inspection required")
				m.setStatusWithError(StatusFailed, ErrCodeApplyFailed, err.Error())
				m.logError("Native package reconciliation timed out", "run_id", state.RunID, "error", err)
				return
			case <-ticker.C:
				if _, _, err := m.ValidatePostUpdate(); err != nil {
					return
				}
				if m.Progress().Terminal() {
					return
				}
			}
		}
	}()
}
