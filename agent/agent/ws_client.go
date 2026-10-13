package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	wscommon "printmaster/common/ws"
)

// maskTokenForLog returns a copy of the URL string with the token query parameter masked
func maskTokenForLog(u *url.URL) string {
	if u == nil {
		return ""
	}
	q := u.Query()
	if q.Get("token") != "" {
		q.Set("token", "***")
		u2 := *u
		u2.RawQuery = q.Encode()
		return u2.String()
	}
	return u.String()
}

// Use shared message types from wscommon

// WSClient manages a persistent WebSocket connection to the server
type WSClient struct {
	serverURL     string
	token         string
	conn          *wscommon.Conn
	mu            sync.RWMutex
	connected     bool
	reconnectChan chan struct{}
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	// Use package-level structured logger (agent.Info/Debug/Error/Warn)

	// Configuration
	reconnectDelay     time.Duration
	pingInterval       time.Duration
	writeTimeout       time.Duration
	readTimeout        time.Duration
	handshakeTimeout   time.Duration
	maxReconnectDelay  time.Duration
	insecureSkipVerify bool

	// Local handler for direct invocation (avoids localhost HTTP round-trip)
	localHandler      http.Handler
	localHandlerMu    sync.RWMutex
	localHandlerReady chan struct{} // closed when handler is set
}

// NewWSClient creates a new WebSocket client
func NewWSClient(serverURL, token string, insecureSkipVerify bool) *WSClient {
	ctx, cancel := context.WithCancel(context.Background())

	return &WSClient{
		serverURL:         serverURL,
		token:             token,
		reconnectChan:     make(chan struct{}, 1),
		stopChan:          make(chan struct{}),
		ctx:               ctx,
		cancel:            cancel,
		localHandlerReady: make(chan struct{}),
		// No stdlib logger; use agent package logging helpers instead
		reconnectDelay:     5 * time.Second,
		pingInterval:       30 * time.Second,
		writeTimeout:       10 * time.Second,
		readTimeout:        60 * time.Second,
		handshakeTimeout:   10 * time.Second,
		maxReconnectDelay:  5 * time.Minute,
		insecureSkipVerify: insecureSkipVerify,
	}
}

// SetLocalHandler sets the local HTTP handler for direct invocation.
// When proxy requests target localhost:8080, the handler is invoked directly
// instead of making an HTTP round-trip. This avoids SSRF validation issues
// and is more efficient.
func (ws *WSClient) SetLocalHandler(handler http.Handler) {
	ws.localHandlerMu.Lock()
	alreadySet := ws.localHandler != nil
	ws.localHandler = handler
	ws.localHandlerMu.Unlock()

	// Signal that the handler is ready (only close once)
	if !alreadySet && handler != nil {
		close(ws.localHandlerReady)
	}
	InfoCtx("Local handler registered for direct proxy invocation")
}

// waitForLocalHandler blocks until the local handler is registered or context is cancelled.
// Returns the handler if available, nil if cancelled/timed out.
func (ws *WSClient) waitForLocalHandler(timeout time.Duration) http.Handler {
	// Fast path: already set
	if h := ws.getLocalHandler(); h != nil {
		return h
	}

	// Wait for handler to be set
	select {
	case <-ws.localHandlerReady:
		return ws.getLocalHandler()
	case <-ws.ctx.Done():
		return nil
	case <-time.After(timeout):
		WarnCtx("Timeout waiting for local handler", "timeout", timeout)
		return nil
	}
}

// getLocalHandler returns the registered local handler, if any
func (ws *WSClient) getLocalHandler() http.Handler {
	ws.localHandlerMu.RLock()
	defer ws.localHandlerMu.RUnlock()
	return ws.localHandler
}

// Start begins the WebSocket connection and management goroutines
func (ws *WSClient) Start() error {
	InfoCtx("Starting WebSocket client")

	// Initial connection attempt
	if err := ws.connect(); err != nil {
		WarnCtx("Initial WebSocket connection failed (will retry)", "error", err)
		// Don't return error - reconnect loop will handle it
	}

	// Start connection manager
	InfoCtx("Starting connection manager goroutine")
	go ws.connectionManager()

	return nil
}

