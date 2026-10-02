package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"printmaster/server/storage"
)

func TestDeviceAuthSecurity(t *testing.T) {
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	for _, id := range []string{"tenant-a", "tenant-b"} {
		if err := store.CreateTenant(context.Background(), &storage.Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"get", "reject", "approve"} {
		for _, assigned := range []string{"", "tenant-a", "tenant-b"} {
			for _, fixture := range []struct {
				name string
				user *storage.User
				want int
			}{
				{"anonymous", nil, http.StatusUnauthorized},
				{"viewer", NewTestUser(storage.RoleViewer, "tenant-a"), http.StatusForbidden},
				{"unscoped", NewTestUser(storage.RoleOperator), http.StatusForbidden},
				{"operator", NewTestUser(storage.RoleOperator, "tenant-a"), http.StatusOK},
				{"admin", NewTestAdminUser(), http.StatusOK},
			} {
				t.Run(action+"/"+assigned+"/"+fixture.name, func(t *testing.T) {
					requests := newDeviceAuthStore()
					pending := requests.Create(deviceAuthMetadata{AgentID: "machine", Hostname: "secret-host"})
					requests.byCode[pending.Code].TenantID = assigned
					method, body := http.MethodGet, ""
					if action != "get" {
						method, body = http.MethodPost, `{"reason":"test","tenant_id":"tenant-a"}`
					}
					req := httptest.NewRequest(method, "/api/v1/device-auth/requests/"+pending.Code, strings.NewReader(body))
					if fixture.user != nil {
						req = InjectTestUser(req, fixture.user)
					}
					res := httptest.NewRecorder()
					switch action {
					case "get":
						handleDeviceAuthRequestGet(res, req, requests, pending.Code)
					case "reject":
						handleDeviceAuthReject(res, req, requests, pending.Code)
					case "approve":
						handleDeviceAuthApprove(res, req, requests, pending.Code)
					}
					want := fixture.want
					if assigned == "tenant-b" && fixture.name == "operator" {
						want = http.StatusForbidden
					}
					if assigned == "tenant-b" && fixture.name == "admin" && action == "approve" {
						want = http.StatusBadRequest // Cannot reassign a pending request.
					}
					if res.Code != want {
						t.Fatalf("status=%d want=%d: %s", res.Code, want, res.Body.String())
					}
					pending, _ = requests.snapshot(pending.Code)
					if want != http.StatusOK {
						if strings.Contains(res.Body.String(), "secret-host") || pending.Status != deviceAuthStatusPending || pending.JoinToken != "" {
							t.Fatal("denied request disclosed metadata or mutated enrollment")
						}
					}
				})
			}
		}
	}
}

func TestDeviceAuthRejectSnapshotRace(t *testing.T) {
	requests := newDeviceAuthStore()
	pending := requests.Create(deviceAuthMetadata{AgentID: "machine"})
	// Simulate assignment after authorization of an unassigned snapshot.
	pending = requests.byCode[pending.Code] // Test-only setup, before concurrent access.
	pending.TenantID = "tenant-b"
	if _, err := requests.reject(pending.Code, "rejected", "operator-a", ""); err == nil {
		t.Fatal("stale authorization rejected newly assigned request")
	}
	pending.Status = deviceAuthStatusApproved
	pending.JoinToken = "original"
	if _, err := requests.reject(pending.Code, "rejected", "admin", "tenant-b"); err == nil || pending.Status != deviceAuthStatusApproved || pending.JoinToken != "original" {
		t.Fatal("rejection overwrote approved request")
	}
}

func TestDeviceAuthAssignedTerminalSecurity(t *testing.T) {
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	ctx := context.Background()
	if err := store.CreateTenant(ctx, &storage.Tenant{ID: "tenant-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	user := &storage.User{Username: "operator-a", Role: storage.RoleOperator, TenantIDs: []string{"tenant-a"}}
	if err := store.CreateUser(ctx, user, "security-test-password"); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, user.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []deviceAuthStatus{deviceAuthStatusApproved, deviceAuthStatusRejected} {
		requests := newDeviceAuthStore()
		pending := requests.Create(deviceAuthMetadata{AgentID: "foreign-machine", Hostname: "secret-host"})
		pending = requests.byCode[pending.Code] // Test-only setup.
		pending.TenantID, pending.Status, pending.JoinToken = "tenant-b", status, "secret-token"
		for _, action := range []string{"get", "reject"} {
			for _, token := range []string{"", "invalid-session", session.Token} {
				method := http.MethodGet
				if action == "reject" {
					method = http.MethodPost
				}
				req := httptest.NewRequest(method, "/api/v1/device-auth/requests/"+pending.Code, strings.NewReader(`{}`))
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				res := httptest.NewRecorder()
				requireWebAuth(func(w http.ResponseWriter, r *http.Request) {
					if action == "get" {
						handleDeviceAuthRequestGet(w, r, requests, pending.Code)
					} else {
						handleDeviceAuthReject(w, r, requests, pending.Code)
					}
				})(res, req)
				want := http.StatusUnauthorized
				if token == session.Token {
					want = http.StatusForbidden
				}
				if res.Code != want || strings.Contains(res.Body.String(), "secret-") || pending.Status != status || pending.JoinToken != "secret-token" {
					t.Fatalf("assigned terminal request crossed auth boundary: status=%d body=%s request=%+v", res.Code, res.Body.String(), pending)
				}
			}
		}
	}
}
