package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wscommon "printmaster/common/ws"
	"printmaster/server/storage"

	"github.com/gorilla/websocket"
)

func TestServerInitiatedDeviceDeleteSuppressesAgentNotification(t *testing.T) {
	for _, deleteMetrics := range []bool{false, true} {
		t.Run(map[bool]string{false: "retain-metrics", true: "delete-metrics"}[deleteMetrics], func(t *testing.T) {
			f := newBoundaryFixture(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handleAgentWebSocket(w, r, f.store)
			}))
			defer srv.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"?token=boundary-machine-a", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// A heartbeat round-trip ensures the authenticated connection is registered.
			if err := conn.WriteJSON(wscommon.Message{Type: wscommon.MessageTypeHeartbeat}); err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var msg wscommon.Message
			if err := conn.ReadJSON(&msg); err != nil || msg.Type != wscommon.MessageTypePong {
				t.Fatalf("heartbeat: %+v, %v", msg, err)
			}
			body, _ := json.Marshal(map[string]interface{}{
				"serial": "boundary-device-a", "delete_from_agent": true, "delete_metrics": deleteMetrics,
			})
			r := InjectTestUser(httptest.NewRequest(http.MethodPost, "/api/v1/devices/delete", strings.NewReader(string(body))), f.users["operator"])
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				handleDeviceDelete(w, r)
			}()
			// Keep fixture globals alive even if the wire assertions fail.
			defer func() { <-done }()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := conn.ReadJSON(&msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != wscommon.MessageTypeProxyRequest || msg.Data["url"] != "http://localhost:8080/devices/delete" {
				t.Fatalf("unexpected proxy request: %+v", msg)
			}
			headers, _ := msg.Data["headers"].(map[string]interface{})
			if headers["X-PrintMaster-Server-Request"] != "true" {
				// Simulate the Agent's existing behavior when the marker is missing:
				// it syncs deletion before returning the proxy response.
				if err := conn.WriteJSON(wscommon.Message{Type: wscommon.MessageTypeDeviceDeleted, Data: map[string]interface{}{"serial": "boundary-device-a"}}); err != nil {
					t.Fatal(err)
				}
				t.Error("trusted deletion marker missing; Agent would send a duplicate deletion")
			}
			payload, err := base64.StdEncoding.DecodeString(msg.Data["body"].(string))
			if err != nil || !strings.Contains(string(payload), "boundary-device-a") {
				t.Fatalf("delete body: %s, %v", payload, err)
			}
			if err := conn.WriteJSON(wscommon.Message{Type: wscommon.MessageTypeProxyResponse, Data: map[string]interface{}{
				"request_id": msg.Data["request_id"], "status_code": 200, "body": "",
			}}); err != nil {
				t.Fatal(err)
			}
			<-done
			if w.Code != http.StatusOK {
				t.Fatalf("delete returned %d: %s", w.Code, w.Body.String())
			}
			var response struct {
				DeletedFromAgent bool `json:"deleted_from_agent"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.DeletedFromAgent {
				t.Fatalf("agent deletion response: %s, %v", w.Body.String(), err)
			}
			if _, err := f.store.GetDevice(context.Background(), "boundary-device-a"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("server device still exists: %v", err)
			}
		})
	}
}

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
