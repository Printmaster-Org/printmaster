package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"printmaster/common/metadata"
)

func testDeviceMetadata(t *testing.T, s *BaseStore) {
	t.Helper()
	ctx := context.Background()
	// PostgreSQL timestamps retain microseconds, not nanoseconds.
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.RegisterAgent(ctx, &Agent{AgentID: "metadata-owner", Token: "metadata-token", RegisteredAt: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	device := &Device{AgentID: "metadata-owner"}
	device.Serial, device.IP, device.Location, device.Description = "METADATA-1", "192.0.2.1", "Reported", "Printer"
	device.LastSeen, device.FirstSeen, device.CreatedAt = now, now, now
	if err := s.UpsertDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	record, err := s.EditDeviceMetadata(ctx, device.Serial, device.AgentID, map[string]interface{}{"location": "Pinned", "description": "", "dns_servers": []string{"192.0.2.53"}}, "", false)
	if err != nil || record.Version != 1 {
		t.Fatalf("pin = %+v, %v", record, err)
	}
	records, err := s.DeviceMetadataForAgent(ctx, device.AgentID)
	if err != nil || len(records) != 1 || records[0].Overrides["location"] != "Pinned" {
		t.Fatalf("owner snapshot = %+v, %v", records, err)
	}
	records, err = s.DeviceMetadataForAgent(ctx, "other-owner")
	if err != nil || len(records) != 0 {
		t.Fatalf("snapshot crossed owner boundary: %+v, %v", records, err)
	}
	device.Location, device.Description = "New reported", "New description"
	if err := s.UpsertDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDevice(ctx, device.Serial)
	if err != nil || got.Location != "Pinned" || got.Description != "" || len(got.DNSServers) != 1 || !got.LastSeen.Equal(now) {
		t.Fatalf("upload replaced pin or observation time: %+v, %v", got, err)
	}
	if _, err := s.EditDeviceMetadata(ctx, device.Serial, device.AgentID, nil, "dns_servers", false); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDevice(ctx, device.Serial)
	if err != nil || len(got.DNSServers) != 0 {
		t.Fatalf("unpin resurrected DNS from raw metadata: %+v, %v", got, err)
	}
	index, err := s.InventoryIndex(ctx, InventoryScope{Unrestricted: true})
	if err != nil || len(index) != 1 || index[0].Location != "Pinned" {
		t.Fatalf("index inconsistent: %+v, %v", index, err)
	}
	if _, err := s.EditDeviceMetadata(ctx, device.Serial, "other-owner", map[string]interface{}{"model": "Bad"}, "", false); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("wrong owner edit = %v", err)
	}
	if _, err := s.EditDeviceMetadata(ctx, device.Serial, device.AgentID, map[string]interface{}{"location": "Bad", "model": "Partial"}, "", true); !errors.Is(err, metadata.ErrPinned) {
		t.Fatalf("Agent pinned edit = %v", err)
	}
	got, _ = s.GetDevice(ctx, device.Serial)
	if got.Model != "" {
		t.Fatal("rejected multi-field edit partially applied")
	}
	if _, err := s.EditDeviceMetadata(ctx, device.Serial, device.AgentID, map[string]interface{}{"model": "Manual"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditDeviceMetadata(ctx, device.Serial, device.AgentID, nil, "location", false); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDevice(ctx, device.Serial)
	if err != nil || got.Location != "New reported" || got.Model != "Manual" {
		t.Fatalf("unpin did not restore report: %+v, %v", got, err)
	}
	// A pin and an ordinary upload must serialize without losing either value.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			incoming := *device
			if err := s.UpsertDevice(ctx, &incoming); err != nil {
				t.Error(err)
			}
		}()
	}
	_, err = s.EditDeviceMetadata(ctx, device.Serial, device.AgentID, map[string]interface{}{"location": "Race pin"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	got, err = s.GetDevice(ctx, device.Serial)
	if err != nil || got.Location != "Race pin" {
		t.Fatalf("concurrent upload replaced pin: %+v, %v", got, err)
	}
}

func TestDeviceMetadata(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testDeviceMetadata(t, s.BaseStore)
}
