package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"printmaster/agent/agent"
)

// Network clients must not be able to bypass agent auth or impersonate a
// server user by forging the headers the server proxy uses.
func TestForgedServerProxyHeadersAreRejected(t *testing.T) {
	auth := securityAuthManager("https://server.invalid", "machine-a")

	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := agent.StripUntrustedProxyHeaders(auth.Wrap(protected))

	forged := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	forged.RemoteAddr = "192.0.2.10:5555"
	forged.Header.Set("X-PrintMaster-Proxy", "server")
	forged.Header.Set("X-PrintMaster-User", "mallory")
	forged.Header.Set("X-PrintMaster-Role", "admin")
	forged.Header.Set("X-PrintMaster-Server-Request", "true")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, forged)
	if rec.Code == http.StatusOK {
		t.Fatalf("forged proxy headers bypassed auth (status %d)", rec.Code)
	}

	// Without stripping (e.g. a handler reached another way), auth itself must
	// still ignore the header.
	rec = httptest.NewRecorder()
	auth.Wrap(protected).ServeHTTP(rec, forged)
	if rec.Code == http.StatusOK {
		t.Fatalf("auth trusted X-PrintMaster-Proxy header (status %d)", rec.Code)
	}

	// /api/v1/auth/me must not echo a forged identity.
	me := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	me.RemoteAddr = "192.0.2.10:5555"
	me.Header.Set("X-PrintMaster-Proxy", "server")
	me.Header.Set("X-PrintMaster-User", "mallory")
	me.Header.Set("X-PrintMaster-Role", "admin")
	rec = httptest.NewRecorder()
	auth.handleAuthMe(rec, me)
	if rec.Code == http.StatusOK {
		t.Fatalf("auth/me accepted forged identity: %s", rec.Body.String())
	}
}

func TestServerProxyContextBypassesAuth(t *testing.T) {
	auth := securityAuthManager("https://server.invalid", "machine-a")

	var seen http.Header
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	})
	handler := agent.StripUntrustedProxyHeaders(auth.Wrap(protected))

	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.Header.Set("X-PrintMaster-Proxy", "server")
	req.Header.Set("X-PrintMaster-Role", "operator")
	req = req.WithContext(agent.WithServerProxy(req.Context(), agent.ServerProxyIdentity{User: "alice", Role: "operator"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("server proxy request rejected: %d", rec.Code)
	}
	if seen.Get("X-PrintMaster-Role") != "operator" {
		t.Fatalf("trusted proxy headers were stripped: %v", seen)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	me = me.WithContext(agent.WithServerProxy(me.Context(), agent.ServerProxyIdentity{User: "alice", Role: "operator"}))
	rec = httptest.NewRecorder()
	auth.handleAuthMe(rec, me)
	var principal AgentPrincipal
	if err := json.Unmarshal(rec.Body.Bytes(), &principal); err != nil || principal.Username != "alice" || principal.Source != "server-proxy" {
		t.Fatalf("auth/me = %d %s", rec.Code, rec.Body.String())
	}
}

// Forwarding headers are client-controlled and must never grant the
// loopback local-admin bypass to a remote peer.
func TestRequestIsLoopbackIgnoresForgedForwardingHeaders(t *testing.T) {
	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		want    bool
	}{
		{"direct loopback", "127.0.0.1:5000", nil, true},
		{"direct ipv6 loopback", "[::1]:5000", nil, true},
		{"remote peer", "192.168.1.50:5000", nil, false},
		{"remote forged xff", "192.168.1.50:5000", map[string]string{"X-Forwarded-For": "127.0.0.1"}, false},
		{"remote forged real ip", "192.168.1.50:5000", map[string]string{"X-Real-IP": "127.0.0.1"}, false},
		{"local proxy for remote client", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, false},
		{"local proxy spoofed chain", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "127.0.0.1, 203.0.113.9"}, false},
		{"local proxy real ip remote", "127.0.0.1:5000", map[string]string{"X-Real-IP": "203.0.113.9"}, false},
		{"local proxy forwarded header", "127.0.0.1:5000", map[string]string{"Forwarded": "for=203.0.113.9"}, false},
		{"local proxy for local client", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "127.0.0.1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
			req.RemoteAddr = tc.remote
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := requestIsLoopback(req); got != tc.want {
				t.Fatalf("requestIsLoopback = %v, want %v", got, tc.want)
			}
		})
	}
}