// Stop gracefully stops the WebSocket client
func (ws *WSClient) Stop() error {
	InfoCtx("Stopping WebSocket client")
	ws.cancel()
	close(ws.stopChan)

	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.conn != nil {
		// Close underlying connection (no explicit close-frame to keep wrapper small)
		if err := ws.conn.Close(); err != nil {
			ErrorCtx("Error closing WS connection", "error", err)
		} else {
			InfoCtx("Closed WebSocket connection object")
		}
		ws.conn = nil

	}

	ws.connected = false
	return nil
}

// IsConnected returns whether the WebSocket is currently connected
func (ws *WSClient) IsConnected() bool {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.connected
}

// connect establishes a WebSocket connection to the server
func (ws *WSClient) connect() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	// Close existing connection if any
	if ws.conn != nil {
		ws.conn.Close()
		ws.conn = nil
		ws.connected = false
		// Give some time for goroutines to observe ctx.Done / stopChan and exit
		// We won't block indefinitely; just log whether they exited within a short window
		done := make(chan struct{})
		go func() {
			// Poll for connection cleared
			for i := 0; i < 10; i++ {
				ws.mu.RLock()
				c := ws.conn
				connState := ws.connected
				ws.mu.RUnlock()
				if c == nil && !connState {
					close(done)
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
			close(done)
		}()

		select {
		case <-done:
			DebugCtx("Stop: connection and goroutines appear to have shut down (or timed check)")
		case <-time.After(6 * time.Second):
			DebugCtx("Stop: timeout waiting for goroutines to exit")
		}
	}

	// Parse and build WebSocket URL
	u, err := url.Parse(ws.serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}

	// Convert http(s) to ws(s)
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return fmt.Errorf("unsupported URL scheme: %s", u.Scheme)
	}

	// Preserve any base path segment present in the configured server URL so
	// deployments mounted under subpaths (e.g. https://host/printmaster)
	// continue to work when switching the scheme to ws/wss.
	basePath := strings.TrimSuffix(u.Path, "/")
	u.Path = basePath + "/api/v1/agents/ws"

	// Add authentication token as query parameter
	q := u.Query()
	q.Set("token", ws.token)
	u.RawQuery = q.Encode()

	// Log the target URL but mask token for privacy in logs
	InfoCtx("Connecting to WebSocket", "url", maskTokenForLog(u))

	// Determine whether to skip TLS verification for the WebSocket dialer.
	// Use the same configured policy passed into the WS client (via ServerClient)
	skipVerify := ws.insecureSkipVerify

	// Connect using shared ws wrapper
	conn, resp, err := wscommon.Dial(u.String(), nil, &tls.Config{InsecureSkipVerify: skipVerify}, ws.handshakeTimeout)
	if err != nil {
		// Try to include HTTP response body and headers from the upgrade attempt for easier debugging
		if resp != nil {
			var bodyBytes []byte
			if resp.Body != nil {
				bodyBytes, _ = io.ReadAll(resp.Body)
			}
			// Log status, a short preview of body, and response headers (if any)
			preview := string(bodyBytes)
			if len(preview) > 1024 {
				preview = preview[:1024] + "..."
			}
			WarnCtx("WebSocket dial failed", "status", resp.StatusCode, "respBodyPreview", preview, "respHeaders", resp.Header, "error", err)
			return fmt.Errorf("WebSocket connection failed (status %d): %w", resp.StatusCode, err)
		}
		WarnCtx("WebSocket dial failed", "error", err)
		return fmt.Errorf("WebSocket connection failed: %w", err)
	}

	ws.conn = conn
	ws.connected = true
	// Log some connection info for debugging
	var respInfo string
	if resp != nil {
		respInfo = resp.Status
	}
	InfoCtx("WebSocket connected successfully", "status", respInfo, "remote", conn.RemoteAddr(), "respHeaders", resp.Header)

	// Start read and ping loops for this connection
	// Ensure pong updates extend our read deadline using wrapper
	conn.SetPongHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(ws.readTimeout))
		DebugCtx("Pong received, extended read deadline")
		return nil
	})

	go ws.readLoop()
	go ws.pingLoop()

	return nil
}

