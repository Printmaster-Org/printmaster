package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wscommon "printmaster/common/ws"
	"printmaster/server/storage"

	"github.com/gorilla/websocket"
)

func TestWSProxyTenantSenderBinding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		typeID  string
		handler func(string, wscommon.Message)
		removed bool
	}{
		{"response", wscommon.MessageTypeProxyResponse, handleWSProxyResponse, true},
		{"chunk", wscommon.MessageTypeProxyStreamChunk, handleWSProxyStreamChunk, false},
		{"end", wscommon.MessageTypeProxyStreamEnd, handleWSProxyStreamEnd, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The prefix lookalike catches naive HasPrefix(agentID) checks.
			owner := "ws-binding-owner-123"
			requestID := fmt.Sprintf("%s-%d", owner, time.Now().UnixNano())
			ch := make(chan wscommon.Message, 4)
			proxyRequestsLock.Lock()
			proxyRequests[requestID] = ch
			proxyRequestsLock.Unlock()
			defer func() {
				proxyRequestsLock.Lock()
				delete(proxyRequests, requestID)
				proxyRequestsLock.Unlock()
			}()
			msg := wscommon.Message{Type: tc.typeID, Data: map[string]interface{}{"request_id": requestID, "agent_id": owner}}
			for _, sender := range []string{"foreign-tenant-agent", "ws-binding-owner", "", owner + "-extra"} {
				tc.handler(sender, msg)
				if len(ch) != 0 {
					t.Fatalf("foreign sender %q delivered a message", sender)
				}
				proxyRequestsLock.RLock()
				pending := proxyRequests[requestID] == ch
				proxyRequestsLock.RUnlock()
				if !pending {
					t.Fatal("foreign sender consumed pending request")
				}
			}
			tc.handler(owner, msg)
			select {
			case got := <-ch:
				if got.Type != tc.typeID || got.Data["request_id"] != requestID {
					t.Fatalf("unexpected response: %+v", got)
				}
			default:
				t.Fatal("expected sender was rejected")
			}
			proxyRequestsLock.RLock()
			_, pending := proxyRequests[requestID]
			proxyRequestsLock.RUnlock()
			if pending == tc.removed {
				t.Fatalf("pending=%v removed=%v", pending, tc.removed)
			}
		})
	}
}

func TestWSProxyIdentityKeyValidation(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"agent", "agent-", "agent-not-a-time", "agent-0", "other-123", "agent-other-123"} {
		if proxyRequestOwnedBy(id, "agent") {
			t.Errorf("accepted malformed/foreign key %q", id)
		}
	}
	if !proxyRequestOwnedBy("agent-with-hyphens-123", "agent-with-hyphens") {
		t.Fatal("hyphenated owner rejected")
	}
	// No pending lookup => no delivery, even for a correctly shaped key.
	handleWSProxyResponse("agent", wscommon.Message{Data: map[string]interface{}{"request_id": "agent-123"}})
	handleWSProxyStreamChunk("agent", wscommon.Message{})
	handleWSProxyStreamEnd("agent", wscommon.Message{})
}

