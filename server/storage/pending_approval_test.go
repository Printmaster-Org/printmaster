package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestPendingApprovalTokenAtomic(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "approval.db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateTenant(ctx, &Tenant{ID: "tenant", Name: "Tenant"}); err != nil { t.Fatal(err) }
	id, err := s.CreatePendingAgentRegistration(ctx, &PendingAgentRegistration{AgentID: "machine", ExpiredTokenID: "expired", ExpiredTenantID: "tenant"})
	if err != nil { t.Fatal(err) }
	_, err = s.DB().Exec(`CREATE TRIGGER fail_approval_token BEFORE INSERT ON join_tokens BEGIN SELECT RAISE(ABORT, 'injected token failure'); END`)
	if err != nil { t.Fatal(err) }
	if _, raw, err := s.ApprovePendingRegistrationWithToken(ctx, id, "tenant", "operator"); err == nil || raw != "" { t.Fatal("injected issuance failure accepted") }
	reg, err := s.GetPendingAgentRegistration(ctx, id)
	if err != nil || reg.Status != PendingStatusPending { t.Fatalf("failure stranded review: %+v %v", reg, err) }
	if _, err := s.DB().Exec(`DROP TRIGGER fail_approval_token`); err != nil { t.Fatal(err) }
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ { wg.Add(1); go func() { defer wg.Done(); _, _, err := s.ApprovePendingRegistrationWithToken(ctx, id, "tenant", "operator"); results <- err }() }
	wg.Wait()
	close(results)
	winners := 0
	for err := range results { if err == nil { winners++ } else if !errors.Is(err, sql.ErrNoRows) { t.Fatal(err) } }
	tokens, err := s.ListJoinTokens(ctx, "tenant")
	if err != nil || winners != 1 || len(tokens) != 1 { t.Fatalf("winners=%d tokens=%d err=%v", winners, len(tokens), err) }
}