// connectionManager handles reconnection logic
func (ws *WSClient) connectionManager() {
	DebugCtx("connectionManager started")
	currentDelay := ws.reconnectDelay

	for {
		select {
		case <-ws.ctx.Done():
			return
		case <-ws.stopChan:
			return
		case <-ws.reconnectChan:
			DebugCtx("Reconnecting in", "delay", currentDelay)

			timer := time.NewTimer(currentDelay)
			select {
			case <-ws.ctx.Done():
				timer.Stop()
				DebugCtx("connectionManager exiting due to context cancellation")
				return
			case <-ws.stopChan:
				timer.Stop()
				DebugCtx("connectionManager exiting due to stopChan")
				return
			case <-timer.C:
				// Attempt reconnection
				if err := ws.connect(); err != nil {
					WarnCtx("Reconnection failed", "error", err)

					// Exponential backoff (up to max)
					currentDelay *= 2
					if currentDelay > ws.maxReconnectDelay {
						currentDelay = ws.maxReconnectDelay
					}
					DebugCtx("Next reconnect attempt in", "delay", currentDelay)

					// Trigger another reconnection attempt
					select {
					case ws.reconnectChan <- struct{}{}:
					default:
					}
				} else {
					// Reset delay on successful connection
					currentDelay = ws.reconnectDelay
				}
			}
		}
	}
}

// readLoop reads messages from the WebSocket connection
func (ws *WSClient) readLoop() {
	defer func() {
		ws.mu.Lock()
		ws.connected = false
		ws.mu.Unlock()

		// Trigger reconnection
		select {
		case ws.reconnectChan <- struct{}{}:
		default:
		}
		DebugCtx("readLoop exiting")
	}()

	for {
		select {
		case <-ws.ctx.Done():
			return
		case <-ws.stopChan:
			return
		default:
			ws.mu.RLock()
			conn := ws.conn
			ws.mu.RUnlock()

			if conn == nil {
				DebugCtx("readLoop: conn is nil, exiting")
				return
			}

			// Set read deadline
			conn.SetReadDeadline(time.Now().Add(ws.readTimeout))

			message, err := conn.ReadMessage()
			if err != nil {
				if wscommon.IsUnexpectedCloseError(err, wscommon.CloseNormalClosure) {
					WarnCtx("WebSocket read error", "error", err)
				} else {
					DebugCtx("WebSocket read returned error", "error", err)
				}
				return
			}

			// Parse message into shared wscommon.Message
			var msg wscommon.Message
			if err := json.Unmarshal(message, &msg); err != nil {
				DebugCtx("Failed to parse WebSocket message", "error", err)
				continue
			}

			// Always log incoming message type (useful when debugging auth/proxy flows)
			if msg.Type != "" {
				// Try to include agent-visible request_id/url when present
				rid := ""
				if v, ok := msg.Data["request_id"].(string); ok {
					rid = v
				}
				if rid != "" {
					DebugCtx("Received WS message", "type", msg.Type, "request_id", rid)
				} else {
					DebugCtx("Received WS message", "type", msg.Type)
				}
			}

			// Handle message types
			switch msg.Type {
			case wscommon.MessageTypePong:
				// Pong received, connection is healthy
			case wscommon.MessageTypeError:
				WarnCtx("Server error", "data", msg.Data)
			case wscommon.MessageTypeCommand:
				// Handle command from server (e.g., check_update, restart)
				go ws.handleCommand(msg)
			case wscommon.MessageTypeProxyRequest:
				// Handle proxy request from server
				// Log some request details to help trace proxy issues
				if requestID, ok := msg.Data["request_id"].(string); ok {
					if urlStr, ok := msg.Data["url"].(string); ok {
						method := "GET"
						if m, ok := msg.Data["method"].(string); ok && m != "" {
							method = m
						}
						DebugCtx("Incoming proxy_request", "id", requestID, "method", method, "url", urlStr)
					} else {
						DebugCtx("Incoming proxy_request", "id", requestID, "note", "no url provided")
					}
				}
				go ws.handleProxyRequest(msg)
			default:
				DebugCtx("Unknown message type", "type", msg.Type)
			}
		}
	}
}

