package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"printmaster/common/updatetrust"
	"printmaster/server/storage"
)

// Serve a read-only mirror of publisher output, never the signing workspace.
// Authorization of these bytes belongs to the Agent's independently pinned TUF
// root; the fleet Server must not reserialize or re-sign metadata.
func handleAgentUpdateMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := r.Context().Value(agentContextKey).(*storage.Agent)
	if !ok || agent == nil || agent.AgentID == "" {
		http.Error(w, "Agent authentication required", http.StatusUnauthorized)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, updatetrust.MetadataRoute)
	if !strings.HasPrefix(r.URL.Path, updatetrust.MetadataRoute) || !updatetrust.ValidMetadataName(name) {
		http.Error(w, "invalid publisher metadata name", http.StatusBadRequest)
		return
	}
	if serverConfig == nil || strings.TrimSpace(serverConfig.Releases.PublisherMetadataDir) == "" {
		http.Error(w, "publisher metadata mirror not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	root, err := os.OpenRoot(serverConfig.Releases.PublisherMetadataDir)
	if err != nil {
		logPublisherMetadataFailure(name, agent.AgentID, err)
		http.Error(w, "publisher metadata mirror unavailable", http.StatusServiceUnavailable)
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if os.IsNotExist(err) {
		// TUF probes the next root version; 404 terminates a legitimate chain.
		http.NotFound(w, r)
		return
	}
	if err != nil {
		logPublisherMetadataFailure(name, agent.AgentID, err)
		http.Error(w, "publisher metadata unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > updatetrust.MaxMetadataBytes {
		if err != nil {
			logPublisherMetadataFailure(name, agent.AgentID, err)
		} else if serverLogger != nil {
			serverLogger.Warn("Rejected publisher metadata file", "name", name, "agent_id", agent.AgentID, "size_bytes", info.Size())
		}
		http.Error(w, "invalid publisher metadata file", http.StatusInternalServerError)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, updatetrust.MaxMetadataBytes+1))
	if err != nil || int64(len(data)) > updatetrust.MaxMetadataBytes || ctx.Err() != nil {
		if err != nil {
			logPublisherMetadataFailure(name, agent.AgentID, err)
		} else if serverLogger != nil {
			serverLogger.Warn("Publisher metadata read exceeded bounds", "name", name, "agent_id", agent.AgentID, "error", ctx.Err())
		}
		http.Error(w, "publisher metadata read failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(data); err != nil {
		logPublisherMetadataFailure(name, agent.AgentID, err)
	}
}

func logPublisherMetadataFailure(name, agentID string, err error) {
	if serverLogger != nil {
		serverLogger.Warn("Publisher metadata delivery failed", "name", name, "agent_id", agentID, "error", err)
	}
}
