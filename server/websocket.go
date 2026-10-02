package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	wscommon "printmaster/common/ws"
	"printmaster/server/storage"

	"github.com/gorilla/websocket"
)

var (
	// Per-connection map and locks
	wsConnections     = make(map[string]*wscommon.Conn)
	wsConnectionsLock sync.RWMutex

	// (global counters removed - using per-agent diagnostics maps below)
	// Per-agent diagnostics
	wsDiagLock                 sync.RWMutex
	wsPingFailuresPerAgent     = make(map[string]int64)
	wsDisconnectEventsPerAgent = make(map[string]int64)

	// Track pending proxy requests awaiting responses from agents
	proxyRequests     = make(map[string]chan wscommon.Message) // key: requestID
	proxyRequestsLock sync.RWMutex
)

// Use shared message type constants from wscommon

// handleAgentWebSocket handles WebSocket connections from agents
func handleAgentWebSocket(w http.ResponseWriter, r *http.Request, serverStore storage.Store) {
	// Extract client IP address (respects X-Forwarded-For when behind proxy)
	clientIP := getRealIP(r)

	// Extract and validate authentication token from query parameter
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Missing authentication token", http.StatusUnauthorized)
		return
	}

	tokenPrefix := token
	if len(token) > 8 {
		tokenPrefix = token[:8]
	}

	// Always log incoming WS attempt (helps diagnose why agents don't complete handshake)
	logInfo("Incoming WebSocket connection attempt",
		"ip", clientIP,
		"token", tokenPrefix+"...",
		"user_agent", r.Header.Get("User-Agent"))
	logDebug("Incoming WebSocket raw headers",
		"remote_addr", r.RemoteAddr,
		"headers", r.Header)

	// Check if this IP+token is currently blocked
	if authRateLimiter != nil {
		if isBlocked, blockedUntil := authRateLimiter.IsBlocked(clientIP, tokenPrefix); isBlocked {
			logWarn("Blocked WebSocket connection attempt",
				"ip", clientIP,
				"token", tokenPrefix+"...",
				"blocked_until", blockedUntil.Format(time.RFC3339),
				"user_agent", r.Header.Get("User-Agent"))
			logDebug("Blocked WebSocket details", "remote_addr", r.RemoteAddr)
			http.Error(w, "Too many failed attempts. Try again later.", http.StatusTooManyRequests)
			return
		}
	}

	// Authenticate agent
	logDebug("Authenticating WebSocket token", "token_prefix", tokenPrefix+"...", "ip", clientIP)
	agent, err := serverStore.GetAgentByToken(r.Context(), token)
	if err != nil {
		// Record failed attempt and check if we should log
		var isBlocked, shouldLog bool
		var attemptCount int
		if authRateLimiter != nil {
			isBlocked, shouldLog, attemptCount = authRateLimiter.RecordFailure(clientIP, tokenPrefix)
		} else {
			isBlocked, shouldLog = false, true
		}

		if shouldLog {
			fields := []interface{}{
				"ip", clientIP,
				"token", tokenPrefix + "...",
				"error", err.Error(),
				"attempt_count", attemptCount,
				"protocol", "websocket",
				"user_agent", r.Header.Get("User-Agent"),
			}

			if isBlocked {
				fields = append(fields, "status", "BLOCKED")
				logError("WebSocket auth failed - IP blocked", fields...)

				// Log to audit trail when blocking occurs
				logAuditEntry(r.Context(), &storage.AuditEntry{
					ActorType: storage.AuditActorAgent,
					ActorID:   tokenPrefix,
					Action:    "auth_blocked_websocket",
					Details: fmt.Sprintf("IP blocked after %d failed WebSocket auth attempts with token %s... Error: %s",
						attemptCount, tokenPrefix, err.Error()),
					IPAddress: clientIP,
					UserAgent: r.Header.Get("User-Agent"),
					Severity:  storage.AuditSeverityWarn,
					Metadata: map[string]interface{}{
						"attempt_count": attemptCount,
						"protocol":      "websocket",
					},
				})
			} else if attemptCount >= 3 {
				logWarn("Repeated WebSocket auth failures", fields...)
			} else {
				logWarn("Invalid WebSocket authentication", fields...)
			}
		}

		if isBlocked {
			http.Error(w, "Too many failed attempts. Try again later.", http.StatusTooManyRequests)
		} else {
			http.Error(w, "Invalid authentication token", http.StatusUnauthorized)
		}
		return
	}

	if agent == nil {
		// Same rate limiting logic for nil agent
		var isBlocked, shouldLog bool
		var attemptCount int
		if authRateLimiter != nil {
			isBlocked, shouldLog, attemptCount = authRateLimiter.RecordFailure(clientIP, tokenPrefix)
		} else {
			isBlocked, shouldLog = false, true
		}

		if shouldLog {
			if isBlocked {
				logError("WebSocket auth returned nil agent - IP blocked",
					"ip", clientIP,
					"token", tokenPrefix+"...",
					"attempt_count", attemptCount,
					"status", "BLOCKED")

				// Log to audit trail when blocking occurs
				logAuditEntry(r.Context(), &storage.AuditEntry{
					ActorType: storage.AuditActorAgent,
					ActorID:   tokenPrefix,
					Action:    "auth_blocked_websocket",
					Details: fmt.Sprintf("IP blocked after %d failed WebSocket auth attempts with token %s... (nil agent)",
						attemptCount, tokenPrefix),
					IPAddress: clientIP,
					UserAgent: r.Header.Get("User-Agent"),
					Severity:  storage.AuditSeverityWarn,
					Metadata: map[string]interface{}{
						"attempt_count": attemptCount,
						"protocol":      "websocket",
					},
				})
			} else {
				logWarn("WebSocket auth returned nil agent",
					"ip", clientIP,
					"token", tokenPrefix+"...",
					"attempt_count", attemptCount)
			}
		}

		if isBlocked {
			http.Error(w, "Too many failed attempts. Try again later.", http.StatusTooManyRequests)
		} else {
			http.Error(w, "Invalid authentication token", http.StatusUnauthorized)
		}
		return
	}

	// Success - clear any failure records for this IP+token
	if authRateLimiter != nil {
		authRateLimiter.RecordSuccess(clientIP, tokenPrefix)
	}

	logDebug("WebSocket authentication success", "agent_id_guess", agent.AgentID, "hostname", agent.Hostname, "ip", clientIP)

	// Upgrade HTTP connection to WebSocket (use shared wrapper)
	conn, err := wscommon.UpgradeHTTP(w, r)
	if err != nil {
		logError("WebSocket upgrade failed",
			"agent_id", agent.AgentID,
			"hostname", agent.Hostname,
			"ip", clientIP,
			"error", err)
		return
	}

	logInfo("Agent WebSocket connected",
		"agent_id", agent.AgentID,
		"hostname", agent.Hostname,
		"ip", clientIP,
		"remote_addr", r.RemoteAddr)
	logDebug("Agent WebSocket connect details", "user_agent", r.Header.Get("User-Agent"), "headers", r.Header)

	// Register connection
	wsConnectionsLock.Lock()
	// Close existing connection if any (agent reconnecting)
	if existingConn, exists := wsConnections[agent.AgentID]; exists {
		logInfo("Closing existing WebSocket for reconnection", "agent_id", agent.AgentID)
		existingConn.Close()
	}
	wsConnections[agent.AgentID] = conn
	wsConnectionsLock.Unlock()

	logDebug("Registered WebSocket connection for agent", "agent_id", agent.AgentID)

	// Broadcast agent_connected event to UI via SSE
	sseHub.Broadcast(SSEEvent{
		Type: "agent_connected",
		Data: map[string]interface{}{
			"agent_id": agent.AgentID,
			"name":     agent.Name,
		},
	})

	// Start server-side ping loop to detect half-open TCP connections and
	// surface failures earlier. If ping fails, close the connection which
	// triggers the cleanup below and marks the agent offline.
	pingTicker := time.NewTicker(25 * time.Second)
	pingDone := make(chan struct{})
	go func() {
		defer pingTicker.Stop()
		for {
			select {
			case <-pingTicker.C:
				// send ping
				if err := conn.WritePing(10 * time.Second); err != nil {
					logWarn("WebSocket ping failed, closing connection", "agent_id", agent.AgentID, "error", err)
					wsDiagLock.Lock()
					wsPingFailuresPerAgent[agent.AgentID]++
					pf := wsPingFailuresPerAgent[agent.AgentID]
					de := wsDisconnectEventsPerAgent[agent.AgentID]
					wsDiagLock.Unlock()
					// Broadcast diagnostic SSE event so UI can update
					sseHub.Broadcast(SSEEvent{
						Type: "agent_ws_diag",
						Data: map[string]interface{}{
							"agent_id":             agent.AgentID,
							"ws_ping_failures":     pf,
							"ws_disconnect_events": de,
						},
					})
					conn.Close()
					return
				}
			case <-pingDone:
				return
			}
		}
	}()

	// Handle connection cleanup on exit
	defer func() {
		// signal ping goroutine to stop
		close(pingDone)

		wsConnectionsLock.Lock()
		if wsConnections[agent.AgentID] == conn {
			delete(wsConnections, agent.AgentID)
			logInfo("Agent WebSocket disconnected", "agent_id", agent.AgentID)

			// Broadcast agent_disconnected event to UI via SSE
			sseHub.Broadcast(SSEEvent{
				Type: "agent_disconnected",
				Data: map[string]interface{}{
					"agent_id": agent.AgentID,
				},
			})
			// Mark disconnect event for diagnostics
			wsDiagLock.Lock()
			wsDisconnectEventsPerAgent[agent.AgentID]++
			pf := wsPingFailuresPerAgent[agent.AgentID]
			de := wsDisconnectEventsPerAgent[agent.AgentID]
			wsDiagLock.Unlock()
			// Broadcast diagnostic SSE event so UI can update
			sseHub.Broadcast(SSEEvent{
				Type: "agent_ws_diag",
				Data: map[string]interface{}{
					"agent_id":             agent.AgentID,
					"ws_ping_failures":     pf,
					"ws_disconnect_events": de,
				},
			})

			// Debounce marking offline: wait a short window before flipping DB state
			go func(agentID string) {
				// Wait 10 seconds to allow quick reconnects
				time.Sleep(10 * time.Second)
				// If agent reconnected, skip marking offline
				if isAgentConnectedWS(agentID) {
					logDebug("Agent reconnected during debounce window, skipping offline mark", "agent_id", agentID)
					return
				}
				// Use timeout context to prevent hanging database operations
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := serverStore.UpdateAgentHeartbeat(ctx, agentID, "offline"); err != nil {
					logWarn("Failed to mark agent offline after WS disconnect", "agent_id", agentID, "error", err)
				}
			}(agent.AgentID)
		}
		wsConnectionsLock.Unlock()
		conn.Close()
	}()

	// Set up ping/pong handler to keep connection alive
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Read messages from agent
	for {
		message, err := conn.ReadMessage()
		if err != nil {
			if wscommon.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logWarn("WebSocket error", "agent_id", agent.AgentID, "error", err)
			}
			break
		}

		logTrace("WebSocket raw message received", "agent_id", agent.AgentID, "len", len(message))

		// Parse message
		var msg wscommon.Message
		if err := json.Unmarshal(message, &msg); err != nil {
			logWarn("Failed to parse WebSocket message", "agent_id", agent.AgentID, "error", err)
			sendWSError(conn, "Invalid message format")
			continue
		}

		// Handle different message types
		switch msg.Type {
		case wscommon.MessageTypeHeartbeat:
			handleWSHeartbeat(conn, agent, msg, serverStore)
		case wscommon.MessageTypeProxyResponse:
			handleWSProxyResponse(agent.AgentID, msg)
		case wscommon.MessageTypeProxyStreamChunk:
			handleWSProxyStreamChunk(agent.AgentID, msg)
		case wscommon.MessageTypeProxyStreamEnd:
			handleWSProxyStreamEnd(agent.AgentID, msg)
		case wscommon.MessageTypeUpdateProgress:
			handleWSUpdateProgress(agent, msg)
		case wscommon.MessageTypeJobProgress:
			handleWSJobProgress(agent, msg)
		case wscommon.MessageTypeDeviceDeleted:
			handleWSDeviceDeleted(agent, msg, serverStore)
		default:
			logWarn("Unknown WebSocket message type", "agent_id", agent.AgentID, "message_type", msg.Type)
			sendWSError(conn, "Unknown message type")
		}
	}
}

