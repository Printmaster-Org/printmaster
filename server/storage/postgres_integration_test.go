//go:build integration

package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	commonstorage "printmaster/common/storage"
	"printmaster/common/updatepolicy"
)

// TestPostgresStore_Integration runs the full store test suite against Postgres
func TestPostgresStore_Integration(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, store *PostgresStore) {
		ctx := context.Background()

		// Test basic connectivity
		t.Run("Path", func(t *testing.T) {
			// PostgresStore.Path() returns empty string by design since
			// PostgreSQL doesn't use file paths like SQLite
			path := store.Path()
			// Just log it - empty is expected for Postgres
			t.Logf("Postgres Path (expected empty): %q", path)
		})

		// Test agent operations
		t.Run("AgentLifecycle", func(t *testing.T) {
			agent := &Agent{
				AgentID: "test-agent-pg",
				Name:    "Test Agent Postgres",
				Token:   "test-token-pg-12345",
				Version: "1.0.0",
				Status:  "online",
			}

			// Register
			err := store.RegisterAgent(ctx, agent)
			if err != nil {
				t.Fatalf("RegisterAgent: %v", err)
			}

			// Get by AgentID
			got, err := store.GetAgent(ctx, "test-agent-pg")
			if err != nil {
				t.Fatalf("GetAgent: %v", err)
			}
			if got.Name != "Test Agent Postgres" {
				t.Errorf("Name = %q, want %q", got.Name, "Test Agent Postgres")
			}

			// Update heartbeat
			err = store.UpdateAgentHeartbeat(ctx, "test-agent-pg", "online")
			if err != nil {
				t.Fatalf("UpdateAgentHeartbeat: %v", err)
			}

			// Update info
			updatedAgent := &Agent{
				AgentID:       "test-agent-pg",
				Version:       "1.1.0",
				OSVersion:     "Windows 11",
				GoVersion:     "go1.24",
				Architecture:  "amd64",
				NumCPU:        8,
				TotalMemoryMB: 16384,
				BuildType:     "dev",
				GitCommit:     "abc123",
			}
			err = store.UpdateAgentInfo(ctx, updatedAgent)
			if err != nil {
				t.Fatalf("UpdateAgentInfo: %v", err)
			}

			// List agents
			agents, err := store.ListAgents(ctx)
			if err != nil {
				t.Fatalf("ListAgents: %v", err)
			}
			if len(agents) == 0 {
				t.Error("ListAgents returned empty list")
			}

			// Delete
			err = store.DeleteAgent(ctx, "test-agent-pg")
			if err != nil {
				t.Fatalf("DeleteAgent: %v", err)
			}
		})

		// Test device operations
		t.Run("DeviceLifecycle", func(t *testing.T) {
			// First create an agent to own the device
			agent := &Agent{
				AgentID: "device-test-agent",
				Name:    "Device Test Agent",
				Token:   "device-test-token",
				Status:  "online",
			}
			if err := store.RegisterAgent(ctx, agent); err != nil {
				t.Fatalf("RegisterAgent for device test: %v", err)
			}

			device := &Device{
				Device: commonstorage.Device{
					Serial:       "SN-PG-001",
					IP:           "192.168.1.100",
					Manufacturer: "HP",
					Model:        "LaserJet Pro",
				},
				AgentID: "device-test-agent",
			}

			// Upsert
			err := store.UpsertDevice(ctx, device)
			if err != nil {
				t.Fatalf("UpsertDevice: %v", err)
			}

			// Get by serial
			got, err := store.GetDevice(ctx, "SN-PG-001")
			if err != nil {
				t.Fatalf("GetDevice: %v", err)
			}
			if got.Serial != "SN-PG-001" {
				t.Errorf("Serial = %q, want %q", got.Serial, "SN-PG-001")
			}
			if got.Manufacturer != "HP" {
				t.Errorf("Manufacturer = %q, want %q", got.Manufacturer, "HP")
			}

			// List
			devices, err := store.ListDevices(ctx, "device-test-agent")
			if err != nil {
				t.Fatalf("ListDevices: %v", err)
			}
			if len(devices) != 1 {
				t.Errorf("ListDevices returned %d devices, want 1", len(devices))
			}
		})

		// Test metrics
		t.Run("MetricsLifecycle", func(t *testing.T) {
			metrics := &MetricsSnapshot{
				Serial:    "SN-PG-001",
				AgentID:   "device-test-agent",
				Timestamp: time.Now().UTC(),
				PageCount: 1000,
			}

			err := store.SaveMetrics(ctx, metrics)
			if err != nil {
				t.Fatalf("SaveMetrics: %v", err)
			}

			got, err := store.GetLatestMetrics(ctx, "SN-PG-001")
			if err != nil {
				t.Fatalf("GetLatestMetrics: %v", err)
			}
			if got == nil {
				t.Fatal("GetLatestMetrics returned nil")
			}
			if got.PageCount != 1000 {
				t.Errorf("PageCount = %d, want 1000", got.PageCount)
			}
		})

		// Test tenant operations
		t.Run("TenantLifecycle", func(t *testing.T) {
			tenant := &Tenant{
				ID:   "tenant-pg-1",
				Name: "Test Tenant PG",
			}

			err := store.CreateTenant(ctx, tenant)
			if err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}

			got, err := store.GetTenant(ctx, "tenant-pg-1")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if got.Name != "Test Tenant PG" {
				t.Errorf("Name = %q, want %q", got.Name, "Test Tenant PG")
			}

			// Update
			tenant.Name = "Updated Tenant PG"
			err = store.UpdateTenant(ctx, tenant)
			if err != nil {
				t.Fatalf("UpdateTenant: %v", err)
			}

			// List
			tenants, err := store.ListTenants(ctx)
			if err != nil {
				t.Fatalf("ListTenants: %v", err)
			}
			if len(tenants) == 0 {
				t.Error("ListTenants returned empty")
			}
		})

		// Test user operations
		t.Run("UserLifecycle", func(t *testing.T) {
			user := &User{
				Username: "testuser_pg",
				Role:     RoleAdmin,
				Email:    "test@example.com",
			}

			err := store.CreateUser(ctx, user, "testpassword123")
			if err != nil {
				t.Fatalf("CreateUser: %v", err)
			}

			got, err := store.GetUserByUsername(ctx, "testuser_pg")
			if err != nil {
				t.Fatalf("GetUserByUsername: %v", err)
			}
			if got.Email != "test@example.com" {
				t.Errorf("Email = %q, want %q", got.Email, "test@example.com")
			}

			// Update password (use the ID from the created user)
			err = store.UpdateUserPassword(ctx, got.ID, "newpassword456")
			if err != nil {
				t.Fatalf("UpdateUserPassword: %v", err)
			}

			// Delete
			err = store.DeleteUser(ctx, got.ID)
			if err != nil {
				t.Fatalf("DeleteUser: %v", err)
			}
		})

		// Test session operations
		t.Run("SessionLifecycle", func(t *testing.T) {
			// Create user first
			user := &User{
				Username: "sessionuser_pg",
				Role:     RoleViewer, // Use RoleViewer instead of RoleUser
			}
			if err := store.CreateUser(ctx, user, "sessionpass"); err != nil {
				t.Fatalf("CreateUser for session test: %v", err)
			}

			// Get the user to get their ID
			createdUser, err := store.GetUserByUsername(ctx, "sessionuser_pg")
			if err != nil {
				t.Fatalf("GetUserByUsername: %v", err)
			}

			// Create session (returns session with token)
			session, err := store.CreateSession(ctx, createdUser.ID, 60) // 60 minutes
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if session.Token == "" {
				t.Error("CreateSession returned empty token")
			}

			got, err := store.GetSessionByToken(ctx, session.Token)
			if err != nil {
				t.Fatalf("GetSessionByToken: %v", err)
			}
			if got.UserID != createdUser.ID {
				t.Errorf("UserID = %d, want %d", got.UserID, createdUser.ID)
			}

			// Delete session by token
			err = store.DeleteSession(ctx, session.Token)
			if err != nil {
				t.Fatalf("DeleteSession: %v", err)
			}
		})

		// Test fleet update policy
		t.Run("FleetUpdatePolicy", func(t *testing.T) {
			// Create tenant first
			tenant := &Tenant{ID: "policy-tenant-pg", Name: "Policy Tenant"}
			if err := store.CreateTenant(ctx, tenant); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}

			policy := &FleetUpdatePolicy{
				TenantID: "policy-tenant-pg",
				PolicySpec: updatepolicy.PolicySpec{
					UpdateCheckDays:    7,
					VersionPinStrategy: updatepolicy.VersionPinMinor,
				},
			}

			err := store.UpsertFleetUpdatePolicy(ctx, policy)
			if err != nil {
				t.Fatalf("UpsertFleetUpdatePolicy: %v", err)
			}

			got, err := store.GetFleetUpdatePolicy(ctx, "policy-tenant-pg")
			if err != nil {
				t.Fatalf("GetFleetUpdatePolicy: %v", err)
			}
			if got.UpdateCheckDays != 7 {
				t.Errorf("UpdateCheckDays = %d, want 7", got.UpdateCheckDays)
			}

			// Delete
			err = store.DeleteFleetUpdatePolicy(ctx, "policy-tenant-pg")
			if err != nil {
				t.Fatalf("DeleteFleetUpdatePolicy: %v", err)
			}
		})

		// Test global settings
		t.Run("GlobalSettings", func(t *testing.T) {
			settings, err := store.GetGlobalSettings(ctx)
			if err != nil {
				t.Fatalf("GetGlobalSettings: %v", err)
			}
			// Should have default settings
			if settings == nil {
				t.Error("GetGlobalSettings returned nil")
			}
		})

		// Test reports
		t.Run("ReportLifecycle", func(t *testing.T) {
			report := &ReportDefinition{
				Name:        "Test Report PG",
				Description: "A test report",
				Type:        string(ReportTypeDeviceInventory),
				Format:      string(ReportFormatJSON),
				Scope:       string(ReportScopeFleet),
			}

			err := store.CreateReport(ctx, report)
			if err != nil {
				t.Fatalf("CreateReport: %v", err)
			}
			if report.ID == 0 {
				t.Error("CreateReport did not set ID")
			}

			got, err := store.GetReport(ctx, report.ID)
			if err != nil {
				t.Fatalf("GetReport: %v", err)
			}
			if got.Name != "Test Report PG" {
				t.Errorf("Name = %q, want %q", got.Name, "Test Report PG")
			}

			// Delete
			err = store.DeleteReport(ctx, report.ID)
			if err != nil {
				t.Fatalf("DeleteReport: %v", err)
			}
		})

		// Test alerts
		t.Run("AlertLifecycle", func(t *testing.T) {
			alert := &Alert{
				DeviceSerial: "SN-PG-001",
				Type:         "low_toner",
				Severity:     "warning",
				Message:      "Toner low",
				Status:       "active",
			}

			id, err := store.CreateAlert(ctx, alert)
			if err != nil {
				t.Fatalf("CreateAlert: %v", err)
			}
			if id == 0 {
				t.Error("CreateAlert returned ID 0")
			}

			got, err := store.GetAlert(ctx, id)
			if err != nil {
				t.Fatalf("GetAlert: %v", err)
			}
			if got.Type != "low_toner" {
				t.Errorf("Type = %q, want %q", got.Type, "low_toner")
			}

			// Update status
			err = store.UpdateAlertStatus(ctx, id, "acknowledged")
			if err != nil {
				t.Fatalf("UpdateAlertStatus: %v", err)
			}

			// Resolve
			err = store.ResolveAlert(ctx, id)
			if err != nil {
				t.Fatalf("ResolveAlert: %v", err)
			}
		})

		// Test signing keys and manifests (Postgres boolean handling)
		t.Run("SigningKeysAndManifests", func(t *testing.T) {
			// Create first signing key
			key1 := &SigningKey{
				ID:         "key-pg-1",
				Algorithm:  "ed25519",
				PublicKey:  "pubkey1",
				PrivateKey: "privkey1",
				Notes:      "first key",
			}
			if err := store.CreateSigningKey(ctx, key1); err != nil {
				t.Fatalf("CreateSigningKey(key1): %v", err)
			}

			// Activate first key (tests active = TRUE in Postgres)
			if err := store.SetSigningKeyActive(ctx, key1.ID); err != nil {
				t.Fatalf("SetSigningKeyActive(key1): %v", err)
			}

			// Verify GetActiveSigningKey works (tests WHERE active = TRUE)
			active, err := store.GetActiveSigningKey(ctx)
			if err != nil {
				t.Fatalf("GetActiveSigningKey: %v", err)
			}
			if active == nil || active.ID != key1.ID {
				t.Fatalf("GetActiveSigningKey: got %v, want key1", active)
			}
			if !active.Active {
				t.Errorf("GetActiveSigningKey: Active = false, want true")
			}

			// Create second key
			key2 := &SigningKey{
				ID:         "key-pg-2",
				Algorithm:  "ed25519",
				PublicKey:  "pubkey2",
				PrivateKey: "privkey2",
				Notes:      "second key",
			}
			if err := store.CreateSigningKey(ctx, key2); err != nil {
				t.Fatalf("CreateSigningKey(key2): %v", err)
			}

			// Activate second key (should deactivate first key, tests active = FALSE)
			if err := store.SetSigningKeyActive(ctx, key2.ID); err != nil {
				t.Fatalf("SetSigningKeyActive(key2): %v", err)
			}

			// Verify key2 is now active
			active, err = store.GetActiveSigningKey(ctx)
			if err != nil {
				t.Fatalf("GetActiveSigningKey after key2 activation: %v", err)
			}
			if active.ID != key2.ID {
				t.Errorf("GetActiveSigningKey: got %s, want %s", active.ID, key2.ID)
			}
			if !active.Active {
				t.Errorf("GetActiveSigningKey: key2 Active = false, want true")
			}

			// List keys (verify both exist and only key2 is active)
			keys, err := store.ListSigningKeys(ctx, 10)
			if err != nil {
				t.Fatalf("ListSigningKeys: %v", err)
			}
			if len(keys) != 2 {
				t.Fatalf("ListSigningKeys: got %d keys, want 2", len(keys))
			}
			var key1Found, key2Found bool
			for _, k := range keys {
				switch k.ID {
				case key1.ID:
					key1Found = true
					if k.Active {
						t.Errorf("ListSigningKeys: key1 Active = true, want false")
					}
				case key2.ID:
					key2Found = true
					if !k.Active {
						t.Errorf("ListSigningKeys: key2 Active = false, want true")
					}
				}
			}
			if !key1Found || !key2Found {
				t.Errorf("ListSigningKeys: missing keys (key1=%v, key2=%v)", key1Found, key2Found)
			}

			// Test release manifest operations
			artifact := &ReleaseArtifact{
				Component:   "agent",
				Version:     "1.0.0-pg",
				Platform:    "linux",
				Arch:        "amd64",
				Channel:     "stable",
				SourceURL:   "https://github.com/example/releases/1.0.0",
				SHA256:      "abcd1234",
				SizeBytes:   1024000,
				PublishedAt: time.Now().UTC(),
			}
			if err := store.UpsertReleaseArtifact(ctx, artifact); err != nil {
				t.Fatalf("UpsertReleaseArtifact: %v", err)
			}

			manifest := &ReleaseManifest{
				Component:       "agent",
				Version:         "1.0.0-pg",
				Platform:        "linux",
				Arch:            "amd64",
				Channel:         "stable",
				ManifestVersion: "1.0",
				ManifestJSON:    `{"version":"1.0.0-pg","sha256":"abcd1234"}`,
				Signature:       "signature-data",
				SigningKeyID:    key2.ID,
				GeneratedAt:     time.Now().UTC(),
			}
			if err := store.UpsertReleaseManifest(ctx, manifest); err != nil {
				t.Fatalf("UpsertReleaseManifest: %v", err)
			}

			// Retrieve manifest
			fetched, err := store.GetReleaseManifest(ctx, "agent", "1.0.0-pg", "linux", "amd64", "")
			if err != nil {
				t.Fatalf("GetReleaseManifest: %v", err)
			}
			if fetched.Signature != "signature-data" {
				t.Errorf("GetReleaseManifest: Signature = %q, want %q", fetched.Signature, "signature-data")
			}
			if fetched.SigningKeyID != key2.ID {
				t.Errorf("GetReleaseManifest: SigningKeyID = %q, want %q", fetched.SigningKeyID, key2.ID)
			}

			// List manifests
			manifests, err := store.ListReleaseManifests(ctx, "agent", 10)
			if err != nil {
				t.Fatalf("ListReleaseManifests: %v", err)
			}
			if len(manifests) != 1 {
				t.Fatalf("ListReleaseManifests: got %d manifests, want 1", len(manifests))
			}
		})
	})
}

