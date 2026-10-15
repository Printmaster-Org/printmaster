package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestDeviceMetadataEditBoundaries(t *testing.T) {
	_, store := setupTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"meta-owner", "meta-other"} {
		if err := store.RegisterAgent(ctx, &storage.Agent{AgentID: id, Token: id, RegisteredAt: now, LastSeen: now}); err != nil {
			t.Fatal(err)
		}
	}
	device := &storage.Device{AgentID: "meta-owner"}
	device.Serial, device.IP, device.Location, device.LastSeen = "META-HTTP", "192.0.2.1", "Reported", now
	if err := store.UpsertDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	run := func(handler http.HandlerFunc, body, token string, user *storage.User) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/devices/update", strings.NewReader(body))
		if user != nil {
			r = InjectTestUser(r, user)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Code
	}
	admin := NewTestUser(storage.RoleAdmin)
	if code := run(handleDeviceUpdateProxy, `{"serial":"META-HTTP","location":"Server pin"}`, "", admin); code != http.StatusOK {
		t.Fatalf("Server edit needs offline Agent: %d", code)
	}
	for _, tc := range []struct {
		body, token string
		want        int
	}{
		{`{"serial":"META-HTTP","location":"Bypass"}`, "meta-owner", http.StatusConflict},
		{`{"serial":"META-HTTP","model":"Manual"}`, "meta-other", http.StatusForbidden},
		{`{"serial":"META-HTTP","model":"Manual"}`, "", http.StatusUnauthorized},
		{`{"serial":"META-HTTP","model":"Manual"}`, "meta-owner", http.StatusOK},
		{`{"serial":"META-HTTP","dns_servers":"bad"}`, "meta-owner", http.StatusBadRequest},
	} {
		if code := run(requireAuth(handleDeviceMetadata), tc.body, tc.token, nil); code != tc.want {
			t.Errorf("machine edit %s / %s = %d, want %d", tc.body, tc.token, code, tc.want)
		}
	}
	if code := run(handleDeviceMetadataUnpin, `{"serial":"META-HTTP","field":"location"}`, "", NewTestUser(storage.RoleViewer)); code != http.StatusForbidden {
		t.Fatalf("viewer unpin = %d", code)
	}
	if code := run(handleDeviceMetadataUnpin, `{"serial":"META-HTTP","field":"location"}`, "", admin); code != http.StatusOK {
		t.Fatalf("unpin = %d", code)
	}
	got, err := store.GetDevice(ctx, device.Serial)
	if err != nil || got.Location != "Reported" || got.Model != "Manual" || !got.LastSeen.Equal(now) {
		t.Fatalf("incorrect effective metadata: %+v, %v", got, err)
	}
}