// handleWSHeartbeat processes heartbeat messages received via WebSocket
func handleWSHeartbeat(conn *wscommon.Conn, agent *storage.Agent, msg wscommon.Message, serverStore storage.Store) {
	// Extract optional device count from heartbeat data
	deviceCount := 0
	if dc, ok := msg.Data["device_count"].(float64); ok {
		deviceCount = int(dc)
	}

	status := wsStringField(msg.Data, "status")
	if status == "" {
		status = "active"
	}

	// Build HeartbeatData from WebSocket message using shared logic
	hbData := &storage.HeartbeatData{
		Status:          status,
		Version:         wsStringField(msg.Data, "version"),
		ProtocolVersion: wsStringField(msg.Data, "protocol_version"),
		Hostname:        wsStringField(msg.Data, "hostname"),
		IP:              wsStringField(msg.Data, "ip"),
		Platform:        wsStringField(msg.Data, "platform"),
		OSVersion:       wsStringField(msg.Data, "os_version"),
		GoVersion:       wsStringField(msg.Data, "go_version"),
		Architecture:    wsStringField(msg.Data, "architecture"),
		BuildType:       wsStringField(msg.Data, "build_type"),
		GitCommit:       wsStringField(msg.Data, "git_commit"),
		DeviceCount:     deviceCount,
	}

	// Use timeout context to prevent database operations from hanging indefinitely
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if agentUpdate := hbData.BuildAgentUpdate(agent.AgentID); agentUpdate != nil {
		if err := serverStore.UpdateAgentInfo(ctx, agentUpdate); err != nil {
			logError("Failed to update agent metadata after WebSocket heartbeat", "agent_id", agent.AgentID, "error", err)
			sendWSError(conn, "Failed to process heartbeat")
			return
		}
	} else {
		if err := serverStore.UpdateAgentHeartbeat(ctx, agent.AgentID, status); err != nil {
			logError("Failed to update agent after WebSocket heartbeat", "agent_id", agent.AgentID, "error", err)
			sendWSError(conn, "Failed to process heartbeat")
			return
		}
	}

	logDebug("WebSocket heartbeat received", "agent_id", agent.AgentID)

	// Send pong response
	pongMsg := wscommon.Message{
		Type:      wscommon.MessageTypePong,
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(pongMsg)
	if err != nil {
		logError("Failed to marshal pong message", "error", err)
		return
	}

	if err := conn.WriteRaw(payload, 10*time.Second); err != nil {
		logWarn("Failed to send pong to agent", "agent_id", agent.AgentID, "error", err)
	}
}

func wsStringField(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	val, ok := data[key]
	if !ok || val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

// sendWSError sends an error message to the WebSocket client
func sendWSError(conn *wscommon.Conn, errorMsg string) {
	msg := wscommon.Message{
		Type: wscommon.MessageTypeError,
		Data: map[string]interface{}{
			"message": errorMsg,
		},
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		logError("Failed to marshal WebSocket error message", "error", err)
		return
	}

	if err := conn.WriteRaw(payload, 10*time.Second); err != nil {
		logWarn("Failed to send WebSocket error message", "error", err)
	}
}

// getAgentWSConnection returns the WebSocket connection for an agent, if connected
func getAgentWSConnection(agentID string) (*wscommon.Conn, bool) {
	wsConnectionsLock.RLock()
	defer wsConnectionsLock.RUnlock()
	conn, exists := wsConnections[agentID]
	return conn, exists
}

// isAgentConnectedWS checks if an agent has an active WebSocket connection
func isAgentConnectedWS(agentID string) bool {
	wsConnectionsLock.RLock()
	defer wsConnectionsLock.RUnlock()
	_, exists := wsConnections[agentID]
	return exists
}

// closeAgentWebSocket closes the WebSocket connection for an agent
func closeAgentWebSocket(agentID string) {
	wsConnectionsLock.Lock()
	defer wsConnectionsLock.Unlock()

	if conn, exists := wsConnections[agentID]; exists {
		conn.Close()
		delete(wsConnections, agentID)
		logInfo("Closed WebSocket connection for deleted agent", "agent_id", agentID)
	}
}

// cleanupAgentDiagnostics removes diagnostic counters for a deleted agent.
// Call this when an agent is permanently deleted to prevent memory growth.
func cleanupAgentDiagnostics(agentID string) {
	wsDiagLock.Lock()
	defer wsDiagLock.Unlock()
	delete(wsPingFailuresPerAgent, agentID)
	delete(wsDisconnectEventsPerAgent, agentID)
}

// proxyRequestOwnedBy checks the target identity embedded by all three server
// proxy dispatchers in their pending request keys: agentID + "-" + UnixNano.
// Split at the LAST hyphen: agent IDs can contain hyphens, and prefix matching
// would let one agent impersonate another. Call only after a pending-key lookup;
// an unsolicited ID supplied by an agent is never an authorization credential.
func proxyRequestOwnedBy(requestID, agentID string) bool {
	i := strings.LastIndexByte(requestID, '-')
	if agentID == "" || i <= 0 || requestID[:i] != agentID {
		return false
	}
	n, err := strconv.ParseInt(requestID[i+1:], 10, 64)
	return err == nil && n > 0
}

// handleWSProxyResponse handles HTTP proxy responses from authenticated agents.
func handleWSProxyResponse(agentID string, msg wscommon.Message) {
	requestID, ok := msg.Data["request_id"].(string)
	if !ok {
		logWarn("Proxy response missing request_id")
		return
	}

	logTrace("Received WS proxy response", "request_id", requestID)

	// Find the waiting channel for this request
	proxyRequestsLock.Lock()
	defer proxyRequestsLock.Unlock()
	respChan, exists := proxyRequests[requestID]

	if !exists {
		logWarn("Received proxy response for unknown request ID", "request_id", requestID)
		return
	}
	if !proxyRequestOwnedBy(requestID, agentID) {
		logWarn("Rejected foreign agent proxy response", "agent_id", agentID, "request_id", requestID)
		return
	}
	delete(proxyRequests, requestID)

	// Keep the map lock through delivery: HTTP cleanup uses it before closing
	// the channel. Releasing it first can race cleanup and panic on send.
	// Send response to waiting HTTP handler (non-blocking with timeout)
	select {
	case respChan <- msg:
		// Successfully delivered
	case <-time.After(5 * time.Second):
		logWarn("Timeout delivering proxy response", "request_id", requestID)
	}
}

// handleWSProxyStreamChunk handles streaming proxy response chunks from agents
func handleWSProxyStreamChunk(agentID string, msg wscommon.Message) {
	requestID, ok := msg.Data["request_id"].(string)
	if !ok {
		logWarn("Stream chunk missing request_id")
		return
	}

	// Find the waiting channel for this request (don't delete - more chunks coming)
	proxyRequestsLock.RLock()
	defer proxyRequestsLock.RUnlock()
	respChan, exists := proxyRequests[requestID]

	if !exists {
		// This can happen if the connection was closed - not necessarily an error
		logDebug("Received stream chunk for unknown/closed request", "request_id", requestID)
		return
	}
	if !proxyRequestOwnedBy(requestID, agentID) {
		logWarn("Rejected foreign agent proxy stream chunk", "agent_id", agentID, "request_id", requestID)
		return
	}

	// Forward the chunk to the HTTP handler (non-blocking)
	select {
	case respChan <- msg:
		// Successfully delivered
	default:
		// Channel full - consumer might be slow
		logDebug("Stream chunk dropped (channel full)", "request_id", requestID)
	}
}

// handleWSProxyStreamEnd handles end of streaming proxy response
func handleWSProxyStreamEnd(agentID string, msg wscommon.Message) {
	requestID, ok := msg.Data["request_id"].(string)
	if !ok {
		logWarn("Stream end missing request_id")
		return
	}

	logDebug("Received stream end", "request_id", requestID)

	// Find and remove the waiting channel
	proxyRequestsLock.Lock()
	defer proxyRequestsLock.Unlock()
	respChan, exists := proxyRequests[requestID]

	if !exists {
		return
	}
	if !proxyRequestOwnedBy(requestID, agentID) {
		logWarn("Rejected foreign agent proxy stream end", "agent_id", agentID, "request_id", requestID)
		return
	}
	delete(proxyRequests, requestID)

	// Send end signal to the HTTP handler
	endMsg := wscommon.Message{
		Type: wscommon.MessageTypeProxyStreamEnd,
		Data: map[string]interface{}{
			"request_id": requestID,
			"stream_end": true,
		},
	}

	select {
	case respChan <- endMsg:
		// Successfully delivered
	case <-time.After(5 * time.Second):
		logWarn("Timeout delivering stream end", "request_id", requestID)
	}
}

// sendProxyRequest sends an HTTP proxy request to an agent via WebSocket
func sendProxyRequest(agentID string, requestID string, targetURL string, method string,
	headers map[string]string, body string) error {

	conn, exists := getAgentWSConnection(agentID)
	if !exists {
		return fmt.Errorf("agent not connected via WebSocket")
	}

	msg := wscommon.Message{
		Type: wscommon.MessageTypeProxyRequest,
		Data: map[string]interface{}{
			"request_id": requestID,
			"url":        targetURL,
			"method":     method,
			"headers":    headers,
			"body":       body,
		},
		Timestamp: time.Now(),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal proxy request: %w", err)
	}

	if err := conn.WriteRaw(payload, 10*time.Second); err != nil {
		return fmt.Errorf("failed to send proxy request: %w", err)
	}

	return nil
}

// handleWSUpdateProgress processes update progress messages from agents
// and broadcasts them to connected UI clients via SSE
func handleWSUpdateProgress(agent *storage.Agent, msg wscommon.Message) {
	if msg.Data == nil {
		msg.Data = make(map[string]interface{})
	}
	// Check status to determine log level - failures should be logged as errors
	status, _ := msg.Data["status"].(string)
	if status == "failed" {
		errorMsg, _ := msg.Data["error"].(string)
		logError("Agent update failed", "agent_id", agent.AgentID, "error", errorMsg, "data", msg.Data)
	} else {
		logDebug("Update progress received", "agent_id", agent.AgentID, "data", msg.Data)
	}

	// Add agent_id to the data for UI routing
	msg.Data["agent_id"] = agent.AgentID

	// Broadcast to UI clients via the SSE event system
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{
			Type: "update_progress",
			Data: msg.Data,
		})
	}
}

