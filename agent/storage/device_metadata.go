package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"printmaster/common/metadata"
	commonstorage "printmaster/common/storage"
)

func (s *SQLiteStore) metadataStore() metadata.Store {
	return metadata.Store{DB: s.db, Query: func(q string) string { return q }}
}

func (s *SQLiteStore) applyMetadata(ctx context.Context, tx *sql.Tx, serial string, patch map[string]interface{}) error {
	if len(patch) == 0 {
		return nil
	}
	var sets []string
	var args []interface{}
	for _, field := range metadata.Fields {
		value, ok := patch[field]
		if !ok {
			continue
		}
		if field == "dns_servers" {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			value = string(data)
		}
		sets = append(sets, field+"=?")
		args = append(args, value)
	}
	if len(sets) == 0 {
		return fmt.Errorf("no supported metadata fields")
	}
	args = append(args, serial)
	_, err := tx.ExecContext(ctx, "UPDATE devices SET "+strings.Join(sets, ",")+" WHERE serial=?", args...)
	return err
}

func (s *SQLiteStore) EditLocalMetadata(ctx context.Context, serial string, patch map[string]interface{}) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE devices SET serial=serial WHERE serial=?", serial); err != nil {
		return err
	}
	device, err := s.getDevice(ctx, tx, serial)
	if err != nil {
		return err
	}
	if err := metadata.Locked(device.LockedFields, patch); err != nil {
		return err
	}
	for field := range patch {
		if _, pinned := device.MetadataOverrides[field]; pinned {
			return fmt.Errorf("%w: %s", metadata.ErrPinned, field)
		}
	}
	if err := s.applyMetadata(ctx, tx, serial, patch); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) SyncDeviceMetadata(ctx context.Context, owner string, records []*metadata.Record) error {
	return s.syncDeviceMetadata(ctx, owner, records, false)
}

func (s *SQLiteStore) SetLocalFieldLock(ctx context.Context, serial, field string, locked bool, learned interface{}) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE devices SET serial=serial WHERE serial=?", serial); err != nil {
		return err
	}
	device, err := s.getDevice(ctx, tx, serial)
	if err != nil {
		return err
	}
	for name := range device.MetadataOverrides {
		if strings.EqualFold(name, field) {
			return metadata.ErrPinned
		}
	}
	locks := make([]FieldLock, 0, len(device.LockedFields)+1)
	for _, lock := range device.LockedFields {
		if !strings.EqualFold(lock.Field, field) {
			locks = append(locks, lock)
		}
	}
	if locked {
		locks = append(locks, FieldLock{Field: strings.ToLower(field), LockedAt: time.Now(), Reason: "user_locked"})
	}
	lockJSON, err := json.Marshal(locks)
	if err != nil {
		return err
	}
	if device.RawData == nil {
		device.RawData = map[string]interface{}{}
	}
	if learned != nil {
		device.RawData["learned_oids"] = learned
	}
	rawJSON, err := json.Marshal(device.RawData)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE devices SET locked_fields=?,raw_data=? WHERE serial=?", string(lockJSON), string(rawJSON), serial); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) ReconcileDeviceMetadata(ctx context.Context, owner string, records []*metadata.Record) error {
	return s.syncDeviceMetadata(ctx, owner, records, true)
}

func (s *SQLiteStore) syncDeviceMetadata(ctx context.Context, owner string, records []*metadata.Record, full bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE device_metadata SET version=version"); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, incoming := range records {
		if incoming == nil || incoming.Serial == "" || incoming.Version <= 0 || incoming.Overrides == nil || owner == "" {
			return fmt.Errorf("invalid Server metadata record")
		}
		if err := metadata.Apply(&commonstorage.Device{}, incoming.Overrides); err != nil {
			return err
		}
		seen[incoming.Serial] = true
		result, err := tx.ExecContext(ctx, "UPDATE devices SET serial=serial WHERE serial=?", incoming.Serial)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		device, err := s.getDevice(ctx, tx, incoming.Serial)
		if err != nil {
			return err
		}
		old, err := s.metadataStore().Load(ctx, tx, incoming.Serial)
		if err != nil {
			return err
		}
		if old.Owner == owner && old.Version >= incoming.Version {
			continue
		}
		values := metadata.Values(&device.Device)
		reported := make(map[string]interface{})
		restore := make(map[string]interface{})
		for field := range old.Overrides {
			if value, ok := old.Reported[field]; ok {
				restore[field] = value
				values[field] = value
			}
		}
		for field := range incoming.Overrides {
			reported[field] = values[field]
		}
		if err := s.applyMetadata(ctx, tx, incoming.Serial, restore); err != nil {
			return err
		}
		if err := s.applyMetadata(ctx, tx, incoming.Serial, incoming.Overrides); err != nil {
			return err
		}
		record := *incoming
		record.Owner, record.Reported = owner, reported
		if err := s.metadataStore().Write(ctx, tx, &record); err != nil {
			return err
		}
	}
	if full {
		rows, err := tx.QueryContext(ctx, "SELECT serial FROM device_metadata")
		if err != nil {
			return err
		}
		var missing []string
		for rows.Next() {
			var serial string
			if err := rows.Scan(&serial); err != nil {
				rows.Close()
				return err
			}
			if !seen[serial] {
				missing = append(missing, serial)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, serial := range missing {
			record, err := s.metadataStore().Load(ctx, tx, serial)
			if err != nil {
				return err
			}
			if err := s.applyMetadata(ctx, tx, serial, record.Reported); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM device_metadata WHERE serial=?", serial); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) ReportedDevice(_ context.Context, device *Device) (*Device, error) {
	copyDevice := *device
	if err := metadata.Apply(&copyDevice.Device, device.ReportedMetadata); err != nil {
		return nil, err
	}
	return &copyDevice, nil
}

func (s *SQLiteStore) protectMetadata(ctx context.Context, tx *sql.Tx, device *Device) error {
	record, err := s.metadataStore().Load(ctx, tx, device.Serial)
	if err != nil {
		return err
	}
	existing, err := s.getDevice(ctx, tx, device.Serial)
	if err != nil && err != ErrNotFound {
		return err
	}
	if existing != nil {
		preserved := make(map[string]interface{})
		values := metadata.Values(&existing.Device)
		for _, lock := range existing.LockedFields {
			field := strings.ToLower(lock.Field)
			if metadata.Known(field) {
				preserved[field] = values[field]
			}
		}
		if err := metadata.Apply(&device.Device, preserved); err != nil {
			return err
		}
	}
	// Other local writers may carry effective values read from the same row.
	values := metadata.Values(&device.Device)
	for field, override := range record.Overrides {
		valueJSON, err := json.Marshal(values[field])
		if err != nil {
			return err
		}
		overrideJSON, err := json.Marshal(override)
		if err != nil {
			return err
		}
		if string(valueJSON) == string(overrideJSON) {
			if reported, ok := record.Reported[field]; ok {
				if err := metadata.Apply(&device.Device, map[string]interface{}{field: reported}); err != nil {
					return err
				}
			}
		}
	}
	return s.metadataStore().Protect(ctx, tx, &device.Device, record.Owner)
}
