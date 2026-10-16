package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"printmaster/common/updatepolicy"
)

func (s *BaseStore) SaveAgentUpdateProgress(ctx context.Context, agentID string, progress updatepolicy.ProgressSnapshot) (bool, error) {
	if agentID == "" {
		return false, fmt.Errorf("agent ID is required for update progress")
	}
	if err := progress.Validate(); err != nil {
		return false, err
	}
	data, err := json.Marshal(progress)
	if err != nil {
		return false, err
	}
	terminal := 0
	if progress.Terminal() {
		terminal = 1
	}
	result, err := s.execContext(ctx, `
		INSERT INTO agent_update_progress (agent_id, run_id, run_started_at, sequence, terminal, payload)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			run_id = excluded.run_id, run_started_at = excluded.run_started_at,
			sequence = excluded.sequence, terminal = excluded.terminal, payload = excluded.payload
		WHERE (agent_update_progress.run_id = excluded.run_id
		       AND agent_update_progress.terminal = 0 AND agent_update_progress.sequence < excluded.sequence)
		   OR (agent_update_progress.run_id <> excluded.run_id
		       AND agent_update_progress.run_started_at < excluded.run_started_at)`,
		agentID, progress.RunID, progress.RunStartedAt.UnixMilli(), progress.Sequence, terminal, string(data))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func decodeAgentUpdateProgress(data string, agent *Agent) error {
	if data == "" {
		return nil
	}
	var progress updatepolicy.ProgressSnapshot
	if err := json.Unmarshal([]byte(data), &progress); err != nil {
		return fmt.Errorf("decode update progress for %s: %w", agent.AgentID, err)
	}
	agent.UpdateProgress = &progress
	return nil
}
