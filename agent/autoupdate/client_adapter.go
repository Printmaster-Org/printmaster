package autoupdate

import (
	"context"
	"fmt"

	"printmaster/agent/agent"
)

// ClientAdapter adapts agent.ServerClient to the UpdateClient interface.
type ClientAdapter struct {
	client *agent.ServerClient
}

// NewClientAdapter creates a new adapter wrapping the given server client.
func NewClientAdapter(client *agent.ServerClient) *ClientAdapter {
	return &ClientAdapter{client: client}
}

// GetLatestManifest fetches the latest manifest from the server and converts it.
func (a *ClientAdapter) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	manifest, err := a.client.GetLatestManifest(ctx, component, platform, arch, channel, format)
	if err != nil {
		return nil, err
	}
	return convertManifest(manifest), nil
}

// DownloadArtifact downloads the artifact via the server client.
func (a *ClientAdapter) DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error) {
	return a.DownloadArtifactWithProgress(ctx, manifest, destPath, resumeFrom, nil)
}

// DownloadArtifactWithProgress downloads the artifact with progress reporting.
func (a *ClientAdapter) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error) {
	if manifest == nil {
		return 0, fmt.Errorf("manifest required")
	}
	// The two manifest types share an identical field layout, so a direct
	// conversion keeps them in lockstep without hand-copying each field.
	agentManifest := agent.UpdateManifest(*manifest)

	// Convert progress callback if provided
	var agentProgressCb agent.DownloadProgressCallback
	if progressCb != nil {
		agentProgressCb = func(percent int, bytesRead int64) {
			progressCb(percent, bytesRead)
		}
	}

	return a.client.DownloadArtifactWithProgress(ctx, &agentManifest, destPath, resumeFrom, agentProgressCb)
}

// convertManifest converts from agent.UpdateManifest to autoupdate.UpdateManifest.
func convertManifest(m *agent.UpdateManifest) *UpdateManifest {
	if m == nil {
		return nil
	}
	converted := UpdateManifest(*m)
	return &converted
}
