package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/storage"
	"printmaster/common/metadata"
)

func TestManagedMetadataEditOnlineAndOffline(t *testing.T) {
	_, store, _ := runtimeTestSetup(t, "META-AGENT")
	oldWorker := uploadWorker
	oldStore := deviceStore
	deviceStore = store
	t.Cleanup(func() { uploadWorker = oldWorker; deviceStore = oldStore })
	device := &storage.Device{}
	device.Serial, device.IP, device.Location = "META-AGENT", "192.0.2.1", "Reported"
	device.LastSeen, device.FirstSeen, device.CreatedAt = time.Now().UTC(), time.Now().UTC(), time.Now().UTC()
	if err := store.Create(t.Context(), device); err != nil {
		t.Fatal(err)
	}
	pinned := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pinned {
			http.Error(w, "metadata field is controlled by the Server", http.StatusConflict)
			return
		}
		pinned = true
		json.NewEncoder(w).Encode(metadata.Record{Serial: device.Serial, Version: 1, Overrides: map[string]interface{}{"location": "Manual"}})
	}))
	uploadWorker = NewUploadWorker(agent.NewServerClient(server.URL, "owner", "token"), store, stubLogger{}, nil, UploadWorkerConfig{}, "")
	update := func(body string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/devices/update", strings.NewReader(body))
		w := httptest.NewRecorder()
		handleAgentMetadataUpdate(w, r, true)
		if w.Code != want {
			t.Fatalf("edit = %d (%s), want %d", w.Code, w.Body.String(), want)
		}
	}
	update(`{"serial":"META-AGENT","location":"Manual"}`, http.StatusOK)
	got, err := store.Get(t.Context(), device.Serial)
	if err != nil || got.Location != "Manual" || len(got.LockedFields) != 0 || !got.LastSeen.Equal(device.LastSeen) {
		t.Fatalf("managed edit became local pin or changed observation: %+v, %v", got, err)
	}
	update(`{"serial":"META-AGENT","location":"Bypass"}`, http.StatusConflict)
	server.Close()
	update(`{"serial":"META-AGENT","model":"Offline edit"}`, http.StatusServiceUnavailable)
	got, _ = store.Get(t.Context(), device.Serial)
	if got.Model != "" || got.Location != "Manual" {
		t.Fatal("offline edit applied or erased cached pin")
	}
	uploadWorker.Stop()
	update(`{"serial":"META-AGENT","model":"Stopped edit"}`, http.StatusServiceUnavailable)
}

func TestMetadataClientRejectsMalformedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte("null"))
			return
		}
		json.NewEncoder(w).Encode(metadata.Record{Serial: "wrong-device", Version: 1, Overrides: map[string]interface{}{"model": "Pin"}})
	}))
	defer server.Close()
	client := agent.NewServerClient(server.URL, "owner", "token")
	if _, err := client.DeviceMetadata(t.Context()); err == nil {
		t.Fatal("null snapshot would release cached pins")
	}
	if _, err := client.EditDeviceMetadata(t.Context(), "expected-device", map[string]interface{}{"model": "Pin"}); err == nil {
		t.Fatal("accepted edit response for a different device")
	}
}

func TestDisconnectReleasesInFlightMetadataEdit(t *testing.T) {
	_, store, _ := runtimeTestSetup(t, "META-DISCONNECT")
	oldWorker, oldStore := uploadWorker, deviceStore
	deviceStore = store
	t.Cleanup(func() { uploadWorker, deviceStore = oldWorker, oldStore })
	device := &storage.Device{}
	device.Serial, device.IP, device.Model = "META-DISCONNECT", "192.0.2.1", "Reported"
	if err := store.Create(t.Context(), device); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(metadata.Record{Serial: device.Serial, Version: 1, Overrides: map[string]interface{}{"model": "Pin"}})
	}))
	defer server.Close()
	worker := NewUploadWorker(agent.NewServerClient(server.URL, "owner", "token"), store, stubLogger{}, nil, UploadWorkerConfig{}, "")
	uploadWorker = worker
	edited, disconnected := make(chan int, 1), make(chan error, 1)
	go func() {
		w := httptest.NewRecorder()
		handleAgentMetadataUpdate(w, httptest.NewRequest(http.MethodPost, "/devices/update", strings.NewReader(`{"serial":"META-DISCONNECT","model":"Pin"}`)), true)
		edited <- w.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("edit did not reach Server")
	}
	go func() { disconnected <- disconnectFromServer(nil, "") }()
	select {
	case <-worker.stopCh:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not stop worker")
	}
	close(release)
	select {
	case err := <-disconnected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect deadlocked with metadata edit")
	}
	select {
	case code := <-edited:
		if code != http.StatusOK {
			t.Fatalf("in-flight edit = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metadata edit did not complete")
	}
	got, err := store.Get(t.Context(), device.Serial)
	if err != nil || got.Model != "Reported" || len(got.MetadataOverrides) != 0 {
		t.Fatalf("disconnected edit resurrected cached pins: %+v, %v", got, err)
	}
}