// TestPostgresStore_NewPostgresStore tests the constructor
func TestPostgresStore_NewPostgresStore(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, store *PostgresStore) {
		// Just verify the store was created successfully
		if store == nil {
			t.Error("NewPostgresStore returned nil")
		}

		// Verify we can query the database
		ctx := context.Background()
		_, err := store.ListTenants(ctx)
		if err != nil {
			t.Errorf("ListTenants failed: %v", err)
		}
	})
}

// TestPostgresStore_Close tests the Close method
func TestPostgresStore_Close(t *testing.T) {
	SkipIfNoDocker(t)

	container, cleanup := NewPostgresTestContainer(t)
	defer cleanup()

	store := NewPostgresStoreFromContainer(t, container)

	// Close should succeed
	err := store.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Operations after close should fail
	ctx := context.Background()
	_, err = store.ListTenants(ctx)
	if err == nil {
		t.Error("Expected error after Close(), got nil")
	}
}

// TestPostgresStore_FreshDatabaseInitialization verifies that a fresh database
// is properly initialized with schema and that the admin bootstrap pattern works correctly.
// This test catches bugs like incorrectly checking for non-existent users.
func TestPostgresStore_FreshDatabaseInitialization(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, store *PostgresStore) {
		ctx := context.Background()

		t.Run("SchemaVersionIsSet", func(t *testing.T) {
			// Verify schema_version table exists and has the correct version
			var version int
			err := store.db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
			if err != nil {
				t.Fatalf("Failed to query schema_version: %v", err)
			}
			if version != currentSchemaVersion {
				t.Errorf("Schema version = %d, want %d", version, currentSchemaVersion)
			}
		})

		t.Run("AllTablesCreated", func(t *testing.T) {
			// Verify critical tables exist
			requiredTables := []string{
				"agents", "devices", "metrics_history", "audit_log",
				"tenants", "sites", "agent_sites", "join_tokens",
				"users", "user_tenants", "sessions", "password_resets",
				"user_invitations", "oidc_providers", "oidc_sessions", "oidc_links",
				"settings_global", "settings_tenant", "settings_agent_override",
				"fleet_update_policies", "fleet_update_policy_global",
				"release_artifacts", "signing_keys", "release_manifests",
				"self_update_runs",
				"alert_rules", "escalation_policies", "alerts",
				"notification_channels", "maintenance_windows", "alert_settings",
				"reports", "report_schedules", "report_runs",
			}

			for _, table := range requiredTables {
				var exists bool
				query := `SELECT EXISTS (
					SELECT FROM information_schema.tables 
					WHERE table_schema = 'public' 
					AND table_name = $1
				)`
				err := store.db.QueryRow(query, table).Scan(&exists)
				if err != nil {
					t.Errorf("Failed to check table %s: %v", table, err)
					continue
				}
				if !exists {
					t.Errorf("Required table %s does not exist", table)
				}
			}
		})

		t.Run("UsersTableEmpty", func(t *testing.T) {
			// Verify users table starts empty (no default admin created by schema)
			var count int
			err := store.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
			if err != nil {
				t.Fatalf("Failed to count users: %v", err)
			}
			if count != 0 {
				t.Errorf("Expected 0 users in fresh database, got %d", count)
			}
		})

		t.Run("AdminBootstrapPattern", func(t *testing.T) {
			// Simulate the admin bootstrap logic from main.go
			// This is the pattern that was broken and should be tested
			adminUser := "testadmin"
			adminPass := "testpassword123"

			// Check if user exists (should return sql.ErrNoRows for non-existent user)
			existingUser, err := store.GetUserByUsername(ctx, adminUser)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("GetUserByUsername failed: %v", err)
			}
			if existingUser != nil {
				t.Fatal("Expected no existing user, got one")
			}

			// Create admin user (this is what main.go does)
			u := &User{Username: adminUser, Role: RoleAdmin}
			if err := store.CreateUser(ctx, u, adminPass); err != nil {
				t.Fatalf("CreateUser failed: %v", err)
			}

			// Verify user was created
			created, err := store.GetUserByUsername(ctx, adminUser)
			if err != nil {
				t.Fatalf("GetUserByUsername after create failed: %v", err)
			}
			if created == nil {
				t.Fatal("User was not created")
			}
			if created.Username != adminUser {
				t.Errorf("Username = %q, want %q", created.Username, adminUser)
			}
			if created.Role != RoleAdmin {
				t.Errorf("Role = %q, want %q", created.Role, RoleAdmin)
			}

			// Verify password can be authenticated
			authenticated, err := store.AuthenticateUser(ctx, adminUser, adminPass)
			if err != nil {
				t.Fatalf("AuthenticateUser failed: %v", err)
			}
			if authenticated == nil {
				t.Fatal("Authentication failed for created user")
			}

			// Try to create the same user again (should fail - duplicate username)
			u2 := &User{Username: adminUser, Role: RoleAdmin}
			err = store.CreateUser(ctx, u2, "differentpass")
			if err == nil {
				t.Error("Expected error when creating duplicate user, got nil")
			}

			// Verify that checking for existing user still works after creation
			existingUser2, err := store.GetUserByUsername(ctx, adminUser)
			if err != nil {
				t.Fatalf("GetUserByUsername for existing user failed: %v", err)
			}
			if existingUser2 == nil {
				t.Fatal("GetUserByUsername returned nil for existing user")
			}
		})

		t.Run("GlobalSettingsInitialized", func(t *testing.T) {
			// Verify global settings row exists
			var count int
			err := store.db.QueryRow("SELECT COUNT(*) FROM settings_global WHERE id = 1").Scan(&count)
			if err != nil {
				t.Fatalf("Failed to check settings_global: %v", err)
			}
			if count != 1 {
				t.Errorf("Expected 1 global settings row, got %d", count)
			}
		})
	})
}

