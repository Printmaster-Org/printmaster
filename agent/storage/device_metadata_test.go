package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"printmaster/common/metadata"
)

func TestServerMetadataPinsAcrossWritersAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UTC()
	device := &Device{}
	device.Serial, device.IP, device.Model, device.Location = "META-1", "192.0.2.1", "Original", "Reported"
	device.LastSeen, device.CreatedAt, device.FirstSeen, device.IsSaved, device.Visible = now, now, now, true, true
	if err := s.Create(ctx, device); err != nil {
		t.Fatal(err)
	}
	record := &metadata.Record{Serial: device.Serial, Version: 1, Overrides: map[string]interface{}{"model": "Pinned", "location": ""}}
	if err := s.SyncDeviceMetadata(ctx, "server-1", []*metadata.Record{record}); err != nil {
		t.Fatal(err)
	}
	device.Model, device.Location = "Spooler model", "Spooler room"
	if err := s.Upsert(ctx, device); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, device.Serial)
	if err != nil || got.Model != "Pinned" || got.Location != "" || len(got.LockedFields) != 0 || !got.LastSeen.Equal(now) {
		t.Fatalf("pins failed or became local locks: %+v, %v", got, err)
	}
	model := "Scanner model"
	if err := s.CommitScannerFacts(ctx, device.Serial, device.IP, DevicePatch{Model: &model}, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, device.Serial)
	reported, err := s.ReportedDevice(ctx, got)
	if err != nil || reported.Model != "Scanner model" || reported.Location != "Spooler room" {
		t.Fatalf("upload cannot recover reports: %+v, %v", reported, err)
	}
	if err := s.EditLocalMetadata(ctx, device.Serial, map[string]interface{}{"model": "Bypass"}); !errors.Is(err, metadata.ErrPinned) {
		t.Fatalf("local pin bypass = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, device.Serial)
	if got.Model != "Pinned" || got.MetadataOverrides["model"] != "Pinned" {
		t.Fatal("restart lost Server authority")
	}
	// An older response cannot undo a newer pin.
	newer := &metadata.Record{Serial: device.Serial, Version: 3, Overrides: map[string]interface{}{"model": "New pin"}}
	if err := s.SyncDeviceMetadata(ctx, "server-1", []*metadata.Record{newer, record}); err != nil {
		t.Fatal(err)
	}
	newer = &metadata.Record{Serial: device.Serial, Version: 4, Overrides: map[string]interface{}{}}
	if err := s.SyncDeviceMetadata(ctx, "server-1", []*metadata.Record{newer}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, device.Serial)
	if got.Model != "Scanner model" || got.Location != "Spooler room" || len(got.MetadataOverrides) != 0 {
		t.Fatalf("unpin did not restore reports: %+v", got)
	}
	if err := s.EditLocalMetadata(ctx, device.Serial, map[string]interface{}{"mac_address": "00:11:22:33:44:55", "dns_servers": []string{"192.0.2.53"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, device.Serial)
	if got.MACAddress != "00:11:22:33:44:55" || len(got.DNSServers) != 1 || !got.LastSeen.Equal(now) {
		t.Fatalf("local edit failed or changed last_seen: %+v", got)
	}
	record = &metadata.Record{Serial: device.Serial, Version: 5, Overrides: map[string]interface{}{"model": "Reconnect pin"}}
	if err := s.SyncDeviceMetadata(ctx, "server-1", []*metadata.Record{record}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileDeviceMetadata(ctx, "server-1", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, device.Serial)
	if got.Model != "Scanner model" || len(got.MetadataOverrides) != 0 {
		t.Fatalf("full reconciliation did not release absent pins: %+v", got)
	}
}

func TestMetadataSyncRejectsMalformedRecordsAtomically(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	device := &Device{}
	device.Serial, device.IP, device.Model = "META-INVALID", "192.0.2.1", "Reported"
	if err := s.Create(ctx, device); err != nil {
		t.Fatal(err)
	}
	valid := &metadata.Record{Serial: device.Serial, Version: 1, Overrides: map[string]interface{}{"model": "Pin"}}
	for _, invalid := range []*metadata.Record{
		nil,
		{Serial: device.Serial, Version: 2},
		{Serial: device.Serial, Version: 2, Overrides: map[string]interface{}{"model": 42}},
	} {
		if err := s.ReconcileDeviceMetadata(ctx, "server", []*metadata.Record{valid, invalid}); err == nil {
			t.Fatal("accepted malformed snapshot")
		}
		got, err := s.Get(ctx, device.Serial)
		if err != nil || got.Model != "Reported" || len(got.MetadataOverrides) != 0 {
			t.Fatalf("malformed snapshot partially applied: %+v, %v", got, err)
		}
	}
	now := time.Now().UTC()
	device.LastSeen = now
	if err := s.Update(ctx, device); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLocalFieldLock(ctx, device.Serial, "model", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.EditLocalMetadata(ctx, device.Serial, map[string]interface{}{"model": "Bypass"}); !errors.Is(err, metadata.ErrLocked) {
		t.Fatalf("standalone locked edit = %v", err)
	}
	got, err := s.Get(ctx, device.Serial)
	if err != nil || !got.LastSeen.Equal(device.LastSeen) || got.Model != "Reported" {
		t.Fatalf("locking changed observation or value: %+v, %v", got, err)
	}
}
