package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteMachineOwnership(t *testing.T) {
	t.Parallel()
	// A file-backed DB lets concurrent pooled connections share the schema.
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ownership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkMachineOwnership(t, s)
	checkPendingReviewTransitions(t, s)
}

// Run the identical security assertions against both SQL dialects.
func checkMachineOwnership(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"machine-tenant-a", "machine-tenant-b"} {
		if err := s.CreateTenant(ctx, &Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{AgentID: "machine-agent-a", Token: "machine-token-a", TenantID: "machine-tenant-a", Name: "User display name", Version: "original"}
	b := &Agent{AgentID: "machine-agent-b", Token: "machine-token-b", TenantID: "machine-tenant-b"}
	for _, agent := range []*Agent{a, b} {
		if err := s.RegisterAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("NewAgentRequiresToken", func(t *testing.T) {
		candidate := &Agent{AgentID: "machine-no-token", TenantID: a.TenantID}
		if err := s.RegisterAgent(ctx, candidate); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("tokenless first registration: %v", err)
		}
		if got, err := s.GetAgent(ctx, candidate.AgentID); err == nil || got != nil {
			t.Fatalf("tokenless agent persisted: %+v, %v", got, err)
		}
	})

	t.Run("AgentConflict", func(t *testing.T) {
		for _, tc := range []struct{ token, tenant string }{
			{"replacement-token", a.TenantID},
			{a.Token, b.TenantID},
			{b.Token, b.TenantID},
			{a.Token, ""},
			{"", a.TenantID},
		} {
			candidate := *a
			candidate.Token, candidate.TenantID, candidate.Version = tc.token, tc.tenant, "tampered"
			if err := s.RegisterAgent(ctx, &candidate); !errors.Is(err, ErrOwnershipConflict) {
				t.Errorf("token/tenant conflict: got %v", err)
			}
			got, err := s.GetAgent(ctx, a.AgentID)
			if err != nil || got.Token != a.Token || got.TenantID != a.TenantID || got.Version != "original" {
				t.Fatalf("registration mutated ownership/metadata: %+v, %v", got, err)
			}
		}
		candidate := *a
		candidate.ID, candidate.Name, candidate.Version = 0, "Agent name", "legitimate-update"
		if err := s.RegisterAgent(ctx, &candidate); err != nil {
			t.Fatalf("same-token registration: %v", err)
		}
		if candidate.ID != a.ID {
			t.Errorf("re-registration ID = %d, want %d", candidate.ID, a.ID)
		}
		got, err := s.GetAgent(ctx, a.AgentID)
		if err != nil || got.Name != a.Name || got.Version != candidate.Version || got.Token != a.Token || got.TenantID != a.TenantID {
			t.Fatalf("legitimate registration lost metadata/identity: %+v, %v", got, err)
		}
	})

	d := &Device{AgentID: a.AgentID}
	d.Serial, d.IP = "machine-serial", "192.0.2.1"
	if err := s.UpsertDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	creds := &DeviceCredentials{Serial: d.Serial, Username: "owner", EncryptedPassword: "encrypted-secret", AuthType: "basic", AutoLogin: true, TenantID: a.TenantID}
	if err := s.UpsertDeviceCredentials(ctx, creds); err != nil {
		t.Fatal(err)
	}
	t.Run("DeviceConflictPreservesCredentials", func(t *testing.T) {
		for _, agentID := range []string{b.AgentID, ""} {
			foreign := *d
			foreign.AgentID, foreign.IP = agentID, "192.0.2.99"
			if err := s.UpsertDevice(ctx, &foreign); !errors.Is(err, ErrOwnershipConflict) {
				t.Fatalf("foreign device upsert: %v", err)
			}
		}
		got, err := s.GetDevice(ctx, d.Serial)
		if err != nil || got.AgentID != a.AgentID || got.IP != d.IP {
			t.Fatalf("foreign upload changed device: %+v, %v", got, err)
		}
		d.IP = "192.0.2.2"
		if err := s.UpsertDevice(ctx, d); err != nil {
			t.Fatalf("owner update: %v", err)
		}
		got, err = s.GetDevice(ctx, d.Serial)
		if err != nil || got.AgentID != a.AgentID || got.IP != d.IP {
			t.Fatalf("owner update failed: %+v, %v", got, err)
		}
		gotCreds, err := s.GetDeviceCredentials(ctx, d.Serial)
		if err != nil || gotCreds.Username != creds.Username || gotCreds.EncryptedPassword != creds.EncryptedPassword || gotCreds.TenantID != a.TenantID || !gotCreds.AutoLogin {
			t.Fatalf("credentials changed: %+v, %v", gotCreds, err)
		}
	})

	t.Run("MetricsBindingAndAsyncRetry", func(t *testing.T) {
		if err := s.SaveMetrics(ctx, nil); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("nil metrics accepted: %v", err)
		}
		for _, tc := range []struct{ serial, agentID string }{
			{d.Serial, b.AgentID}, {d.Serial, ""}, {"", a.AgentID}, {"missing-serial", a.AgentID},
		} {
			m := &MetricsSnapshot{Serial: tc.serial, AgentID: tc.agentID, Timestamp: time.Now().UTC(), PageCount: 999}
			if err := s.SaveMetrics(ctx, m); !errors.Is(err, ErrOwnershipConflict) {
				t.Fatalf("unbound metrics accepted: %+v, %v", tc, err)
			}
			latest, err := s.GetLatestMetrics(ctx, tc.serial)
			if err != nil || latest != nil {
				t.Fatalf("rejected metrics persisted: %+v, %v", latest, err)
			}
		}
		// A persisted/older async snapshot is legitimate once its device exists.
		m := &MetricsSnapshot{Serial: "async-serial", AgentID: a.AgentID, Timestamp: time.Now().UTC().Add(-24 * time.Hour), PageCount: 123}
		if err := s.SaveMetrics(ctx, m); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("missing device must reject metrics: %v", err)
		}
		async := &Device{AgentID: a.AgentID}
		async.Serial = m.Serial
		if err := s.UpsertDevice(ctx, async); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveMetrics(ctx, m); err != nil {
			t.Fatalf("async retry after device persisted: %v", err)
		}
		latest, err := s.GetLatestMetrics(ctx, m.Serial)
		if err != nil || latest == nil || latest.AgentID != a.AgentID || latest.PageCount != m.PageCount {
			t.Fatalf("async metrics not persisted: %+v, %v", latest, err)
		}
	})

	t.Run("AtomicDeletionBinding", func(t *testing.T) {
		ownedStore, ok := s.(interface {
			DeleteDeviceForAgent(context.Context, string, string) error
		})
		if !ok {
			t.Fatal("store missing atomic machine deletion")
		}
		if err := ownedStore.DeleteDeviceForAgent(ctx, d.Serial, b.AgentID); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("foreign deletion: %v", err)
		}
		if err := ownedStore.DeleteDeviceForAgent(ctx, "missing-serial", a.AgentID); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("missing deletion: %v", err)
		}
		if got, err := s.GetDeviceCredentials(ctx, d.Serial); err != nil || got.EncryptedPassword != creds.EncryptedPassword {
			t.Fatalf("foreign deletion removed credentials: %+v, %v", got, err)
		}
		if err := ownedStore.DeleteDeviceForAgent(ctx, d.Serial, a.AgentID); err != nil {
			t.Fatalf("owner deletion: %v", err)
		}
		// Rediscovery by another agent must not authorize the former owner.
		replacement := *d
		replacement.AgentID = b.AgentID
		if err := s.UpsertDevice(ctx, &replacement); err != nil {
			t.Fatal(err)
		}
		if err := ownedStore.DeleteDeviceForAgent(ctx, d.Serial, a.AgentID); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("former owner deleted rediscovered device: %v", err)
		}
		if got, err := s.GetDevice(ctx, d.Serial); err != nil || got.AgentID != b.AgentID {
			t.Fatalf("rediscovered device lost: %+v, %v", got, err)
		}
	})

	t.Run("ConcurrentFirstRegistration", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 2)
		for i, tenant := range []string{a.TenantID, b.TenantID} {
			go func(i int, tenant string) {
				<-start
				results <- s.RegisterAgent(ctx, &Agent{AgentID: "contested-agent", Token: fmt.Sprintf("contested-token-%d", i), TenantID: tenant})
			}(i, tenant)
		}
		close(start)
		assertOneMachineWinner(t, results)
	})
	t.Run("ConcurrentFirstDeviceUpload", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, agentID := range []string{a.AgentID, b.AgentID} {
			go func(agentID string) {
				<-start
				device := &Device{AgentID: agentID}
				device.Serial = "contested-serial"
				results <- s.UpsertDevice(ctx, device)
			}(agentID)
		}
		close(start)
		assertOneMachineWinner(t, results)
	})
}

