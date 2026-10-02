package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// ApprovePendingRegistrationWithToken keeps review state and its credential in
// one transaction. Any generation/insert/commit failure leaves review retryable.
func (s *BaseStore) ApprovePendingRegistrationWithToken(ctx context.Context, id int64, tenantID, reviewer string) (*JoinToken, string, error) {
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, "", err
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	raw := hex.EncodeToString(b)
	hash, err := hashArgon(raw)
	if err != nil {
		return nil, "", err
	}
	tokenID, err := generateSecureToken(16)
	if err != nil {
		return nil, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.query(`UPDATE pending_agent_registrations
		SET status = ?, reviewed_at = ?, reviewed_by = ? WHERE id = ? AND status = ?`),
		PendingStatusApproved, now, reviewer, id, PendingStatusPending)
	if err != nil {
		return nil, "", err
	}
	if err := requirePendingTransition(result, id); err != nil {
		return nil, "", err
	}
	jt := &JoinToken{ID: tokenID, TenantID: tenantID, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), OneTime: true}
	_, err = tx.ExecContext(ctx, s.query(`INSERT INTO join_tokens
		(id, token_hash, tenant_id, expires_at, one_time, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
		jt.ID, hash, jt.TenantID, jt.ExpiresAt, boolToInt(jt.OneTime), jt.CreatedAt)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return jt, raw, nil
}
