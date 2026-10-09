package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestInventorySavedStateRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "saved-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.RegisterAgent(ctx, &Agent{AgentID: "state-agent", Token: "state-token", RegisteredAt: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []*bool{nil, boolPointer(true), boolPointer(false)} {
		device := &Device{AgentID: "state-agent", IsSaved: state}
		device.Serial, device.IP = "STATE-DEVICE", "192.0.2.10"
		device.RawData = map[string]interface{}{"model": "unchanged"}
		if err := store.UpsertDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
		if _, ok := device.RawData["is_saved"]; ok {
			t.Fatal("upsert mutated caller metadata")
		}
		got, err := store.GetDevice(ctx, device.Serial)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := store.InventoryRows(ctx, InventoryScope{Unrestricted: true}, []string{device.Serial}, 0, 0)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows: %+v %v", rows, err)
		}
		for _, result := range []*Device{got, &rows[0].Device} {
			if (result.IsSaved == nil) != (state == nil) || state != nil && *result.IsSaved != *state {
				t.Fatalf("saved flag: got %+v, want %+v", result.IsSaved, state)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			value, present := payload["is_saved"]
			if state == nil && present || state != nil && (!present || value != *state) {
				t.Fatalf("legacy/false serialization: %s", encoded)
			}
		}
	}
}

func boolPointer(value bool) *bool { return &value }