func TestWSDeviceDeletionTenantOwnership(t *testing.T) {
	t.Parallel()
	s, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "ws-ownership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, tenant := range []string{"ws-tenant-a", "ws-tenant-b"} {
		if err := s.CreateTenant(ctx, &storage.Tenant{ID: tenant, Name: tenant}); err != nil {
			t.Fatal(err)
		}
	}
	owner := &storage.Agent{AgentID: "ws-device-owner", Token: "ws-device-owner-token", TenantID: "ws-tenant-a"}
	sameTenant := &storage.Agent{AgentID: "ws-device-peer", Token: "ws-device-peer-token", TenantID: owner.TenantID}
	foreign := &storage.Agent{AgentID: "ws-device-foreign", Token: "ws-device-foreign-token", TenantID: "ws-tenant-b"}
	for _, a := range []*storage.Agent{owner, sameTenant, foreign} {
		if err := s.RegisterAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	d := &storage.Device{AgentID: owner.AgentID}
	d.Serial = "ws-owned-serial"
	if err := s.UpsertDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	creds := &storage.DeviceCredentials{Serial: d.Serial, Username: "owner", EncryptedPassword: "encrypted", TenantID: owner.TenantID}
	if err := s.UpsertDeviceCredentials(ctx, creds); err != nil {
		t.Fatal(err)
	}
	msg := wscommon.Message{Type: wscommon.MessageTypeDeviceDeleted, Data: map[string]interface{}{"serial": d.Serial, "agent_id": owner.AgentID}}
	for _, sender := range []*storage.Agent{foreign, sameTenant, nil, {}} {
		handleWSDeviceDeleted(sender, msg, s)
		got, err := s.GetDevice(ctx, d.Serial)
		if err != nil || got.AgentID != owner.AgentID {
			t.Fatalf("foreign deletion changed device: %+v, %v", got, err)
		}
		gotCreds, err := s.GetDeviceCredentials(ctx, d.Serial)
		if err != nil || gotCreds.EncryptedPassword != creds.EncryptedPassword {
			t.Fatalf("foreign deletion changed credentials: %+v, %v", gotCreds, err)
		}
	}
	handleWSDeviceDeleted(owner, wscommon.Message{Data: map[string]interface{}{"serial": "missing"}}, s)
	handleWSDeviceDeleted(owner, wscommon.Message{}, s)
	handleWSDeviceDeleted(owner, msg, s)
	if got, err := s.GetDevice(ctx, d.Serial); err == nil || got != nil {
		t.Fatalf("owner could not delete device: %+v, %v", got, err)
	}
	if got, err := s.GetDeviceCredentials(ctx, d.Serial); err == nil || got != nil {
		t.Fatalf("owned deletion did not remove credentials: %+v, %v", got, err)
	}
}

// Exercise dispatch, not just helpers: claimed payload identity cannot override
// the token-authenticated WebSocket connection identity.
func TestWSAuthenticatedProxyTenantBinding(t *testing.T) {
	t.Parallel()
	s, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "ws-auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	owner := &storage.Agent{AgentID: "ws-auth-owner", Token: "ws-auth-owner-token", TenantID: "ws-auth-tenant-a"}
	foreign := &storage.Agent{AgentID: "ws-auth-foreign", Token: "ws-auth-foreign-token", TenantID: "ws-auth-tenant-b"}
	for _, a := range []*storage.Agent{owner, foreign} {
		if err := s.CreateTenant(ctx, &storage.Tenant{ID: a.TenantID, Name: a.TenantID}); err != nil {
			t.Fatal(err)
		}
		if err := s.RegisterAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handleAgentWebSocket(w, r, s) }))
	defer srv.Close()
	dial := func(a *storage.Agent) *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"?token="+a.Token, nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	ownerConn, foreignConn := dial(owner), dial(foreign)
	defer ownerConn.Close()
	defer foreignConn.Close()
	barrier := func(conn *websocket.Conn) {
		if err := conn.WriteJSON(wscommon.Message{Type: wscommon.MessageTypeHeartbeat}); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var msg wscommon.Message
		if err := conn.ReadJSON(&msg); err != nil || msg.Type != wscommon.MessageTypePong {
			t.Fatalf("heartbeat barrier: %+v, %v", msg, err)
		}
	}
	for _, kind := range []string{wscommon.MessageTypeProxyResponse, wscommon.MessageTypeProxyStreamChunk, wscommon.MessageTypeProxyStreamEnd} {
		requestID := fmt.Sprintf("%s-%d", owner.AgentID, time.Now().UnixNano())
		ch := make(chan wscommon.Message, 2)
		proxyRequestsLock.Lock()
		proxyRequests[requestID] = ch
		proxyRequestsLock.Unlock()
		msg := wscommon.Message{Type: kind, Data: map[string]interface{}{"request_id": requestID, "agent_id": owner.AgentID}}
		if err := foreignConn.WriteJSON(msg); err != nil {
			t.Fatal(err)
		}
		barrier(foreignConn)
		if len(ch) != 0 {
			t.Fatalf("foreign authenticated %s delivered", kind)
		}
		if err := ownerConn.WriteJSON(msg); err != nil {
			t.Fatal(err)
		}
		barrier(ownerConn)
		if len(ch) != 1 {
			t.Fatalf("owner authenticated %s rejected", kind)
		}
		proxyRequestsLock.Lock()
		delete(proxyRequests, requestID)
		proxyRequestsLock.Unlock()
	}
}
