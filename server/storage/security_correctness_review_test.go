package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSecurityReviewOneTimeJoinToken(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkReviewOneTimeJoinToken(t, s)
	t.Run("ConsumptionFailure", func(t *testing.T) {
		s.DB().SetMaxOpenConns(1)
		ctx := context.Background()
		_, raw, err := s.CreateJoinToken(ctx, "review-token-tenant", 60, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().ExecContext(ctx, `CREATE TRIGGER fail_token_consumption BEFORE UPDATE ON join_tokens BEGIN SELECT RAISE(ABORT, 'consumption unavailable'); END`); err != nil {
			t.Fatal(err)
		}
		if token, err := s.ValidateJoinToken(ctx, raw); err == nil || token != nil {
			t.Fatalf("failed consumption authorized enrollment: %+v, %v", token, err)
		}
	})
}

func checkReviewOneTimeJoinToken(t *testing.T, s Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.CreateTenant(ctx, &Tenant{ID: "review-token-tenant", Name: "Review"}); err != nil {
		t.Fatal(err)
	}
	_, raw, err := s.CreateJoinToken(ctx, "review-token-tenant", 60, true)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 4)
	for i := 0; i < cap(results); i++ {
		go func() {
			<-start
			_, err := s.ValidateJoinToken(ctx, raw)
			results <- err
		}()
	}
	close(start)
	winners := 0
	for i := 0; i < cap(results); i++ {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, ErrTokenRevoked) {
			t.Errorf("unexpected consumption failure: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("one-time token authorized %d enrollments, want 1", winners)
	}
}

func TestSecurityReviewPendingTransitionTypedConflict(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	id, err := s.CreatePendingAgentRegistration(ctx, &PendingAgentRegistration{AgentID: "review-agent", ExpiredTokenID: "expired", ExpiredTenantID: "review-tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RejectPendingRegistration(ctx, id, "winner", ""); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{s.ApprovePendingRegistration(ctx, id, "review-tenant", "loser"), s.RejectPendingRegistration(ctx, id, "loser", "")} {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("losing transition lacks handler conflict sentinel: %v", err)
		}
	}
}

func TestSecurityReviewOwnedDeletion(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "deletion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkReviewOwnedDeletion(t, s)
}

func checkReviewOwnedDeletion(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	owned := s.(interface {
		DeleteDeviceForAgentWithMetrics(context.Context, string, string, bool) error
	})
	if err := s.CreateTenant(ctx, &Tenant{ID: "review-delete-tenant", Name: "Review deletion"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"review-delete-owner", "review-delete-foreign"} {
		if err := s.RegisterAgent(ctx, &Agent{AgentID: id, Token: id + "-token", TenantID: "review-delete-tenant"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, deleteMetrics := range []bool{false, true} {
		d := &Device{AgentID: "review-delete-owner"}
		d.Serial = "review-delete-serial"
		if err := s.UpsertDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveMetrics(ctx, &MetricsSnapshot{Serial: d.Serial, AgentID: d.AgentID, Timestamp: time.Now().UTC(), PageCount: 123}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertDeviceCredentials(ctx, &DeviceCredentials{Serial: d.Serial, TenantID: "review-delete-tenant", Username: "owner"}); err != nil {
			t.Fatal(err)
		}
		if err := owned.DeleteDeviceForAgentWithMetrics(ctx, d.Serial, "review-delete-foreign", deleteMetrics); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("foreign deletion: %v", err)
		}
		if creds, err := s.GetDeviceCredentials(ctx, d.Serial); err != nil || creds == nil || creds.Username != "owner" {
			t.Fatalf("foreign deletion removed credentials: %+v, %v", creds, err)
		}
		if metrics, err := s.GetLatestMetrics(ctx, d.Serial); err != nil || metrics == nil || metrics.PageCount != 123 {
			t.Fatalf("foreign deletion removed metrics: %+v, %v", metrics, err)
		}
		if err := owned.DeleteDeviceForAgentWithMetrics(ctx, d.Serial, d.AgentID, deleteMetrics); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetDevice(ctx, d.Serial); !errors.Is(err, sql.ErrNoRows) || got != nil {
			t.Fatalf("owner deletion failed: %+v, %v", got, err)
		}
	}
}
