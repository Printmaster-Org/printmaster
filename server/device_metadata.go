package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"printmaster/common/metadata"
	"printmaster/server/authz"
	"printmaster/server/storage"
)

type deviceMetadataStore interface {
	EditDeviceMetadata(context.Context, string, string, map[string]interface{}, string, bool) (*metadata.Record, error)
	DeviceMetadataForAgent(context.Context, string) ([]*metadata.Record, error)
}

func handleDeviceMetadata(w http.ResponseWriter, r *http.Request) {
	agent, ok := r.Context().Value(agentContextKey).(*storage.Agent)
	if !ok || agent == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost {
		handleMetadataEdit(w, r, agent.AgentID, false)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	store, ok := serverStore.(deviceMetadataStore)
	if !ok {
		http.Error(w, "metadata storage unavailable", http.StatusServiceUnavailable)
		return
	}
	records, err := store.DeviceMetadataForAgent(r.Context(), agent.AgentID)
	if err != nil {
		logError("Failed to read device metadata", "error", err)
		http.Error(w, "metadata lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

func handleMetadataEdit(w http.ResponseWriter, r *http.Request, agentID string, unpin bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var serial, field string
	var patch map[string]interface{}
	if unpin {
		var req struct{ Serial, Field string }
		err = json.Unmarshal(body, &req)
		serial, field = req.Serial, req.Field
		if err == nil && (serial == "" || !metadata.Known(field)) {
			err = errors.New("serial and supported field required")
		}
	} else {
		serial, patch, err = metadata.Parse(body)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	device, err := serverStore.GetDevice(r.Context(), serial)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && device == nil) {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	if err != nil {
		logError("Device metadata lookup failed", "error", err)
		http.Error(w, "metadata lookup failed", http.StatusInternalServerError)
		return
	}
	fromAgent := agentID != ""
	if fromAgent {
		if device.AgentID != agentID {
			http.Error(w, "device owner mismatch", http.StatusForbidden)
			return
		}
	} else {
		if _, ok := authorizeInventoryAgent(w, r, device.AgentID, authz.ActionProxyDeviceConnect); !ok {
			return
		}
		agentID = device.AgentID
	}
	store, ok := serverStore.(deviceMetadataStore)
	if !ok {
		http.Error(w, "metadata storage unavailable", http.StatusServiceUnavailable)
		return
	}
	record, err := store.EditDeviceMetadata(r.Context(), serial, agentID, patch, field, fromAgent)
	if err != nil {
		if errors.Is(err, metadata.ErrPinned) || errors.Is(err, storage.ErrOwnershipConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			logError("Device metadata edit failed", "error", err)
			http.Error(w, "metadata update failed", http.StatusInternalServerError)
		}
		return
	}
	entry := &storage.AuditEntry{
		ActorType: storage.AuditActorAgent, ActorID: agentID, Action: "device.metadata",
		Details: "Updated device metadata overrides", Metadata: map[string]interface{}{"serial": serial, "from_agent": fromAgent, "unpin": field},
		IPAddress: extractClientIP(r),
	}
	if !fromAgent {
		if principal := getPrincipal(r); principal != nil && principal.User != nil {
			entry.ActorType, entry.ActorID, entry.ActorName = storage.AuditActorUser, principal.User.Username, principal.User.Username
		}
	}
	logAuditEntry(r.Context(), entry)
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{Type: "device_updated", Data: map[string]interface{}{"agent_id": agentID, "serial": serial}})
	}
	w.Header().Set("Content-Type", "application/json")
	if fromAgent {
		json.NewEncoder(w).Encode(record)
		return
	}
	updated, err := serverStore.GetDevice(r.Context(), serial)
	if err != nil {
		logError("Metadata saved but device reload failed", "error", err)
		http.Error(w, "metadata saved but reload failed", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "metadata": record, "device": updated})
}

func handleDeviceMetadataUnpin(w http.ResponseWriter, r *http.Request) {
	handleMetadataEdit(w, r, "", true)
}
