package storage

import (
	"context"
	"testing"
	"time"
)

func TestAgentLifecycle(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Register an agent
	agent := &Agent{
		AgentID:         "agent-uuid-123",
		Name:            "Test Agent",
		Hostname:        "test-host",
		IP:              "192.168.1.100",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Status:          "active",
		Token:           "test-token-abc123",
	}

	err = s.RegisterAgent(ctx, agent)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if agent.ID == 0 {
		t.Fatal("expected non-zero agent ID")
	}

	// Get agent by ID
	got, err := s.GetAgent(ctx, "agent-uuid-123")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got == nil {
		t.Fatal("expected agent, got nil")
	}
	if got.Name != "Test Agent" {
		t.Errorf("name mismatch: got=%q", got.Name)
	}
	if got.Platform != "linux" {
		t.Errorf("platform mismatch: got=%q", got.Platform)
	}

	// Get agent by token
	gotByToken, err := s.GetAgentByToken(ctx, "test-token-abc123")
	if err != nil {
		t.Fatalf("GetAgentByToken: %v", err)
	}
	if gotByToken == nil {
		t.Fatal("expected agent by token, got nil")
	}
	if gotByToken.AgentID != "agent-uuid-123" {
		t.Errorf("agent_id mismatch: got=%q", gotByToken.AgentID)
	}

	// List agents
	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}

	// Update heartbeat
	err = s.UpdateAgentHeartbeat(ctx, "agent-uuid-123", "active")
	if err != nil {
		t.Fatalf("UpdateAgentHeartbeat: %v", err)
	}

	// Update agent info
	agent.Version = "1.1.0"
	agent.OSVersion = "Ubuntu 22.04"
	agent.Architecture = "amd64"
	agent.NumCPU = 4
	agent.TotalMemoryMB = 8192
	err = s.UpdateAgentInfo(ctx, agent)
	if err != nil {
		t.Fatalf("UpdateAgentInfo: %v", err)
	}

	got, _ = s.GetAgent(ctx, "agent-uuid-123")
	if got.Version != "1.1.0" {
		t.Errorf("version not updated: got=%q", got.Version)
	}
	if got.OSVersion != "Ubuntu 22.04" {
		t.Errorf("os_version not updated: got=%q", got.OSVersion)
	}

	// Update agent name
	err = s.UpdateAgentName(ctx, "agent-uuid-123", "Renamed Agent")
	if err != nil {
		t.Fatalf("UpdateAgentName: %v", err)
	}

	got, _ = s.GetAgent(ctx, "agent-uuid-123")
	if got.Name != "Renamed Agent" {
		t.Errorf("name not updated: got=%q", got.Name)
	}

	// Delete agent
	err = s.DeleteAgent(ctx, "agent-uuid-123")
	if err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	got, err = s.GetAgent(ctx, "agent-uuid-123")
	if err == nil {
		t.Fatalf("GetAgent after delete: expected error for deleted agent")
	}
	if got != nil {
		t.Error("expected nil agent after delete")
	}
}