// handleWSJobProgress processes background job progress messages from agents
// and broadcasts them to connected UI clients via SSE for real-time updates.
// Jobs include metrics collection, device scanning, report generation, etc.
func handleWSJobProgress(agent *storage.Agent, msg wscommon.Message) {
	if msg.Data == nil {
		msg.Data = make(map[string]interface{})
	}
	jobID, _ := msg.Data["job_id"].(string)
	jobType, _ := msg.Data["job_type"].(string)
	status, _ := msg.Data["status"].(string)

	// Log appropriate level based on status
	if status == "failed" {
		errorMsg, _ := msg.Data["error"].(string)
		logError("Agent job failed",
			"agent_id", agent.AgentID,
			"job_id", jobID,
			"job_type", jobType,
			"error", errorMsg)
	} else {
		logDebug("Job progress received",
			"agent_id", agent.AgentID,
			"job_id", jobID,
			"job_type", jobType,
			"status", status)
	}

	// Add agent_id to the data for UI routing
	msg.Data["agent_id"] = agent.AgentID

	// Broadcast to UI clients via the SSE event system
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{
			Type: "job_progress",
			Data: msg.Data,
		})
	}
}

// handleWSDeviceDeleted processes device deletion messages from agents.
// When an agent deletes a device locally, it notifies the server to sync the deletion.
func handleWSDeviceDeleted(agent *storage.Agent, msg wscommon.Message, store storage.Store) {
	if agent == nil || agent.AgentID == "" {
		return
	}
	serial, _ := msg.Data["serial"].(string)
	if serial == "" {
		logError("Device deleted message missing serial", "agent_id", agent.AgentID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	device, err := store.GetDevice(ctx, serial)
	if err != nil || device == nil || device.AgentID != agent.AgentID {
		logWarn("Rejected device deletion without agent ownership",
			"agent_id", agent.AgentID, "serial", serial, "error", err)
		return
	}

	logInfo("Agent deleted device, syncing to server",
		"agent_id", agent.AgentID,
		"serial", serial)

	// Recheck ownership in the DELETE itself: a serial could be deleted and
	// rediscovered by another agent between the lookup and this write. Both
	// production stores embed BaseStore. Fail closed for unsupported stores.
	ownedStore, ok := store.(interface {
		DeleteDeviceForAgent(context.Context, string, string) error
	})
	if !ok {
		logError("Storage lacks atomic agent device deletion", "agent_id", agent.AgentID)
		return
	}
	err = ownedStore.DeleteDeviceForAgent(ctx, serial, agent.AgentID)
	if err != nil {
		// Log but don't fail - device may not exist on server yet
		logWarn("Failed to delete device from server during agent sync",
			"serial", serial,
			"agent_id", agent.AgentID,
			"error", err.Error())
		return
	} else {
		logInfo("Device deleted from server (synced from agent)",
			"serial", serial,
			"agent_id", agent.AgentID)
	}

	// Broadcast to UI clients so they can update their view
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{
			Type: "device_deleted",
			Data: map[string]any{
				"serial":   serial,
				"agent_id": agent.AgentID,
				"source":   "agent",
			},
		})
	}
}

// WSCounter implements the WSConnectionCounter interface for metrics collection.
type WSCounter struct{}

// GetConnectionCount returns the total number of WebSocket connections.
func (c *WSCounter) GetConnectionCount() int {
	wsConnectionsLock.RLock()
	defer wsConnectionsLock.RUnlock()
	return len(wsConnections)
}

// GetAgentCount returns the number of connected agents (same as connection count for this server).
func (c *WSCounter) GetAgentCount() int {
	wsConnectionsLock.RLock()
	defer wsConnectionsLock.RUnlock()
	return len(wsConnections)
}

// IsAgentConnected checks if a specific agent has an active WebSocket connection.
func (c *WSCounter) IsAgentConnected(agentID string) bool {
	wsConnectionsLock.RLock()
	defer wsConnectionsLock.RUnlock()
	_, exists := wsConnections[agentID]
	return exists
}
