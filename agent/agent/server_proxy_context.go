package agent

import (
	"context"
	"net/http"
	"strings"
)

// ServerProxyIdentity describes a request that arrived over the agent's
// authenticated WebSocket connection to the PrintMaster server and was
// dispatched in-process to the local HTTP handler.
type ServerProxyIdentity struct {
	User            string
	Role            string
	ServerInitiated bool
}

// IsAdmin reports whether the server-authenticated user is an administrator.
func (id ServerProxyIdentity) IsAdmin() bool {
	return strings.EqualFold(strings.TrimSpace(id.Role), "admin")
}

type serverProxyContextKey struct{}

// WithServerProxy marks ctx as originating from the trusted server proxy path.
// Context values cannot be set by network clients, so unlike request headers
// this marker cannot be spoofed by anyone who can reach the agent's port.
func WithServerProxy(ctx context.Context, id ServerProxyIdentity) context.Context {
	return context.WithValue(ctx, serverProxyContextKey{}, id)
}

// ServerProxyFromContext returns the server proxy identity if the request was
// dispatched by the WebSocket proxy.
func ServerProxyFromContext(ctx context.Context) (ServerProxyIdentity, bool) {
	if ctx == nil {
		return ServerProxyIdentity{}, false
	}
	id, ok := ctx.Value(serverProxyContextKey{}).(ServerProxyIdentity)
	return id, ok
}

// IsServerProxyRequest reports whether r was dispatched by the WebSocket proxy.
func IsServerProxyRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	_, ok := ServerProxyFromContext(r.Context())
	return ok
}

// Headers the server uses to describe proxied requests. They are only
// meaningful on requests carrying the server proxy context marker.
var serverProxyHeaders = []string{
	"X-PrintMaster-Proxy",
	"X-PrintMaster-User",
	"X-PrintMaster-Role",
	"X-PrintMaster-Server-Request",
}

// StripUntrustedProxyHeaders removes server proxy headers from requests that
// did not come through the WebSocket proxy, so no handler can be tricked by a
// LAN client forging them.
func StripUntrustedProxyHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsServerProxyRequest(r) {
			for _, h := range serverProxyHeaders {
				r.Header.Del(h)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// newServerProxyRequest attaches the trusted server proxy identity, derived
// from headers supplied by the server over the authenticated WebSocket.
func newServerProxyRequest(req *http.Request) *http.Request {
	id := ServerProxyIdentity{
		User:            req.Header.Get("X-PrintMaster-User"),
		Role:            req.Header.Get("X-PrintMaster-Role"),
		ServerInitiated: req.Header.Get("X-PrintMaster-Server-Request") != "",
	}
	// Kept for handlers/UI code that still inspect the header; trust decisions
	// must use ServerProxyFromContext.
	req.Header.Set("X-PrintMaster-Proxy", "server")
	return req.WithContext(WithServerProxy(req.Context(), id))
}
