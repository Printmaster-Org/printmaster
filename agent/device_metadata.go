package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/storage"
	"printmaster/common/metadata"
)

type metadataStorage interface {
	SyncDeviceMetadata(context.Context, string, []*metadata.Record) error
	ReconcileDeviceMetadata(context.Context, string, []*metadata.Record) error
	EditLocalMetadata(context.Context, string, map[string]interface{}) error
	ReportedDevice(context.Context, *storage.Device) (*storage.Device, error)
}

func (w *UploadWorker) syncDeviceMetadata(ctx context.Context) error {
	w.metadataMu.Lock()
	defer w.metadataMu.Unlock()
	return w.syncDeviceMetadataLocked(ctx)
}

func (w *UploadWorker) syncDeviceMetadataLocked(ctx context.Context) error {
	store, ok := w.store.(metadataStorage)
	if !ok {
		return errors.New("metadata storage unavailable")
	}
	records, err := w.client.DeviceMetadata(ctx)
	if err != nil {
		return err
	}
	if err := store.ReconcileDeviceMetadata(ctx, w.client.GetServerURL(), records); err != nil {
		return err
	}
	if mainScanner != nil {
		return mainScanner.refreshIndex(ctx)
	}
	return nil
}

func handleAgentMetadataUpdate(w http.ResponseWriter, r *http.Request, managed bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	serial, patch, err := metadata.Parse(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	store, ok := deviceStore.(metadataStorage)
	if !ok {
		http.Error(w, "metadata storage unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var record *metadata.Record
	if managed {
		uploadWorkerMu.RLock()
		worker := uploadWorker
		uploadWorkerMu.RUnlock()
		if worker == nil {
			http.Error(w, "Server unavailable; managed metadata edits cannot be saved offline", http.StatusServiceUnavailable)
			return
		}
		worker.metadataMu.Lock()
		select {
		case <-worker.stopCh:
			err = errors.New("Server connection is stopping")
		default:
			record, err = worker.client.EditDeviceMetadata(ctx, serial, patch)
		}
		if err == nil {
			err = store.SyncDeviceMetadata(ctx, worker.client.GetServerURL(), []*metadata.Record{record})
			if err != nil {
				err = fmt.Errorf("Server saved metadata but local synchronization failed: %w", err)
			}
		}
		if err == nil && mainScanner != nil {
			if refreshErr := mainScanner.refreshIndex(ctx); refreshErr != nil {
				err = fmt.Errorf("metadata saved but scanner index refresh failed: %w", refreshErr)
			}
		}
		worker.metadataMu.Unlock()
	} else {
		err = store.EditLocalMetadata(ctx, serial, patch)
	}
	if err != nil {
		status := http.StatusInternalServerError
		if managed {
			status = http.StatusServiceUnavailable
		}
		var response *agent.HTTPStatusError
		if errors.Is(err, storage.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, metadata.ErrPinned) || errors.Is(err, metadata.ErrLocked) {
			status = http.StatusConflict
		} else if errors.As(err, &response) && response.Status >= 400 && response.Status < 500 {
			status = response.Status
		}
		if appLogger != nil {
			appLogger.Error("Device metadata edit failed", "error", err)
		}
		http.Error(w, "Metadata edit failed: "+err.Error(), status)
		return
	}
	if !managed && mainScanner != nil {
		if err := mainScanner.refreshIndex(ctx); err != nil {
			appLogger.Error("Metadata saved but scanner index refresh failed", "error", err)
			http.Error(w, "metadata saved but scanner refresh failed", http.StatusInternalServerError)
			return
		}
	}
	scannerUploadWake()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "metadata": record})
}