func assertOneMachineWinner(t *testing.T, results <-chan error) {
	t.Helper()
	winners, rejected := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrOwnershipConflict):
			rejected++
		default:
			t.Fatalf("unexpected concurrent write error: %v", err)
		}
	}
	if winners != 1 || rejected != 1 {
		t.Fatalf("winners=%d rejected=%d, want one each", winners, rejected)
	}
}

func checkPendingReviewTransitions(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.ApprovePendingRegistration(ctx, -1, "tenant", "reviewer"); err == nil {
		t.Fatal("approved missing registration")
	}
	if err := s.RejectPendingRegistration(ctx, -1, "reviewer", "notes"); err == nil {
		t.Fatal("rejected missing registration")
	}
	for _, initial := range []string{PendingStatusPending, PendingStatusApproved, PendingStatusRejected, "invalid"} {
		t.Run("PendingReview_"+initial, func(t *testing.T) {
			reg := &PendingAgentRegistration{AgentID: "pending-" + initial, ExpiredTokenID: "expired-token", ExpiredTenantID: "expired-tenant", Status: initial}
			id, err := s.CreatePendingAgentRegistration(ctx, reg)
			if err != nil {
				t.Fatal(err)
			}
			err = s.ApprovePendingRegistration(ctx, id, "tenant", "reviewer")
			if initial == PendingStatusPending {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("approved %s registration", initial)
			}
			if err := s.ApprovePendingRegistration(ctx, id, "other-tenant", "second-reviewer"); err == nil {
				t.Fatal("repeated approval accepted")
			}
			if err := s.RejectPendingRegistration(ctx, id, "second-reviewer", "overwrite"); err == nil {
				t.Fatal("reviewed/invalid registration rejected again")
			}
			got, err := s.GetPendingAgentRegistration(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if initial == PendingStatusPending {
				if got.Status != PendingStatusApproved || got.ReviewedBy != "reviewer" || got.ReviewedAt.IsZero() || got.Notes != "" {
					t.Fatalf("approval overwritten: %+v", got)
				}
			} else if got.Status != initial || got.ReviewedBy != "" || !got.ReviewedAt.IsZero() || got.Notes != "" {
				t.Fatalf("invalid transition mutated row: %+v", got)
			}
		})
	}
	t.Run("ConcurrentApprovalRejection", func(t *testing.T) {
		reg := &PendingAgentRegistration{AgentID: "contested-pending", ExpiredTokenID: "expired-token", ExpiredTenantID: "expired-tenant"}
		id, err := s.CreatePendingAgentRegistration(ctx, reg)
		if err != nil {
			t.Fatal(err)
		}
		start, results := make(chan struct{}), make(chan error, 2)
		go func() { <-start; results <- s.ApprovePendingRegistration(ctx, id, "tenant", "approver") }()
		go func() { <-start; results <- s.RejectPendingRegistration(ctx, id, "rejecter", "rejected") }()
		close(start)
		err1, err2 := <-results, <-results
		if (err1 == nil) == (err2 == nil) {
			t.Fatalf("want exactly one transition: %v, %v", err1, err2)
		}
		got, err := s.GetPendingAgentRegistration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == PendingStatusApproved && got.ReviewedBy == "approver" && got.Notes == "" {
			return
		}
		if got.Status != PendingStatusRejected || got.ReviewedBy != "rejecter" || got.Notes != "rejected" {
			t.Fatalf("inconsistent winning review: %+v", got)
		}
	})
}
