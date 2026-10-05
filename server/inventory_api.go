package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"printmaster/server/authz"
	"printmaster/server/storage"
)

// Every request gets a fresh caller scope from the session middleware principal.
func inventoryRequestScope(w http.ResponseWriter, r *http.Request) (storage.InventoryStore, storage.InventoryScope, bool) {
	p := getPrincipal(r)
	if p == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return nil, storage.InventoryScope{}, false
	}
	if !authorizeOrReject(w, r, authz.ActionDevicesRead, authz.ResourceRef{}) {
		return nil, storage.InventoryScope{}, false
	}
	scope, ok := tenantScope(p)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, storage.InventoryScope{}, false
	}
	selection := storage.InventoryScope{Unrestricted: p.IsAdmin(), TenantIDs: []string{}}
	for id := range scope {
		selection.TenantIDs = append(selection.TenantIDs, id)
	}
	sort.Strings(selection.TenantIDs)
	store, ok := serverStore.(storage.InventoryStore)
	if !ok {
		http.Error(w, "Scoped inventory storage unavailable", http.StatusInternalServerError)
		return nil, selection, false
	}
	return store, selection, true
}

func inventorySerials(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var req struct {
		Serials []string `json:"serials"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Serials == nil {
		http.Error(w, "Expected JSON object with serials array", http.StatusBadRequest)
		return nil, false
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		http.Error(w, "Expected one JSON object", http.StatusBadRequest)
		return nil, false
	}
	serials := make([]string, 0, len(req.Serials))
	seen := map[string]bool{}
	for _, serial := range req.Serials {
		if strings.TrimSpace(serial) == "" {
			http.Error(w, "Serials must be nonempty", http.StatusBadRequest)
			return nil, false
		}
		// Preserve exact stored identifiers; whitespace is not silently normalized.
		if !seen[serial] {
			seen[serial] = true
			serials = append(serials, serial)
		}
		if len(serials) > 100 {
			http.Error(w, "At most 100 distinct serials", http.StatusBadRequest)
			return nil, false
		}
	}
	return serials, true
}

func handleDevicesIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	store, scope, ok := inventoryRequestScope(w, r)
	if !ok {
		return
	}
	index, err := store.InventoryIndex(r.Context(), scope)
	if err != nil {
		http.Error(w, "Failed to read inventory index", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func handleDevicesRows(w http.ResponseWriter, r *http.Request) {
	handleInventoryQuery(w, r, false)
}

func handleDevicesMetricsQuery(w http.ResponseWriter, r *http.Request) {
	handleInventoryQuery(w, r, true)
}

// Legacy lists retain best-effort toner/counter enrichment. Chunk keys to bound
// SQL parameters even when the compatible no-limit list spans a large fleet.
func enrichInventoryRows(ctx context.Context, store storage.InventoryStore, scope storage.InventoryScope, rows []*storage.DeviceWithMetrics) []*storage.DeviceWithMetrics {
	for start := 0; start < len(rows); start += 100 {
		end := start + 100
		if end > len(rows) {
			end = len(rows)
		}
		serials := make([]string, 0, end-start)
		for _, d := range rows[start:end] {
			serials = append(serials, d.Serial)
		}
		metrics, err := store.InventoryMetrics(ctx, scope, serials)
		if err != nil {
			logError("Failed to enrich inventory", "error", err)
			continue
		}
		bySerial := make(map[string]*storage.MetricsSnapshot, len(metrics))
		for _, m := range metrics {
			bySerial[m.Serial] = m
		}
		for _, d := range rows[start:end] {
			if m := bySerial[d.Serial]; m != nil && m.AgentID == d.AgentID {
				d.PageCount, d.ColorPages, d.MonoPages, d.ScanCount = m.PageCount, m.ColorPages, m.MonoPages, m.ScanCount
				d.TonerLevels, d.LastMetricsAt = m.TonerLevels, &m.Timestamp
			}
		}
	}
	return rows
}

func handleInventoryQuery(w http.ResponseWriter, r *http.Request, metrics bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	store, scope, ok := inventoryRequestScope(w, r)
	if !ok {
		return
	}
	serials, ok := inventorySerials(w, r)
	if !ok {
		return
	}
	var result interface{}
	var err error
	if metrics {
		result, err = store.InventoryMetrics(r.Context(), scope, serials)
	} else {
		result, err = store.InventoryRows(r.Context(), scope, serials, 0, 0)
	}
	if err != nil {
		http.Error(w, "Failed to query inventory", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
