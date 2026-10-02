//go:build integration

package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestPostgresPendingApprovalTokenAtomic(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, s *PostgresStore) {
		ctx := context.Background()
		if err := s.CreateTenant(ctx, &Tenant{ID: "approval-tenant", Name: "Approval"}); err != nil { t.Fatal(err) }
		id, err := s.CreatePendingAgentRegistration(ctx, &PendingAgentRegistration{AgentID: "approval-machine", ExpiredTokenID: "expired", ExpiredTenantID: "approval-tenant"})
		if err != nil { t.Fatal(err) }
		for _, statement := range []string{
			`CREATE FUNCTION fail_approval_token() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected approval token failure'; END $$`,
			`CREATE TRIGGER fail_approval_token BEFORE INSERT ON join_tokens FOR EACH ROW EXECUTE FUNCTION fail_approval_token()`,
		} { if _, err := s.DB().ExecContext(ctx, statement); err != nil { t.Fatal(err) } }
		if _, raw, err := s.ApprovePendingRegistrationWithToken(ctx, id, "approval-tenant", "reviewer"); err == nil || raw != "" { t.Fatal("issuance failure accepted") }
		reg, err := s.GetPendingAgentRegistration(ctx, id)
		if err != nil || reg.Status != PendingStatusPending { t.Fatalf("review not rolled back: %+v %v", reg, err) }
		if _, err := s.DB().ExecContext(ctx, `DROP TRIGGER fail_approval_token ON join_tokens`); err != nil { t.Fatal(err) }
		jt, raw, err := s.ApprovePendingRegistrationWithToken(ctx, id, "approval-tenant", "reviewer")
		if err != nil || jt == nil || raw == "" { t.Fatalf("retry failed: %+v %v", jt, err) }
		if _, _, err := s.ApprovePendingRegistrationWithToken(ctx, id, "approval-tenant", "loser"); !errors.Is(err, sql.ErrNoRows) { t.Fatalf("repeat review: %v", err) }
		tokens, err := s.ListJoinTokens(ctx, "approval-tenant")
		if err != nil || len(tokens) != 1 { t.Fatalf("loser issued token: %+v %v", tokens, err) }
	})
}