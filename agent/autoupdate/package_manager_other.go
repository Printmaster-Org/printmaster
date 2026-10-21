//go:build !linux

package autoupdate

import (
	"context"
	"fmt"
)

func (m *Manager) executeRepositoryPackage(ctx context.Context, manifest *UpdateManifest) error {
	_, err := m.acquireRepositoryPackage(ctx, manifest, m.runUpdateCommandIn)
	if err == nil {
		err = fmt.Errorf("%w: privileged native package installation requires Linux systemd", ErrPublisherTrust)
	}
	m.setStatusWithError(StatusFailed, ErrCodeManifestError, err.Error())
	m.reportTelemetry(ctx, StatusFailed, ErrCodeManifestError, err.Error())
	return err
}

func (m *Manager) validateRepositoryPostUpdate(state PendingUpdateState) (bool, string, error) {
	err := fmt.Errorf("native package pending state cannot be reconciled on this platform")
	m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
	return true, state.FromVersion, err
}

func (m *Manager) repositoryHandoffBlocker(PendingUpdateState) error {
	return fmt.Errorf("native package handoff cannot be reconciled on this platform")
}