// pingLoop sends periodic ping messages to keep connection alive
func (ws *WSClient) pingLoop() {
	ticker := time.NewTicker(ws.pingInterval)
	defer ticker.Stop()
	DebugCtx("pingLoop started")
	for {
		select {
		case <-ws.ctx.Done():
			DebugCtx("pingLoop exiting due to context cancellation")
			return
		case <-ws.stopChan:
			DebugCtx("pingLoop exiting due to stopChan")
			return
		case <-ticker.C:
			ws.mu.RLock()
			conn := ws.conn
			connected := ws.connected
			ws.mu.RUnlock()

			if !connected || conn == nil {
				DebugCtx("pingLoop: not connected, exiting")
				return
			}

			// Send ping using wrapper helper
			if err := conn.WritePing(ws.writeTimeout); err != nil {
				WarnCtx("Failed to send ping", "error", err)
				return
			}
			DebugCtx("pingLoop: sent ping")
		}
	}
}

// SendHeartbeat sends a heartbeat message over the WebSocket
func (ws *WSClient) SendHeartbeat(data map[string]interface{}) error {
	ws.mu.RLock()
	conn := ws.conn
	connected := ws.connected
	ws.mu.RUnlock()

	if !connected || conn == nil {
		DebugCtx("SendHeartbeat called but WebSocket not connected")
		return errors.New("WebSocket not connected")
	}

	msg := wscommon.Message{
		Type:      wscommon.MessageTypeHeartbeat,
		Data:      data,
		Timestamp: time.Now(),
	}

	// Helpful debug: include device_count if present
	if v, ok := data["device_count"]; ok {
		DebugCtx("SendHeartbeat via WS", "device_count", v)
	} else {
		DebugCtx("SendHeartbeat via WS")
	}

	// Marshal message
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat: %w", err)
	}

	// Send message using wrapper
	if err := conn.WriteRaw(payload, ws.writeTimeout); err != nil {
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}

	return nil
}

