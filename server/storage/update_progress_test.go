package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"printmaster/common/updatepolicy"
)

func TestAgentUpdateProgress(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "progress.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	testAgentUpdateProgress(t, store.BaseStore)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	agent, err := reopened.GetAgent(context.Background(), "agent-progress")
	if err != nil || agent.UpdateProgress == nil || agent.UpdateProgress.RunID != "run-two" {
		t.Fatalf("progress lost across restart: %+v %v", agent, err)
	}
}

func testAgentUpdateProgress(t *testing.T, store *BaseStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"agent-progress", "other-agent"} {
		if err := store.RegisterAgent(ctx, &Agent{AgentID: id, Token: id + "-token", RegisteredAt: now, LastSeen: now, TenantID: "tenant-progress"}); err != nil {
			t.Fatal(err)
		}
	}
	p := updatepolicy.ProgressSnapshot{RunID: "run-one", RunStartedAt: now, Sequence: 1, Status: "downloading", Progress: 40, UpdatedAt: now}
	save := func(progress updatepolicy.ProgressSnapshot, want bool) {
		t.Helper()
		got, err := store.SaveAgentUpdateProgress(ctx, "agent-progress", progress)
		if err != nil || got != want {
			t.Fatalf("save %+v: accepted=%v err=%v want=%v", progress, got, err, want)
		}
	}
	save(p, true)
	save(p, false)
	p.Sequence, p.Status, p.Progress = 3, "succeeded", 100
	save(p, true)
	stale := p
	stale.Sequence, stale.Status = 2, "restarting"
	save(stale, false)
	stale.Sequence = 4
	save(stale, false)
	stale.RunID = "older-run"
	stale.RunStartedAt = now.Add(-time.Second)
	save(stale, false)
	agent, err := store.GetAgent(ctx, "agent-progress")
	if err != nil || agent.UpdateProgress == nil || *agent.UpdateProgress != p {
		t.Fatalf("read snapshot: %+v %v", agent, err)
	}
	other, err := store.GetAgent(ctx, "other-agent")
	if err != nil || other.UpdateProgress != nil {
		t.Fatalf("snapshot leaked to another agent: %+v %v", other, err)
	}
	for _, tenants := range [][]string{nil, {"tenant-progress"}} {
		agents, err := store.ListAgentsPaginated(ctx, 10, 0, tenants)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range agents {
			if a.AgentID == "agent-progress" && (a.UpdateProgress == nil || *a.UpdateProgress != p) {
				t.Fatalf("paginated snapshot missing: %+v", a)
			}
		}
	}
	if _, err := store.ListAgents(ctx); err != nil {
		t.Fatal(err)
	}
	p.RunID, p.Sequence, p.Status = "run-two", 1, "checking"
	p.RunStartedAt = now.Add(time.Millisecond)
	save(p, true)
	p.Progress = 101
	if _, err := store.SaveAgentUpdateProgress(ctx, "agent-progress", p); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
}
