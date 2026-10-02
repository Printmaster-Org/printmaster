package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wscommon "printmaster/common/ws"
	"printmaster/server/storage"
)

type reviewEventStore struct {
	storage.Store
	agentErr  error
	deviceErr error
}

func (s reviewEventStore) GetAgent(ctx context.Context, id string) (*storage.Agent, error) {
	if s.agentErr != nil {
		return nil, s.agentErr
	}
	return s.Store.GetAgent(ctx, id)
}

func (s reviewEventStore) GetDevice(context.Context, string) (*storage.Device, error) {
	return nil, s.deviceErr
}

func TestSecurityReviewDeletionEventsFailClosed(t *testing.T) {
	f := newBoundaryFixture(t)
	p := newPrincipal(f.users["operator"])
	for _, tc := range []struct {
		name      string
		agentErr  error
		deviceErr error
		event     SSEEvent
		visible   bool
	}{
		{"agent-db-error", errors.New("database unavailable"), nil, SSEEvent{Type: "agent_deleted", TenantID: "boundary-a", Data: map[string]interface{}{"agent_id": "gone"}}, false},
		{"agent-confirmed-deleted", sql.ErrNoRows, nil, SSEEvent{Type: "agent_deleted", TenantID: "boundary-a", Data: map[string]interface{}{"agent_id": "gone"}}, true},
		{"device-db-error", nil, errors.New("database unavailable"), SSEEvent{Type: "device_deleted", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "serial": "gone"}}, false},
		{"device-unknown-result", nil, nil, SSEEvent{Type: "device_deleted", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "serial": "gone"}}, false},
		{"device-confirmed-deleted", nil, sql.ErrNoRows, SSEEvent{Type: "device_deleted", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "serial": "gone"}}, true},
		{"device-missing-serial", nil, nil, SSEEvent{Type: "device_updated", Data: map[string]interface{}{"agent_id": "boundary-agent-a"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverStore = reviewEventStore{Store: f.store, agentErr: tc.agentErr, deviceErr: tc.deviceErr}
			if got := eventVisibleToPrincipal(context.Background(), p, tc.event); got != tc.visible {
				t.Fatalf("visible=%v, want %v", got, tc.visible)
			}
		})
	}
	serverStore = f.store
	if err := f.store.DeleteDevice(context.Background(), "boundary-device-a", false); err != nil {
		t.Fatal(err)
	}
	if !eventVisibleToPrincipal(context.Background(), p, SSEEvent{Type: "device_deleted", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "serial": "boundary-device-a"}}) {
		t.Fatal("real deleted-device event rejected")
	}
}

func TestSecurityReviewProgressWithoutData(t *testing.T) {
	f := newBoundaryFixture(t)
	agent, err := f.store.GetAgent(context.Background(), "boundary-agent-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, handler := range []func(*storage.Agent, wscommon.Message){handleWSUpdateProgress, handleWSJobProgress} {
		handler(agent, wscommon.Message{})
	}
}

type reviewDeleteRaceStore struct {
	storage.Store
	replace func()
}

func (s *reviewDeleteRaceStore) GetDevice(ctx context.Context, serial string) (*storage.Device, error) {
	d, err := s.Store.GetDevice(ctx, serial)
	if s.replace != nil {
		replace := s.replace
		s.replace = nil
		replace()
	}
	return d, err
}

func (s *reviewDeleteRaceStore) DeleteDeviceForAgentWithMetrics(ctx context.Context, serial, agentID string, deleteMetrics bool) error {
	return s.Store.(*storage.SQLiteStore).DeleteDeviceForAgentWithMetrics(ctx, serial, agentID, deleteMetrics)
}

func TestSecurityReviewHTTPDeletionRechecksOwner(t *testing.T) {
	for _, deleteMetrics := range []bool{false, true} {
		t.Run(map[bool]string{false: "retain-metrics", true: "delete-metrics"}[deleteMetrics], func(t *testing.T) {
			f := newBoundaryFixture(t)
			ctx := context.Background()
			serverStore = &reviewDeleteRaceStore{Store: f.store, replace: func() {
				if err := f.store.DeleteDevice(ctx, "boundary-device-a", true); err != nil {
					t.Fatal(err)
				}
				d := &storage.Device{AgentID: "boundary-agent-b"}
				d.Serial = "boundary-device-a"
				if err := f.store.UpsertDevice(ctx, d); err != nil {
					t.Fatal(err)
				}
				if err := f.store.SaveMetrics(ctx, &storage.MetricsSnapshot{Serial: d.Serial, AgentID: d.AgentID, PageCount: 123}); err != nil {
					t.Fatal(err)
				}
				if err := f.store.UpsertDeviceCredentials(ctx, &storage.DeviceCredentials{Serial: d.Serial, TenantID: "boundary-b", Username: "new-owner"}); err != nil {
					t.Fatal(err)
				}
			}}
			body := `{"serial":"boundary-device-a","delete_metrics":false}`
			if deleteMetrics {
				body = strings.Replace(body, "false", "true", 1)
			}
			w := httptest.NewRecorder()
			handleDeviceDelete(w, InjectTestUser(httptest.NewRequest(http.MethodPost, "/api/v1/devices/delete", strings.NewReader(body)), f.users["operator"]))
			if w.Code != http.StatusConflict {
				t.Fatalf("ownership change returned %d, want 409: %s", w.Code, w.Body.String())
			}
			d, err := f.store.GetDevice(ctx, "boundary-device-a")
			if err != nil || d == nil || d.AgentID != "boundary-agent-b" {
				t.Fatalf("foreign replacement deleted: %+v, %v", d, err)
			}
			creds, err := f.store.GetDeviceCredentials(ctx, d.Serial)
			if err != nil || creds == nil || creds.Username != "new-owner" {
				t.Fatalf("foreign credentials deleted: %+v, %v", creds, err)
			}
			metrics, err := f.store.GetLatestMetrics(ctx, d.Serial)
			if err != nil || metrics == nil || metrics.PageCount != 123 {
				t.Fatalf("foreign metrics deleted: %+v, %v", metrics, err)
			}
		})
	}
}