// SendMessage sends an arbitrary message over the WebSocket
func (ws *WSClient) SendMessage(msg wscommon.Message) error {
	ws.mu.RLock()
	conn := ws.conn
	connected := ws.connected
	ws.mu.RUnlock()

	if !connected || conn == nil {
		return errors.New("WebSocket not connected")
	}

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	if err := conn.WriteRaw(payload, ws.writeTimeout); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// handleProxyRequest handles incoming HTTP proxy requests from the server.
// All proxy requests from the server target localhost:8080 (either the agent's
// web UI or the agent's device proxy endpoint). Non-localhost URLs are rejected.
func (ws *WSClient) handleProxyRequest(msg wscommon.Message) {
	requestID, ok := msg.Data["request_id"].(string)
	if !ok {
		WarnCtx("Proxy request missing request_id")
		return
	}

	targetURL, ok := msg.Data["url"].(string)
	if !ok {
		ws.sendProxyError(requestID, "Missing target URL")
		return
	}

	method, ok := msg.Data["method"].(string)
	if !ok || method == "" {
		method = "GET"
	}

	// Check if this is a streaming request
	streaming, _ := msg.Data["streaming"].(bool)

	// Extract headers
	headers := make(map[string]string)
	if headersData, ok := msg.Data["headers"].(map[string]interface{}); ok {
		for k, v := range headersData {
			if vStr, ok := v.(string); ok {
				headers[k] = vStr
			}
		}
	}

	// Decode body if present
	var bodyBytes []byte
	if bodyB64, ok := msg.Data["body"].(string); ok {
		decoded, err := base64.StdEncoding.DecodeString(bodyB64)
		if err == nil {
			bodyBytes = decoded
		}
	}

	// Parse and validate the target URL
	parsed, err := url.Parse(targetURL)
	if err != nil {
		ws.sendProxyError(requestID, fmt.Sprintf("Invalid URL: %v", err))
		return
	}

	// Only allow localhost - all server proxy requests should target our local web server
	hostname := parsed.Hostname()
	if hostname != "localhost" && hostname != "127.0.0.1" && hostname != "::1" {
		WarnCtx("Proxy request rejected: non-localhost URL", "request_id", requestID, "hostname", hostname)
		ws.sendProxyError(requestID, "Proxy only supports localhost targets")
		return
	}

	// Wait for local handler to be registered (handles web server startup race)
	localHandler := ws.waitForLocalHandler(10 * time.Second)
	if localHandler == nil {
		ws.sendProxyError(requestID, "Local handler not available (server may be shutting down)")
		return
	}

	// Use streaming or buffered handler based on request flag
	if streaming {
		DebugCtx("Using streaming handler for proxy request", "request_id", requestID, "path", parsed.Path)
		ws.handleStreamingProxyRequest(requestID, method, parsed.Path, parsed.RawQuery, headers, bodyBytes, localHandler)
	} else {
		DebugCtx("Using local handler for proxy request", "request_id", requestID, "path", parsed.Path)
		ws.handleLocalProxyRequest(requestID, method, parsed.Path, parsed.RawQuery, headers, bodyBytes, localHandler)
	}
}

// handleStreamingProxyRequest handles proxy requests that need to stream the response
// back to the server in real-time (e.g., SSE endpoints like /api/report/stream)
func (ws *WSClient) handleStreamingProxyRequest(requestID, method, path, rawQuery string, headers map[string]string, body []byte, handler http.Handler) {
	start := time.Now()

	// Build the request URL path with query string
	reqPath := path
	if reqPath == "" {
		reqPath = "/"
	}
	if rawQuery != "" {
		reqPath = reqPath + "?" + rawQuery
	}

	TraceTagCtx("proxy", "Streaming proxy request", "request_id", requestID, "method", method, "path", reqPath, "body_bytes", len(body))

	// Create request for the handler
	var req *http.Request
	var err error
	if len(body) > 0 {
		req, err = http.NewRequest(method, reqPath, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(method, reqPath, nil)
	}
	if err != nil {
		ws.sendProxyError(requestID, fmt.Sprintf("Failed to create streaming request: %v", err))
		return
	}

	// Set headers (skip hop-by-hop headers)
	for k, v := range headers {
		if strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Keep-Alive") ||
			strings.EqualFold(k, "Proxy-Authenticate") || strings.EqualFold(k, "Proxy-Authorization") ||
			strings.EqualFold(k, "TE") || strings.EqualFold(k, "Trailers") ||
			strings.EqualFold(k, "Transfer-Encoding") || strings.EqualFold(k, "Upgrade") {
			continue
		}
		req.Header.Set(k, v)
	}

	// Mark this as a server-proxied request via the request context (not
	// spoofable by network clients, unlike headers).
	req = newServerProxyRequest(req)

	// Use our streaming ResponseWriter that sends chunks via WebSocket
	streamWriter := newStreamingResponseWriter(ws, requestID)

	// Invoke the handler - this will call Write() on our streaming writer
	// which sends chunks via WebSocket as they're generated
	handler.ServeHTTP(streamWriter, req)

	// Signal end of stream
	ws.sendProxyStreamEnd(requestID)

	duration := time.Since(start)
	TraceTagCtx("proxy", "Streaming proxy completed", "request_id", requestID, "duration", duration)
}

// handleLocalProxyRequest handles proxy requests to the agent's own web server
// by invoking the HTTP handler directly instead of making an HTTP round-trip.
// This is more efficient and avoids SSRF validation for trusted localhost requests.
func (ws *WSClient) handleLocalProxyRequest(requestID, method, path, rawQuery string, headers map[string]string, body []byte, handler http.Handler) {
	start := time.Now()

	// Build the request URL path with query string
	reqPath := path
	if reqPath == "" {
		reqPath = "/"
	}
	if rawQuery != "" {
		reqPath = reqPath + "?" + rawQuery
	}

	TraceTagCtx("proxy", "Local proxy request (direct handler)", "request_id", requestID, "method", method, "path", reqPath, "body_bytes", len(body))

	// Create request for the handler
	var req *http.Request
	var err error
	if len(body) > 0 {
		req, err = http.NewRequest(method, reqPath, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(method, reqPath, nil)
	}
	if err != nil {
		ws.sendProxyError(requestID, fmt.Sprintf("Failed to create local request: %v", err))
		return
	}

	// Set headers (skip hop-by-hop headers)
	for k, v := range headers {
		if strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Keep-Alive") ||
			strings.EqualFold(k, "Proxy-Authenticate") || strings.EqualFold(k, "Proxy-Authorization") ||
			strings.EqualFold(k, "TE") || strings.EqualFold(k, "Trailers") ||
			strings.EqualFold(k, "Transfer-Encoding") || strings.EqualFold(k, "Upgrade") {
			continue
		}
		req.Header.Set(k, v)
	}

	// Mark this as a server-proxied request via the request context so agent
	// auth can bypass it. Only this in-process path can set the context marker.
	req = newServerProxyRequest(req)

	// Use httptest.ResponseRecorder to capture the response
	recorder := httptest.NewRecorder()

	// Invoke the handler directly
	handler.ServeHTTP(recorder, req)

	// Extract response
	result := recorder.Result()
	respBody, err := io.ReadAll(result.Body)
	result.Body.Close()
	if err != nil {
		ws.sendProxyError(requestID, fmt.Sprintf("Failed to read local response: %v", err))
		return
	}

	// Extract response headers, filtering out frame-busting headers that would
	// prevent the proxied content from being displayed in the server's UI
	respHeaders := make(map[string]string)
	for k, v := range result.Header {
		if len(v) > 0 {
			// Skip frame-busting security headers - these would block proxied device web UIs
			if strings.EqualFold(k, "X-Frame-Options") ||
				strings.EqualFold(k, "Content-Security-Policy") ||
				strings.EqualFold(k, "Content-Security-Policy-Report-Only") {
				continue
			}
			respHeaders[k] = v[0]
		}
	}

	duration := time.Since(start)
	TraceTagCtx("proxy", "Local proxy response completed", "request_id", requestID, "status", result.StatusCode, "duration", duration, "bytes", len(respBody))

	// Send proxy response back to server
	ws.sendProxyResponse(requestID, result.StatusCode, respHeaders, respBody)
}

// sendProxyResponse sends a successful proxy response back to the server
func (ws *WSClient) sendProxyResponse(requestID string, statusCode int, headers map[string]string, body []byte) {
	ws.mu.RLock()
	conn := ws.conn
	connected := ws.connected
	ws.mu.RUnlock()

	if !connected || conn == nil {
		WarnCtx("Cannot send proxy response - not connected", "request_id", requestID)
		return
	}

	msg := wscommon.Message{
		Type: wscommon.MessageTypeProxyResponse,
		Data: map[string]interface{}{
			"request_id":  requestID,
			"status_code": statusCode,
			"headers":     headers,
			"body":        base64.StdEncoding.EncodeToString(body),
		},
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		WarnCtx("Failed to marshal proxy response", "error", err)
		return
	}

	if err := conn.WriteRaw(payload, ws.writeTimeout); err != nil {
		WarnCtx("Failed to send proxy response", "error", err)
	}
}

// sendProxyError sends an error response for a failed proxy request
func (ws *WSClient) sendProxyError(requestID string, errorMsg string) {
	WarnCtx("Proxy error for request", "request_id", requestID, "error", errorMsg)

	ws.sendProxyResponse(requestID, 502, map[string]string{
		"Content-Type": "text/plain",
	}, []byte(errorMsg))
}

// streamingResponseWriter implements http.ResponseWriter and streams chunks via WebSocket
type streamingResponseWriter struct {
	ws          *WSClient
	requestID   string
	headers     http.Header
	statusCode  int
	wroteHeader bool
	headersSent bool
}

func newStreamingResponseWriter(ws *WSClient, requestID string) *streamingResponseWriter {
	return &streamingResponseWriter{
		ws:         ws,
		requestID:  requestID,
		headers:    make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (w *streamingResponseWriter) Header() http.Header {
	return w.headers
}

func (w *streamingResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.statusCode = statusCode

	// Send headers as the first stream chunk
	if !w.headersSent {
		w.headersSent = true
		headers := make(map[string]string)
		for k, v := range w.headers {
			if len(v) > 0 {
				// Skip frame-busting headers
				if strings.EqualFold(k, "X-Frame-Options") ||
					strings.EqualFold(k, "Content-Security-Policy") ||
					strings.EqualFold(k, "Content-Security-Policy-Report-Only") {
					continue
				}
				headers[k] = v[0]
			}
		}
		w.ws.sendProxyStreamStart(w.requestID, statusCode, headers)
	}
}

func (w *streamingResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	// Send chunk via WebSocket
	w.ws.sendProxyStreamChunk(w.requestID, data)
	return len(data), nil
}

// Flush implements http.Flusher - for SSE endpoints this triggers sending buffered data
func (w *streamingResponseWriter) Flush() {
	// Data is already sent immediately in Write(), so Flush is a no-op
	// but we need to implement the interface for SSE handlers to work
}

// sendProxyStreamStart sends the initial headers for a streaming proxy response
func (ws *WSClient) sendProxyStreamStart(requestID string, statusCode int, headers map[string]string) {
	ws.mu.RLock()
	conn := ws.conn
	connected := ws.connected
	ws.mu.RUnlock()

	if !connected || conn == nil {
		WarnCtx("Cannot send stream start - not connected", "request_id", requestID)
		return
	}

	msg := wscommon.Message{
		Type: wscommon.MessageTypeProxyStreamChunk,
		Data: map[string]interface{}{
			"request_id":  requestID,
			"status_code": statusCode,
			"headers":     headers,
			"is_start":    true,
		},
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		WarnCtx("Failed to marshal stream start", "error", err)
		return
	}

	if err := conn.WriteRaw(payload, ws.writeTimeout); err != nil {
		WarnCtx("Failed to send stream start", "error", err)
	}
}

// sendProxyStreamChunk sends a chunk of streaming proxy response data
func (ws *WSClient) sendProxyStreamChunk(requestID string, data []byte) {
	ws.mu.RLock()
	conn := ws.conn
	connected := ws.connected
	ws.mu.RUnlock()

	if !connected || conn == nil {
		return // Silently drop - connection lost
	}

	msg := wscommon.Message{
		Type: wscommon.MessageTypeProxyStreamChunk,
		Data: map[string]interface{}{
			"request_id": requestID,
			"chunk":      base64.StdEncoding.EncodeToString(data),
		},
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	// Use a short timeout for streaming chunks
	conn.WriteRaw(payload, 5*time.Second)
}

// sendProxyStreamEnd signals the end of a streaming proxy response
func (ws *WSClient) sendProxyStreamEnd(requestID string) {
	ws.mu.RLock()
	conn := ws.conn
	connected := ws.connected
	ws.mu.RUnlock()

	if !connected || conn == nil {
		return
	}

	msg := wscommon.Message{
		Type: wscommon.MessageTypeProxyStreamEnd,
		Data: map[string]interface{}{
			"request_id": requestID,
		},
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	conn.WriteRaw(payload, ws.writeTimeout)
}

// CommandHandler is called when a command is received from the server
type CommandHandler func(command string, data map[string]interface{})

// commandHandler is the registered handler for commands
var commandHandler CommandHandler
var commandHandlerMu sync.RWMutex

// SetCommandHandler registers a handler for server commands
func SetCommandHandler(handler CommandHandler) {
	commandHandlerMu.Lock()
	commandHandler = handler
	commandHandlerMu.Unlock()
}

// handleCommand processes a command message from the server
func (ws *WSClient) handleCommand(msg wscommon.Message) {
	command, _ := msg.Data["command"].(string)
	if command == "" {
		WarnCtx("Received command message with no command field")
		return
	}

	InfoCtx("Received command from server", "command", command)

	// Call registered handler if present
	commandHandlerMu.RLock()
	handler := commandHandler
	commandHandlerMu.RUnlock()

	if handler != nil {
		handler(command, msg.Data)
	} else {
		WarnCtx("No command handler registered", "command", command)
	}
}