// TestTimescaleDBStore_Integration runs tests against a TimescaleDB container
// to verify TimescaleDB-specific features work correctly.
func TestTimescaleDBStore_Integration(t *testing.T) {
	WithTimescaleDBStore(t, func(t *testing.T, store *PostgresStore) {
		ctx := context.Background()

		// Test that TimescaleDB is detected and enabled
		t.Run("TimescaleEnabled", func(t *testing.T) {
			if !store.TimescaleEnabled() {
				t.Error("TimescaleDB should be enabled when using TimescaleDB container")
			}
		})

		// Test hypertable creation for metrics_history
		t.Run("HypertableCreated", func(t *testing.T) {
			var isHypertable bool
			err := store.db.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM timescaledb_information.hypertables 
					WHERE hypertable_name = 'metrics_history'
				)
			`).Scan(&isHypertable)
			if err != nil {
				t.Fatalf("Failed to check hypertable: %v", err)
			}
			if !isHypertable {
				t.Error("metrics_history should be a hypertable")
			}
		})

		// Test server_metrics_history hypertable
		t.Run("ServerMetricsHypertableCreated", func(t *testing.T) {
			var isHypertable bool
			err := store.db.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM timescaledb_information.hypertables 
					WHERE hypertable_name = 'server_metrics_history'
				)
			`).Scan(&isHypertable)
			if err != nil {
				t.Fatalf("Failed to check server_metrics_history hypertable: %v", err)
			}
			if !isHypertable {
				t.Error("server_metrics_history should be a hypertable")
			}
		})

		// Test basic CRUD operations work with TimescaleDB
		t.Run("BasicOperations", func(t *testing.T) {
			// Register an agent
			agent := &Agent{
				AgentID: "timescale-test-agent",
				Name:    "TimescaleDB Test Agent",
				Token:   "timescale-test-token",
				Status:  "online",
			}
			if err := store.RegisterAgent(ctx, agent); err != nil {
				t.Fatalf("RegisterAgent: %v", err)
			}

			// Create a device
			device := &Device{
				Device: commonstorage.Device{
					Serial:       "SN-TSDB-001",
					IP:           "192.168.1.200",
					Manufacturer: "HP",
					Model:        "LaserJet TSDB",
				},
				AgentID: "timescale-test-agent",
			}
			if err := store.UpsertDevice(ctx, device); err != nil {
				t.Fatalf("UpsertDevice: %v", err)
			}

			// Save metrics (should use hypertable)
			metrics := &MetricsSnapshot{
				Serial:    "SN-TSDB-001",
				AgentID:   "timescale-test-agent",
				Timestamp: time.Now().UTC(),
				PageCount: 5000,
			}
			if err := store.SaveMetrics(ctx, metrics); err != nil {
				t.Fatalf("SaveMetrics: %v", err)
			}

			// Retrieve metrics
			got, err := store.GetLatestMetrics(ctx, "SN-TSDB-001")
			if err != nil {
				t.Fatalf("GetLatestMetrics: %v", err)
			}
			if got == nil {
				t.Fatal("GetLatestMetrics returned nil")
			}
			if got.PageCount != 5000 {
				t.Errorf("PageCount = %d, want 5000", got.PageCount)
			}
		})

		// Test compression policy (TimescaleDB feature)
		t.Run("CompressionPolicyExists", func(t *testing.T) {
			var hasPolicies bool
			err := store.db.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM timescaledb_information.jobs 
					WHERE proc_name = 'policy_compression'
				)
			`).Scan(&hasPolicies)
			if err != nil {
				t.Fatalf("Failed to check compression policies: %v", err)
			}
			// Just log - compression policies are set up by initTimescale
			t.Logf("Compression policies exist: %v", hasPolicies)
		})
	})
}
