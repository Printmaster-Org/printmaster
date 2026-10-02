package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestHandleAgentsList_TenantFiltering(t *testing.T) {
	store := SetupTestStore(t)
	ctx := context.Background()

	agents := []*storage.Agent{
		{
			AgentID:         "agent-tenant-a",
			Token:           "rbac-list-token-tenant-a",
			Name:            "Tenant A Agent",
			TenantID:        "tenant-a",
			RegisteredAt:    time.Now(),
			LastSeen:        time.Now(),
			ProtocolVersion: "1",
		},
		{
			AgentID:         "agent-tenant-b",
			Token:           "rbac-list-token-tenant-b",
			Name:            "Tenant B Agent",
			TenantID:        "tenant-b",
			RegisteredAt:    time.Now(),
			LastSeen:        time.Now(),
			ProtocolVersion: "1",
		},
	}

	for _, agent := range agents {
		if err := store.RegisterAgent(ctx, agent); err != nil {
			t.Fatalf("failed to register agent %s: %v", agent.AgentID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/list", nil)
	req = InjectTestUser(req, NewTestUser(storage.RoleOperator, "tenant-a"))
	rr := httptest.NewRecorder()

	handleAgentsList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var payload []map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(payload) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(payload))
	}

	if payload[0]["agent_id"] != "agent-tenant-a" {
		t.Fatalf("expected agent-tenant-a, got %v", payload[0]["agent_id"])
	}
}

func TestHandleAgentDetails_DeleteRespectsTenantScope(t *testing.T) {
	store := SetupTestStore(t)
	ctx := context.Background()

	tenants := []string{"tenant-a", "tenant-b"}
	for idx, tenant := range tenants {
		agent := &storage.Agent{
			AgentID:         "agent-" + tenant,
			Token:           "rbac-delete-token-" + tenant,
			Name:            "Agent " + tenant,
			TenantID:        tenant,
			RegisteredAt:    time.Now(),
			LastSeen:        time.Now(),
			ProtocolVersion: "1",
		}
		if err := store.RegisterAgent(ctx, agent); err != nil {
			t.Fatalf("failed to register agent %d: %v", idx, err)
		}
	}

	// Operator limited to tenant-a should be forbidden from deleting tenant-b agent
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/agent-tenant-b", nil)
	req = InjectTestUser(req, NewTestUser(storage.RoleOperator, "tenant-a"))
	rr := httptest.NewRecorder()
	handleAgentDetails(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when deleting foreign tenant, got %d", rr.Code)
	}

	if _, err := store.GetAgent(ctx, "agent-tenant-b"); err != nil {
		t.Fatalf("expected agent-tenant-b to remain, lookup error: %v", err)
	}

	// Same operator should be allowed to delete tenant-a agent
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/agents/agent-tenant-a", nil)
	req = InjectTestUser(req, NewTestUser(storage.RoleOperator, "tenant-a"))
	rr = httptest.NewRecorder()
	handleAgentDetails(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting in-scope agent, got %d", rr.Code)
	}

	if _, err := store.GetAgent(ctx, "agent-tenant-a"); err == nil {
		t.Fatalf("expected agent-tenant-a to be deleted")
	}
}
