package main

import (
	"context"

	"printmaster/common/updatepolicy"
)

func recordAgentUpdateProgress(ctx context.Context, agentID string, snapshot updatepolicy.ProgressSnapshot) (bool, error) {
	accepted, err := serverStore.SaveAgentUpdateProgress(ctx, agentID, snapshot)
	if err != nil || !accepted {
		return accepted, err
	}
	if snapshot.Status == "failed" {
		logError("Agent update failed", "agent_id", agentID, "run_id", snapshot.RunID, "error", snapshot.Error)
	}
	if sseHub != nil {
		data := snapshot.Data()
		data["agent_id"] = agentID
		sseHub.Broadcast(SSEEvent{Type: "update_progress", Data: data})
	}
	return true, nil
}