func TestAgentDeploymentReplacementAndReactivation(t *testing.T) {
	t.Parallel()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.CreateTenant(ctx, &Tenant{ID: "tenant-deploy", Name: "Deployment Tenant"}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := store.CreateSite(ctx, &Site{ID: "site-deploy", TenantID: "tenant-deploy", Name: "Deployment Site"}); err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	oldAgent := &Agent{
		AgentID: "old-agent", Name: "Old Agent", Hostname: "host", IP: "192.0.2.1",
		Platform: "windows", Version: "1.0.0", ProtocolVersion: "1", Token: "old-secret",
		TenantID: "tenant-deploy", Status: "active",
	}
	if err := store.RegisterAgent(ctx, oldAgent); err != nil {
		t.Fatalf("RegisterAgent old: %v", err)
	}
	if err := store.AssignAgentToSite(ctx, oldAgent.AgentID, "site-deploy"); err != nil {
		t.Fatalf("AssignAgentToSite: %v", err)
	}
	device := &Device{AgentID: oldAgent.AgentID}
	device.Serial = "printer-deploy"
	device.IP = "192.0.2.2"
	if err := store.UpsertDevice(ctx, device); err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	joinToken, _, err := store.CreateJoinToken(ctx, "tenant-deploy", 60, true)
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	deployment := &AgentDeployment{
		JoinTokenID: joinToken.ID, TenantID: "tenant-deploy", Platform: "windows",
		Name: "Replacement Agent", ReplaceAgentID: oldAgent.AgentID,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := store.CreateAgentDeployment(ctx, deployment); err != nil {
		t.Fatalf("CreateAgentDeployment: %v", err)
	}

	newAgent := &Agent{
		AgentID: "new-agent", Name: "Installer Name", Hostname: "new-host", IP: "192.0.2.3",
		Platform: "windows", Version: "2.0.0", ProtocolVersion: "1", Token: "new-secret",
		TenantID: "tenant-deploy", Status: "active",
	}
	replacedID, err := store.CompleteAgentDeployment(ctx, joinToken.ID, newAgent)
	if err != nil {
		t.Fatalf("CompleteAgentDeployment: %v", err)
	}
	if replacedID != oldAgent.AgentID {
		t.Fatalf("replacement target = %q, want %q", replacedID, oldAgent.AgentID)
	}
	if newAgent.Name != deployment.Name {
		t.Fatalf("successor name = %q, want deployment name %q", newAgent.Name, deployment.Name)
	}

	retainedOld, err := store.GetAgent(ctx, oldAgent.AgentID)
	if err != nil || retainedOld.Status != "deactivated" {
		t.Fatalf("old agent must remain deactivated: agent=%+v err=%v", retainedOld, err)
	}
	if _, err := store.GetAgentByToken(ctx, oldAgent.Token); err == nil {
		t.Fatal("deactivated Agent token still authenticates")
	}
	if err := store.UpdateAgentHeartbeat(ctx, oldAgent.AgentID, "offline"); err != nil {
		t.Fatalf("late heartbeat update: %v", err)
	}
	if err := store.UpdateAgentInfo(ctx, &Agent{AgentID: oldAgent.AgentID, Status: "active", LastSeen: time.Now().UTC()}); err != nil {
		t.Fatalf("late metadata update: %v", err)
	}
	retainedOld, err = store.GetAgent(ctx, oldAgent.AgentID)
	if err != nil || retainedOld.Status != "deactivated" {
		t.Fatalf("late Agent updates reactivated the old record: agent=%+v err=%v", retainedOld, err)
	}
	if _, err := store.GetAgentByToken(ctx, oldAgent.Token); err == nil {
		t.Fatal("late Agent updates re-enabled the old token")
	}
	if _, err := store.GetAgentByToken(ctx, newAgent.Token); err != nil {
		t.Fatalf("successor token should authenticate: %v", err)
	}
	transferredDevice, err := store.GetDevice(ctx, device.Serial)
	if err != nil || transferredDevice.AgentID != newAgent.AgentID {
		t.Fatalf("device owner = %q, err=%v; want %q", transferredDevice.AgentID, err, newAgent.AgentID)
	}
	siteIDs, err := store.GetAgentSiteIDs(ctx, newAgent.AgentID)
	if err != nil || len(siteIDs) != 1 || siteIDs[0] != "site-deploy" {
		t.Fatalf("successor site assignments = %v, err=%v", siteIDs, err)
	}
	completed, err := store.GetAgentDeploymentByJoinTokenID(ctx, joinToken.ID)
	if err != nil || completed.Status != "joined" || completed.AgentID != newAgent.AgentID {
		t.Fatalf("deployment completion = %+v, err=%v", completed, err)
	}

	if err := store.SetAgentStatus(ctx, oldAgent.AgentID, "active"); err != nil {
		t.Fatalf("reactivate old agent: %v", err)
	}
	if _, err := store.GetAgentByToken(ctx, oldAgent.Token); err != nil {
		t.Fatalf("reactivated Agent token should authenticate: %v", err)
	}
}

func createPendingDeploymentFixture(t *testing.T, store interface {
	CreateTenant(context.Context, *Tenant) error
	CreateJoinToken(context.Context, string, int, bool) (*JoinToken, string, error)
	CreateAgentDeployment(context.Context, *AgentDeployment) error
}) (*JoinToken, *AgentDeployment) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateTenant(ctx, &Tenant{ID: "tenant-pending", Name: "Pending Tenant"}); err != nil {
		t.Fatal(err)
	}
	joinToken, _, err := store.CreateJoinToken(ctx, "tenant-pending", 60, true)
	if err != nil {
		t.Fatal(err)
	}
	deployment := &AgentDeployment{
		JoinTokenID: joinToken.ID, TenantID: joinToken.TenantID, Name: "Pending Linux Agent",
		Platform: "linux", ReplaceAgentID: "replace-me", Status: "pending",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := store.CreateAgentDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	return joinToken, deployment
}

func TestPendingAgentDeploymentLifecycle(t *testing.T) {
	t.Run("remove expired deployment revokes invite", func(t *testing.T) {
		store, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		joinToken, deployment := createPendingDeploymentFixture(t, store)
		if _, err := store.DB().Exec(`UPDATE agent_deployments SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute), deployment.ID); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetAgentDeployment(context.Background(), deployment.ID)
		if err != nil || got.Status != "expired" {
			t.Fatalf("deployment status = %+v, err=%v; want expired", got, err)
		}
		if err := store.DeletePendingAgentDeployment(context.Background(), deployment.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetAgentDeployment(context.Background(), deployment.ID); err != ErrAgentDeploymentNotFound {
			t.Fatalf("removed deployment lookup err = %v, want not found", err)
		}
		tokens, err := store.ListJoinTokens(context.Background(), joinToken.TenantID)
		if err != nil || len(tokens) != 1 || !tokens[0].Revoked {
			t.Fatalf("removed deployment invite = %+v, err=%v; want revoked", tokens, err)
		}
	})

	t.Run("revoke retains cancelled record", func(t *testing.T) {
		store, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		joinToken, deployment := createPendingDeploymentFixture(t, store)
		if err := store.RevokeAgentDeployment(context.Background(), deployment.ID); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetAgentDeployment(context.Background(), deployment.ID)
		if err != nil || got.Status != "revoked" {
			t.Fatalf("revoked deployment = %+v, err=%v; want revoked", got, err)
		}
		tokens, err := store.ListJoinTokens(context.Background(), joinToken.TenantID)
		if err != nil || len(tokens) != 1 || !tokens[0].Revoked {
			t.Fatalf("revoked deployment invite = %+v, err=%v; want revoked", tokens, err)
		}
	})

	t.Run("regenerate rotates token and preserves deployment details", func(t *testing.T) {
		store, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		oldToken, deployment := createPendingDeploymentFixture(t, store)
		if _, err := store.DB().Exec(`UPDATE agent_deployments SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute), deployment.ID); err != nil {
			t.Fatal(err)
		}
		replacement, rawToken, err := store.RegenerateAgentDeployment(context.Background(), deployment.ID, 45)
		if err != nil {
			t.Fatal(err)
		}
		if replacement.ID == deployment.ID || replacement.JoinTokenID == oldToken.ID || replacement.TenantID != deployment.TenantID ||
			replacement.Name != deployment.Name || replacement.Platform != deployment.Platform || replacement.ReplaceAgentID != deployment.ReplaceAgentID ||
			replacement.Status != "pending" || !replacement.ExpiresAt.After(time.Now()) || rawToken == "" {
			t.Fatalf("replacement deployment = %+v; old = %+v", replacement, deployment)
		}
		old, err := store.GetAgentDeployment(context.Background(), deployment.ID)
		if err != nil || old.Status != "cancelled" {
			t.Fatalf("old deployment = %+v, err=%v; want cancelled", old, err)
		}
		gotToken, err := store.ValidateJoinToken(context.Background(), rawToken)
		if err != nil || gotToken.ID != replacement.JoinTokenID || !gotToken.OneTime {
			t.Fatalf("replacement token = %+v, err=%v", gotToken, err)
		}
		if _, err := store.ValidateJoinToken(context.Background(), rawToken); err == nil {
			t.Fatal("replacement one-time token was accepted more than once")
		}
	})

	t.Run("joined deployment cannot be changed", func(t *testing.T) {
		store, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		joinToken, deployment := createPendingDeploymentFixture(t, store)
		if _, err := store.DB().Exec(`UPDATE agent_deployments SET status = 'joined' WHERE id = ?`, deployment.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.DeletePendingAgentDeployment(context.Background(), deployment.ID); err != ErrAgentDeploymentConflict {
			t.Fatalf("delete joined deployment err = %v, want conflict", err)
		}
		if err := store.RevokeAgentDeployment(context.Background(), deployment.ID); err != ErrAgentDeploymentConflict {
			t.Fatalf("revoke joined deployment err = %v, want conflict", err)
		}
		if _, _, err := store.RegenerateAgentDeployment(context.Background(), deployment.ID, 60); err != ErrAgentDeploymentConflict {
			t.Fatalf("regenerate joined deployment err = %v, want conflict", err)
		}
		tokens, err := store.ListJoinTokens(context.Background(), joinToken.TenantID)
		if err != nil || len(tokens) != 1 || tokens[0].Revoked {
			t.Fatalf("joined Agent invite changed: %+v, err=%v", tokens, err)
		}
	})
}

func TestAgentHeartbeatPreservesOmittedMetadata(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	original := &Agent{
		AgentID: "partial-heartbeat", Token: "partial-heartbeat-token", Name: "Custom name", Hostname: "PC",
		IP: "192.0.2.10", Platform: "Windows 11 (24H2)", OSVersion: "Windows 11 (24H2) (build 26100)",
		Version: "0.32.0-beta.2", ProtocolVersion: "1", GoVersion: "go1.27.0",
		Architecture: "amd64", NumCPU: 8, TotalMemoryMB: 16384,
		BuildType: "beta", GitCommit: "abc123", Status: "active",
	}
	if err := s.RegisterAgent(ctx, original); err != nil {
		t.Fatal(err)
	}
	update := (&HeartbeatData{Status: "active", Version: "0.32.0-beta.3"}).BuildAgentUpdate(original.AgentID)
	if err := s.UpdateAgentInfo(ctx, update); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgent(ctx, original.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "0.32.0-beta.3" || got.OSVersion != original.OSVersion ||
		got.Platform != original.Platform || got.IP != original.IP || got.Hostname != original.Hostname ||
		got.Architecture != original.Architecture || got.GoVersion != original.GoVersion ||
		got.NumCPU != original.NumCPU || got.TotalMemoryMB != original.TotalMemoryMB ||
		got.BuildType != original.BuildType || got.GitCommit != original.GitCommit ||
		got.Name != original.Name || got.ProtocolVersion != original.ProtocolVersion {
		t.Fatalf("partial heartbeat erased metadata: %+v", got)
	}
	update = (&HeartbeatData{NumCPU: 12, TotalMemoryMB: 32768}).BuildAgentUpdate(original.AgentID)
	if update == nil {
		t.Fatal("hardware-only heartbeat should update metadata")
	}
	if err := s.UpdateAgentInfo(ctx, update); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAgent(ctx, original.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NumCPU != 12 || got.TotalMemoryMB != 32768 || got.OSVersion != original.OSVersion {
		t.Fatalf("hardware update failed: %+v", got)
	}
}

func TestAgentNotFound(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Get non-existent agent
	got, err := s.GetAgent(ctx, "non-existent")
	if err == nil {
		t.Fatalf("GetAgent: expected error for non-existent agent")
	}
	if got != nil {
		t.Error("expected nil agent for non-existent agent")
	}

	// Get by non-existent token
	got, err = s.GetAgentByToken(ctx, "bad-token")
	if err == nil {
		t.Fatalf("GetAgentByToken: expected error for non-existent token")
	}
	if got != nil {
		t.Error("expected nil for non-existent token")
	}
}

func TestDeviceLifecycle(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	if err := s.RegisterAgent(ctx, &Agent{
		AgentID:         "agent-1",
		Name:            "Test Agent",
		Hostname:        "test-host",
		IP:              "192.168.1.10",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Token:           "agent-1-token",
		Status:          "active",
	}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Upsert a device
	device := &Device{
		AgentID: "agent-1",
	}
	device.Serial = "SN12345"
	device.IP = "192.168.1.50"
	device.Manufacturer = "HP"
	device.Model = "LaserJet Pro"
	device.Hostname = "printer01"

	err = s.UpsertDevice(ctx, device)
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	// Get device
	got, err := s.GetDevice(ctx, "SN12345")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got == nil {
		t.Fatal("expected device, got nil")
	}
	if got.Manufacturer != "HP" {
		t.Errorf("manufacturer mismatch: got=%q", got.Manufacturer)
	}
	if got.AgentID != "agent-1" {
		t.Errorf("agent_id mismatch: got=%q", got.AgentID)
	}

	// Update device
	device.Hostname = "printer01-updated"
	device.Model = "LaserJet Pro MFP"
	err = s.UpsertDevice(ctx, device)
	if err != nil {
		t.Fatalf("UpsertDevice (update): %v", err)
	}

	got, _ = s.GetDevice(ctx, "SN12345")
	if got.Hostname != "printer01-updated" {
		t.Errorf("hostname not updated: got=%q", got.Hostname)
	}
	if got.Model != "LaserJet Pro MFP" {
		t.Errorf("model not updated: got=%q", got.Model)
	}

	// List devices by agent
	devices, err := s.ListDevices(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}

	// List all devices
	allDevices, err := s.ListAllDevices(ctx)
	if err != nil {
		t.Fatalf("ListAllDevices: %v", err)
	}
	if len(allDevices) != 1 {
		t.Fatalf("expected 1 device in all, got %d", len(allDevices))
	}
}

func TestDeviceWithRawData(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	if err := s.RegisterAgent(ctx, &Agent{
		AgentID:         "agent-1",
		Name:            "Raw Data Agent",
		Hostname:        "raw-host",
		IP:              "192.168.1.11",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Token:           "agent-raw-token",
		Status:          "active",
	}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	device := &Device{
		AgentID: "agent-1",
	}
	device.Serial = "SN-RAWDATA"
	device.IP = "192.168.1.51"
	device.Manufacturer = "Canon"
	device.RawData = map[string]interface{}{
		"toner_black":   75,
		"toner_cyan":    50,
		"toner_magenta": 25,
		"toner_yellow":  90,
	}

	err = s.UpsertDevice(ctx, device)
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	got, err := s.GetDevice(ctx, "SN-RAWDATA")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.RawData == nil {
		t.Fatal("expected raw_data, got nil")
	}
	if len(got.RawData) != 4 {
		t.Errorf("expected 4 raw_data entries, got %d", len(got.RawData))
	}
}

func TestMultipleDevicesByAgent(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	for _, agentID := range []string{"agent-1", "agent-2"} {
		if err := s.RegisterAgent(ctx, &Agent{
			AgentID:         agentID,
			Name:            agentID,
			Hostname:        agentID + "-host",
			IP:              "192.168.1." + string(rune('1'+len(agentID))),
			Platform:        "linux",
			Version:         "1.0.0",
			ProtocolVersion: "1",
			Token:           agentID + "-token",
			Status:          "active",
		}); err != nil {
			t.Fatalf("RegisterAgent %s: %v", agentID, err)
		}
	}

	// Create devices for agent-1
	for i := 0; i < 3; i++ {
		device := &Device{
			AgentID: "agent-1",
		}
		device.Serial = "SN-A1-" + string(rune('A'+i))
		device.IP = "192.168.1.5" + string(rune('0'+i))
		err := s.UpsertDevice(ctx, device)
		if err != nil {
			t.Fatalf("UpsertDevice: %v", err)
		}
	}

	// Create devices for agent-2
	for i := 0; i < 2; i++ {
		device := &Device{
			AgentID: "agent-2",
		}
		device.Serial = "SN-A2-" + string(rune('A'+i))
		device.IP = "192.168.2.5" + string(rune('0'+i))
		err := s.UpsertDevice(ctx, device)
		if err != nil {
			t.Fatalf("UpsertDevice: %v", err)
		}
	}

	// List by agent-1
	devices, err := s.ListDevices(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListDevices agent-1: %v", err)
	}
	if len(devices) != 3 {
		t.Errorf("expected 3 devices for agent-1, got %d", len(devices))
	}

	// List by agent-2
	devices, err = s.ListDevices(ctx, "agent-2")
	if err != nil {
		t.Fatalf("ListDevices agent-2: %v", err)
	}
	if len(devices) != 2 {
		t.Errorf("expected 2 devices for agent-2, got %d", len(devices))
	}

	// List all
	allDevices, err := s.ListAllDevices(ctx)
	if err != nil {
		t.Fatalf("ListAllDevices: %v", err)
	}
	if len(allDevices) != 5 {
		t.Errorf("expected 5 devices total, got %d", len(allDevices))
	}
}

func TestMetricsLifecycle(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	if err := s.RegisterAgent(ctx, &Agent{
		AgentID:         "agent-1",
		Name:            "Metrics Agent",
		Hostname:        "metrics-host",
		IP:              "192.168.1.12",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Token:           "agent-metrics-token",
		Status:          "active",
	}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// First create a device (metrics are linked to devices by serial)
	device := &Device{
		AgentID: "agent-1",
	}
	device.Serial = "SN-METRICS"
	device.IP = "192.168.1.100"
	err = s.UpsertDevice(ctx, device)
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	// Save metrics
	metrics := &MetricsSnapshot{
		Serial:     "SN-METRICS",
		AgentID:    "agent-1",
		Timestamp:  time.Now(),
		PageCount:  1000,
		ColorPages: 300,
		MonoPages:  700,
		ScanCount:  50,
		TonerLevels: map[string]interface{}{
			"black": 80,
			"cyan":  60,
		},
	}

	err = s.SaveMetrics(ctx, metrics)
	if err != nil {
		t.Fatalf("SaveMetrics: %v", err)
	}

	// Get latest metrics
	latest, err := s.GetLatestMetrics(ctx, "SN-METRICS")
	if err != nil {
		t.Fatalf("GetLatestMetrics: %v", err)
	}
	if latest == nil {
		t.Fatal("expected metrics, got nil")
	}
	if latest.PageCount != 1000 {
		t.Errorf("page_count mismatch: got=%d", latest.PageCount)
	}
	if latest.ColorPages != 300 {
		t.Errorf("color_pages mismatch: got=%d", latest.ColorPages)
	}

	// Save another snapshot
	time.Sleep(10 * time.Millisecond) // ensure different timestamp
	metrics2 := &MetricsSnapshot{
		Serial:    "SN-METRICS",
		AgentID:   "agent-1",
		Timestamp: time.Now(),
		PageCount: 1050,
	}
	err = s.SaveMetrics(ctx, metrics2)
	if err != nil {
		t.Fatalf("SaveMetrics (2): %v", err)
	}

	// Get history
	history, err := s.GetMetricsHistory(ctx, "SN-METRICS", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetMetricsHistory: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(history))
	}
}

func TestTenantLifecycle(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Create tenant
	tenant := &Tenant{
		ID:           "tenant-123",
		Name:         "Acme Corp",
		LoginDomain:  "acme.com",
		ContactName:  "John Doe",
		ContactEmail: "john@acme.com",
		ContactPhone: "555-1234",
	}

	err = s.CreateTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Get tenant
	got, err := s.GetTenant(ctx, "tenant-123")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got == nil {
		t.Fatal("expected tenant, got nil")
	}
	if got.Name != "Acme Corp" {
		t.Errorf("name mismatch: got=%q", got.Name)
	}
	if got.LoginDomain != "acme.com" {
		t.Errorf("login_domain mismatch: got=%q", got.LoginDomain)
	}

	// Update tenant
	tenant.Name = "Acme Corporation"
	tenant.Description = "Updated description"
	err = s.UpdateTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}

	got, _ = s.GetTenant(ctx, "tenant-123")
	if got.Name != "Acme Corporation" {
		t.Errorf("name not updated: got=%q", got.Name)
	}
	if got.Description != "Updated description" {
		t.Errorf("description not updated: got=%q", got.Description)
	}

	// List tenants
	tenants, err := s.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 1 {
		t.Errorf("expected 1 tenant, got %d", len(tenants))
	}

	// Find by domain
	found, err := s.FindTenantByDomain(ctx, "acme.com")
	if err != nil {
		t.Fatalf("FindTenantByDomain: %v", err)
	}
	if found == nil {
		t.Fatal("expected tenant by domain, got nil")
	}
	if found.ID != "tenant-123" {
		t.Errorf("wrong tenant found: got=%q", found.ID)
	}
}

func TestTenantDelete(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Create tenant
	tenant := &Tenant{
		ID:          "tenant-del",
		Name:        "Delete Test Tenant",
		LoginDomain: "delete.com",
	}
	if err := s.CreateTenant(ctx, tenant); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Create a site for this tenant (should cascade delete)
	site := &Site{
		ID:       "site-del-1",
		TenantID: "tenant-del",
		Name:     "Test Site",
	}
	if err := s.CreateSite(ctx, site); err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	// Verify site exists
	sites, _ := s.ListSitesByTenant(ctx, "tenant-del")
	if len(sites) != 1 {
		t.Fatalf("expected 1 site, got %d", len(sites))
	}

	// Register an agent for this tenant
	agent := &Agent{
		AgentID:         "agent-del-1",
		Hostname:        "testhost",
		IP:              "127.0.0.1",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Token:           "test-token",
		TenantID:        "tenant-del",
	}
	if err := s.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Test CountTenantAgents
	count, err := s.CountTenantAgents(ctx, "tenant-del")
	if err != nil {
		t.Fatalf("CountTenantAgents: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 agent, got %d", count)
	}

	// Delete tenant
	if err := s.DeleteTenant(ctx, "tenant-del"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	// Verify tenant is deleted
	_, err = s.GetTenant(ctx, "tenant-del")
	if err == nil {
		t.Error("expected error getting deleted tenant")
	}

	// Verify site was cascade deleted
	sites, _ = s.ListSitesByTenant(ctx, "tenant-del")
	if len(sites) != 0 {
		t.Errorf("expected 0 sites after tenant delete, got %d", len(sites))
	}

	// Verify agent still exists but is orphaned (tenant_id = NULL)
	gotAgent, err := s.GetAgent(ctx, "agent-del-1")
	if err != nil {
		t.Fatalf("GetAgent after tenant delete: %v", err)
	}
	if gotAgent.TenantID != "" {
		t.Errorf("expected agent tenant_id to be empty, got %q", gotAgent.TenantID)
	}

	// Test delete non-existent tenant
	err = s.DeleteTenant(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error deleting non-existent tenant")
	}
}

func TestTenantDomainNotFound(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	found, err := s.FindTenantByDomain(ctx, "nonexistent.com")
	if err != nil {
		t.Fatalf("FindTenantByDomain: %v", err)
	}
	if found != nil {
		t.Error("expected nil for non-existent domain")
	}
}

func TestSiteLifecycle(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Create tenant first
	tenant := &Tenant{
		ID:   "tenant-sites",
		Name: "Sites Test Tenant",
	}
	err = s.CreateTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Create site
	site := &Site{
		ID:          "site-001",
		TenantID:    "tenant-sites",
		Name:        "Headquarters",
		Address:     "123 Main St",
		Description: "Main office location",
	}

	err = s.CreateSite(ctx, site)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	// Get site
	got, err := s.GetSite(ctx, "site-001")
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if got == nil {
		t.Fatal("expected site, got nil")
	}
	if got.Name != "Headquarters" {
		t.Errorf("name mismatch: got=%q", got.Name)
	}
	if got.TenantID != "tenant-sites" {
		t.Errorf("tenant_id mismatch: got=%q", got.TenantID)
	}

	// Update site
	site.Name = "Main Office"
	site.Description = "Updated description"
	err = s.UpdateSite(ctx, site)
	if err != nil {
		t.Fatalf("UpdateSite: %v", err)
	}

	got, _ = s.GetSite(ctx, "site-001")
	if got.Name != "Main Office" {
		t.Errorf("name not updated: got=%q", got.Name)
	}

	// List sites by tenant
	sites, err := s.ListSitesByTenant(ctx, "tenant-sites")
	if err != nil {
		t.Fatalf("ListSitesByTenant: %v", err)
	}
	if len(sites) != 1 {
		t.Errorf("expected 1 site, got %d", len(sites))
	}

	// Delete site
	err = s.DeleteSite(ctx, "site-001")
	if err != nil {
		t.Fatalf("DeleteSite: %v", err)
	}

	got, err = s.GetSite(ctx, "site-001")
	if err != nil {
		t.Fatalf("GetSite after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestAgentSiteAssignment(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Create tenant and sites
	tenant := &Tenant{ID: "tenant-assign", Name: "Assignment Test"}
	err = s.CreateTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	site1 := &Site{ID: "site-a", TenantID: "tenant-assign", Name: "Site A"}
	site2 := &Site{ID: "site-b", TenantID: "tenant-assign", Name: "Site B"}
	err = s.CreateSite(ctx, site1)
	if err != nil {
		t.Fatalf("CreateSite A: %v", err)
	}
	err = s.CreateSite(ctx, site2)
	if err != nil {
		t.Fatalf("CreateSite B: %v", err)
	}

	// Create agent
	agent := &Agent{
		AgentID:  "agent-sites-test",
		Name:     "Sites Test Agent",
		Hostname: "test-host",
		Token:    "token-sites",
		Status:   "active",
	}
	err = s.RegisterAgent(ctx, agent)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Assign agent to site A
	err = s.AssignAgentToSite(ctx, "agent-sites-test", "site-a")
	if err != nil {
		t.Fatalf("AssignAgentToSite: %v", err)
	}

	// Get agent's sites
	siteIDs, err := s.GetAgentSiteIDs(ctx, "agent-sites-test")
	if err != nil {
		t.Fatalf("GetAgentSiteIDs: %v", err)
	}
	if len(siteIDs) != 1 || siteIDs[0] != "site-a" {
		t.Errorf("unexpected site IDs: %v", siteIDs)
	}

	// Assign to site B as well
	err = s.AssignAgentToSite(ctx, "agent-sites-test", "site-b")
	if err != nil {
		t.Fatalf("AssignAgentToSite (B): %v", err)
	}

	siteIDs, _ = s.GetAgentSiteIDs(ctx, "agent-sites-test")
	if len(siteIDs) != 2 {
		t.Errorf("expected 2 sites, got %d", len(siteIDs))
	}

	// Get agents for site A
	agentIDs, err := s.GetSiteAgentIDs(ctx, "site-a")
	if err != nil {
		t.Fatalf("GetSiteAgentIDs: %v", err)
	}
	if len(agentIDs) != 1 || agentIDs[0] != "agent-sites-test" {
		t.Errorf("unexpected agent IDs: %v", agentIDs)
	}

	// Unassign from site A
	err = s.UnassignAgentFromSite(ctx, "agent-sites-test", "site-a")
	if err != nil {
		t.Fatalf("UnassignAgentFromSite: %v", err)
	}

	siteIDs, _ = s.GetAgentSiteIDs(ctx, "agent-sites-test")
	if len(siteIDs) != 1 || siteIDs[0] != "site-b" {
		t.Errorf("after unassign, expected only site-b: got %v", siteIDs)
	}

	// Set sites (replace all)
	err = s.SetAgentSites(ctx, "agent-sites-test", []string{"site-a", "site-b"})
	if err != nil {
		t.Fatalf("SetAgentSites: %v", err)
	}

	siteIDs, _ = s.GetAgentSiteIDs(ctx, "agent-sites-test")
	if len(siteIDs) != 2 {
		t.Errorf("after SetAgentSites, expected 2 sites: got %v", siteIDs)
	}
}

func TestAuditLog(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Save audit entries
	entry1 := &AuditEntry{
		ActorID:    "user-1",
		Action:     "device.create",
		TargetType: "device",
		TargetID:   "SN123",
		Details:    "Created device",
	}
	err = s.SaveAuditEntry(ctx, entry1)
	if err != nil {
		t.Fatalf("SaveAuditEntry: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	entry2 := &AuditEntry{
		ActorID:    "user-1",
		Action:     "device.update",
		TargetType: "device",
		TargetID:   "SN123",
		Details:    "Updated device status",
	}
	err = s.SaveAuditEntry(ctx, entry2)
	if err != nil {
		t.Fatalf("SaveAuditEntry (2): %v", err)
	}

	entry3 := &AuditEntry{
		ActorID:    "user-2",
		Action:     "user.login",
		TargetType: "session",
		Details:    "User logged in",
	}
	err = s.SaveAuditEntry(ctx, entry3)
	if err != nil {
		t.Fatalf("SaveAuditEntry (3): %v", err)
	}

	// Get audit log for user-1
	logs, err := s.GetAuditLog(ctx, "user-1", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(logs) != 2 {
		t.Errorf("expected 2 entries for user-1, got %d", len(logs))
	}

	// Get all audit logs (empty actor)
	allLogs, err := s.GetAuditLog(ctx, "", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetAuditLog (all): %v", err)
	}
	if len(allLogs) != 3 {
		t.Errorf("expected 3 total entries, got %d", len(allLogs))
	}
}

func TestDatabaseStats(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	if err := s.RegisterAgent(ctx, &Agent{AgentID: "stats-agent", Name: "Stats Agent", Token: "stats-token", Status: "active"}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Add some data
	agent := &Agent{AgentID: "stats-agent", Name: "Stats Agent", Token: "stats-token", Status: "active"}
	err = s.RegisterAgent(ctx, agent)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	device := &Device{AgentID: "stats-agent"}
	device.Serial = "STATS-SN"
	err = s.UpsertDevice(ctx, device)
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	// Get stats
	stats, err := s.GetDatabaseStats(ctx)
	if err != nil {
		t.Fatalf("GetDatabaseStats: %v", err)
	}

	if stats.Agents < 1 {
		t.Errorf("expected at least 1 agent, got %d", stats.Agents)
	}
	if stats.Devices < 1 {
		t.Errorf("expected at least 1 device, got %d", stats.Devices)
	}
}
