package tenancy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"printmaster/server/storage"
)

type reviewCompetingApprovalStore struct{ storage.Store }

func (s reviewCompetingApprovalStore) ApprovePendingRegistrationWithToken(ctx context.Context, id int64, tenantID, reviewer string) (*storage.JoinToken, string, error) {
	if err := s.Store.RejectPendingRegistration(ctx, id, "winning-reviewer", "rejected concurrently"); err != nil {
		return nil, "", err
	}
	return s.Store.ApprovePendingRegistrationWithToken(ctx, id, tenantID, reviewer)
}

func TestSecurityReviewPendingHandlerLostRace(t *testing.T) {
	db, mux, token := setupEnrollmentSecurity(t, storage.RoleAdmin)
	ctx := context.Background()
	id, err := db.CreatePendingAgentRegistration(ctx, &storage.PendingAgentRegistration{AgentID: "review-agent", ExpiredTokenID: "expired", ExpiredTenantID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	dbStore = reviewCompetingApprovalStore{Store: db}
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/pending-registrations/%d", id), strings.NewReader(`{"action":"approve","tenant_id":"tenant-a"}`))
	r.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("real losing storage transition returned %d: %s", w.Code, w.Body.String())
	}
	tokens, err := db.ListJoinTokens(ctx, "tenant-a")
	if err != nil || len(tokens) != 0 {
		t.Fatalf("losing review issued tokens: %+v, %v", tokens, err)
	}
}
