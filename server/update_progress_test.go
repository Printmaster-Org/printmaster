package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatepolicy"
)

func TestAgentUpdateProgressTelemetryBoundToCredentials(t *testing.T) {
	f := newBoundaryFixture(t)
	snapshot := updatepolicy.ProgressSnapshot{
		RunID: "run-one", RunStartedAt: time.Now().UTC().Truncate(time.Millisecond),
		Sequence: 1, Status: "restarting", Progress: 80, UpdatedAt: time.Now().UTC(),
	}
	post := func(token, agentID string, p updatepolicy.ProgressSnapshot) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(map[string]interface{}{"agent_id": agentID, "status": p.Status, "update_progress": p})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/update/telemetry", strings.NewReader(string(data)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		requireAuth(handleAgentUpdateTelemetry)(w, r)
		return w
	}
	for _, tc := range []struct {
		token, id string
		code      int
	}{
		{"", "boundary-agent-a", http.StatusUnauthorized},
		{"boundary-machine-a", "boundary-agent-b", http.StatusForbidden},
		{"boundary-machine-a", "boundary-agent-a", http.StatusOK},
	} {
		if w := post(tc.token, tc.id, snapshot); w.Code != tc.code {
			t.Fatalf("telemetry %s/%s: %d %s", tc.token, tc.id, w.Code, w.Body.String())
		}
	}
	snapshot.Status, snapshot.Sequence, snapshot.Progress = "succeeded", 3, 100
	if w := post("boundary-machine-a", "boundary-agent-a", snapshot); w.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", w.Code, w.Body.String())
	}
	stale := snapshot
	stale.Status, stale.Sequence, stale.Progress = "restarting", 2, 80
	if w := post("boundary-machine-a", "boundary-agent-a", stale); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	agent, err := f.store.GetAgent(t.Context(), "boundary-agent-a")
	if err != nil || agent.UpdateProgress == nil || agent.UpdateProgress.Status != "succeeded" {
		t.Fatalf("completion regressed: %+v %v", agent, err)
	}
	snapshot.Progress = 101
	if w := post("boundary-machine-a", "boundary-agent-a", snapshot); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid progress: %d", w.Code)
	}
}
