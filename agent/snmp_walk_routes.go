package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"printmaster/agent/agent"
)

// Admin-only SNMP walk capture endpoints:
//
//	GET    /api/diagnostics/snmp-walks                 list walks
//	POST   /api/diagnostics/snmp-walks                 start a walk {ip, roots, max_oids, label}
//	GET    /api/diagnostics/snmp-walks/{id}            walk status
//	GET    /api/diagnostics/snmp-walks/{id}/download   gzipped JSON walk file
//	DELETE /api/diagnostics/snmp-walks/{id}            cancel (running) or delete
const snmpWalkRoutePrefix = "/api/diagnostics/snmp-walks"

var snmpWalkManager *agent.SNMPWalkManager

func registerSNMPWalkRoutes(dir string, meta map[string]string) {
	snmpWalkManager = agent.NewSNMPWalkManager(dir, nil, meta)
	http.HandleFunc(snmpWalkRoutePrefix, handleSNMPWalks)
	http.HandleFunc(snmpWalkRoutePrefix+"/", handleSNMPWalks)
}

// requireAgentAdmin allows server-proxied requests only for server admins and
// local requests only for agent admins.
func requireAgentAdmin(w http.ResponseWriter, r *http.Request) bool {
	if id, ok := agent.ServerProxyFromContext(r.Context()); ok {
		if id.IsAdmin() {
			return true
		}
		http.Error(w, "administrator required", http.StatusForbidden)
		return false
	}
	if principal, ok := r.Context().Value(agentPrincipalContextKey).(*AgentPrincipal); ok && principal != nil {
		if strings.EqualFold(principal.Role, "admin") {
			return true
		}
		http.Error(w, "administrator required", http.StatusForbidden)
		return false
	}
	if agentAuth == nil {
		return true
	}
	if principal, ok := agentAuth.PrincipalForRequest(r); ok && principal != nil && strings.EqualFold(principal.Role, "admin") {
		return true
	}
	http.Error(w, "administrator required", http.StatusForbidden)
	return false
}

func handleSNMPWalks(w http.ResponseWriter, r *http.Request) {
	if !requireAgentAdmin(w, r) {
		return
	}
	if snmpWalkManager == nil {
		http.Error(w, "snmp walks unavailable", http.StatusServiceUnavailable)
		return
	}

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, snmpWalkRoutePrefix), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			writeSNMPWalkJSON(w, http.StatusOK, map[string]interface{}{
				"walks":            snmpWalkManager.List(),
				"default_roots":    agent.DefaultSNMPWalkRoots,
				"default_max_oids": agent.DefaultSNMPWalkMaxOIDs,
				"max_max_oids":     agent.MaxSNMPWalkMaxOIDs,
			})
		case http.MethodPost:
			startSNMPWalk(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	parts := strings.Split(rest, "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodGet {
		downloadSNMPWalk(w, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		job, ok := snmpWalkManager.Get(id)
		if !ok {
			http.Error(w, "walk not found", http.StatusNotFound)
			return
		}
		writeSNMPWalkJSON(w, http.StatusOK, job)
	case http.MethodDelete:
		if err := snmpWalkManager.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeSNMPWalkJSON(w, http.StatusOK, map[string]bool{"success": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func startSNMPWalk(w http.ResponseWriter, r *http.Request) {
	if err := scannerIPScanningAllowed(); err != nil {
		status := http.StatusInternalServerError
		if err == errIPScanningDisabled {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	var req agent.SNMPWalkRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	job, err := snmpWalkManager.Start(req)
	switch {
	case errors.Is(err, agent.ErrSNMPWalkInvalidRequest):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, agent.ErrSNMPWalkBusy):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if appLogger != nil {
		appLogger.Info("Admin SNMP walk started", "id", job.ID, "ip", job.IP, "roots", job.Roots, "max_oids", job.MaxOIDs)
	}
	writeSNMPWalkJSON(w, http.StatusAccepted, job)
}

func downloadSNMPWalk(w http.ResponseWriter, id string) {
	path, job, err := snmpWalkManager.FilePath(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "walk file missing", http.StatusNotFound)
		return
	}
	// Served as an opaque gzip file (not Content-Encoding) so the browser saves
	// the .json.gz as-is.
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(job.FileName)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func writeSNMPWalkJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
