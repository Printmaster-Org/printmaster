package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnrollmentMissingAgentHasTypedNotFoundError(t *testing.T) {
	t.Parallel()
	db, err := NewSQLiteStore(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	agent, err := db.GetAgent(context.Background(), "not-enrolled")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetAgent must preserve sql.ErrNoRows for safe enrollment: %v", err)
	}
	if agent != nil || !strings.HasPrefix(err.Error(), "agent not found") {
		t.Fatalf("GetAgent must preserve nil result and not-found text: %+v, %v", agent, err)
	}
}

func TestEnrollmentPreservesExistingAgentOwnership(t *testing.T) {
	t.Parallel()
	for _, originalTenant := range []string{"tenant-a", ""} {
		for _, replacement := range []struct {
			name, tenant, token string
		}{
			{"different-credential", originalTenant, "attacker-token"},
			{"different-tenant", "tenant-b", "original-token"},
			{"different-tenant-and-credential", "tenant-b", "attacker-token"},
		} {
			t.Run(originalTenant+"/"+replacement.name, func(t *testing.T) {
				t.Parallel()
				db, err := NewSQLiteStore(":memory:")
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				ctx := context.Background()
				for _, id := range []string{"tenant-a", "tenant-b"} {
					if err := db.CreateTenant(ctx, &Tenant{ID: id, Name: id}); err != nil {
						t.Fatal(err)
					}
				}
				original := &Agent{AgentID: "agent-a", Name: "Original", TenantID: originalTenant, Token: "original-token", RegisteredAt: time.Now(), LastSeen: time.Now()}
				if err := db.RegisterAgent(ctx, original); err != nil {
					t.Fatal(err)
				}
				attempt := *original
				attempt.Token, attempt.TenantID, attempt.Hostname = replacement.token, replacement.tenant, "attacker-host"
				if err := db.RegisterAgent(ctx, &attempt); err == nil {
					t.Fatal("existing identity accepted an ownership/credential replacement")
				}
				stored, err := db.GetAgent(ctx, original.AgentID)
				if err != nil || stored.Token != original.Token || stored.TenantID != original.TenantID || stored.Hostname != original.Hostname {
					t.Fatalf("rejected enrollment mutated agent: %+v, %v", stored, err)
				}
				// A storage caller proving the same credential may refresh metadata,
				// but cannot change tenant ownership as part of enrollment.
				refresh := *original
				refresh.Hostname = "legitimate-refresh"
				if err := db.RegisterAgent(ctx, &refresh); err != nil {
					t.Fatalf("credential-proven refresh failed: %v", err)
				}
			})
		}
	}
}

func TestPendingEnrollmentTransitionsRequirePendingRow(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"missing", PendingStatusApproved, PendingStatusRejected} {
		for _, action := range []string{"approve", "reject"} {
			t.Run(status+"/"+action, func(t *testing.T) {
				t.Parallel()
				db, err := NewSQLiteStore(":memory:")
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				ctx := context.Background()
				id := int64(999)
				if status != "missing" {
					id, err = db.CreatePendingAgentRegistration(ctx, &PendingAgentRegistration{AgentID: "agent", ExpiredTokenID: "expired", ExpiredTenantID: "tenant-a", Status: status})
					if err != nil {
						t.Fatal(err)
					}
				}
				if action == "approve" {
					err = db.ApprovePendingRegistration(ctx, id, "tenant-b", "admin")
				} else {
					err = db.RejectPendingRegistration(ctx, id, "admin", "rejected")
				}
				if err == nil {
					t.Fatal("transition accepted without exactly one pending row")
				}
				if status != "missing" {
					reg, err := db.GetPendingAgentRegistration(ctx, id)
					if err != nil || reg.Status != status || reg.ReviewedBy != "" {
						t.Fatalf("terminal record mutated: %+v, %v", reg, err)
					}
				}
			})
		}
	}
}

func TestConcurrentPendingEnrollmentReviewHasSingleWinner(t *testing.T) {
	t.Parallel()
	db, err := NewSQLiteStore(filepath.Join(t.TempDir(), "enrollment.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.CreatePendingAgentRegistration(ctx, &PendingAgentRegistration{AgentID: "agent", ExpiredTokenID: "expired", ExpiredTenantID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 12
	results := make(chan error, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				results <- db.ApprovePendingRegistration(ctx, id, "tenant-b", "admin")
			} else {
				results <- db.RejectPendingRegistration(ctx, id, "admin", "rejected")
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("got %d successful reviews; want exactly one", winners)
	}
}

func TestConcurrentEnrollmentCannotReplaceWinner(t *testing.T) {
	t.Parallel()
	// Pooled concurrent connections must share one schema, unlike :memory:.
	db, err := NewSQLiteStore(filepath.Join(t.TempDir(), "enrollment.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, id := range []string{"tenant-a", "tenant-b"} {
		if err := db.CreateTenant(ctx, &Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	type outcome struct {
		agent Agent
		err   error
	}
	const attempts = 12
	results := make(chan outcome, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tenantID := "tenant-a"
			if i%2 != 0 {
				tenantID = "tenant-b"
			}
			agent := Agent{AgentID: "shared-agent", TenantID: tenantID, Token: fmt.Sprintf("credential-%d", i), RegisteredAt: time.Now(), LastSeen: time.Now()}
			err := db.RegisterAgent(ctx, &agent)
			results <- outcome{agent: agent, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	var winner Agent
	for result := range results {
		if result.err == nil {
			winners++
			winner = result.agent
		} else if !errors.Is(result.err, ErrOwnershipConflict) {
			t.Errorf("unexpected concurrent enrollment error: %v", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d successful enrollments; want exactly one", winners)
	}
	stored, err := db.GetAgent(ctx, "shared-agent")
	if err != nil || stored.Token != winner.Token || stored.TenantID != winner.TenantID {
		t.Fatalf("concurrent enrollment replaced winner: %+v, %v", stored, err)
	}
}
