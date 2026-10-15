package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"printmaster/common/metadata"
)

func (s *BaseStore) metadataStore() metadata.Store {
	return metadata.Store{DB: s.db, Query: s.query}
}

func (s *BaseStore) InitDeviceMetadata() error {
	return s.metadataStore().Init()
}

func (s *BaseStore) lockDeviceMetadata(ctx context.Context, tx *sql.Tx, serial string) error {
	if s.dialect.Name() == "postgres" {
		_, err := tx.ExecContext(ctx, s.query("SELECT pg_advisory_xact_lock(hashtext(?))"), "device-metadata:"+serial)
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE devices SET serial=serial WHERE serial=?", serial)
	return err
}

func (s *BaseStore) EditDeviceMetadata(ctx context.Context, serial, owner string, patch map[string]interface{}, unpin string, fromAgent bool) (*metadata.Record, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.lockDeviceMetadata(ctx, tx, serial); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, s.query("UPDATE devices SET serial=serial WHERE serial=? AND agent_id=?"), serial, owner)
	if err != nil {
		return nil, err
	}
	if err := requireMachineWrite(result, "device", serial); err != nil {
		return nil, err
	}
	device, err := s.getDevice(ctx, tx, serial)
	if err != nil {
		return nil, err
	}
	store := s.metadataStore()
	record, err := store.Load(ctx, tx, serial)
	if err != nil {
		return nil, err
	}
	if fromAgent {
		for field := range patch {
			if _, pinned := record.Overrides[field]; pinned {
				return nil, fmt.Errorf("%w: %s", metadata.ErrPinned, field)
			}
		}
	}
	if unpin != "" {
		if !metadata.Known(unpin) {
			return nil, errors.New("unsupported metadata field")
		}
		if value, ok := record.Reported[unpin]; ok {
			if err := metadata.Apply(&device.Device, map[string]interface{}{unpin: value}); err != nil {
				return nil, err
			}
		}
		delete(record.Overrides, unpin)
		delete(record.Reported, unpin)
	} else {
		current := metadata.Values(&device.Device)
		for field, value := range patch {
			if _, pinned := record.Overrides[field]; !pinned {
				record.Reported[field] = current[field]
			}
			record.Overrides[field] = value
		}
		if err := metadata.Apply(&device.Device, patch); err != nil {
			return nil, err
		}
	}
	record.Owner, record.Version = owner, record.Version+1
	if err := store.Write(ctx, tx, record); err != nil {
		return nil, err
	}
	device.MetadataOverrides = record.Overrides
	if err := s.upsertDevice(ctx, tx, device); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *BaseStore) DeviceMetadataForAgent(ctx context.Context, owner string) ([]*metadata.Record, error) {
	rows, err := s.queryContext(ctx, "SELECT m.serial,m.version,m.overrides FROM device_metadata m JOIN devices d ON d.serial=m.serial WHERE m.owner=? AND d.agent_id=? ORDER BY m.serial", owner, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]*metadata.Record, 0)
	for rows.Next() {
		record := &metadata.Record{Owner: owner}
		var overrides string
		if err := rows.Scan(&record.Serial, &record.Version, &overrides); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(overrides), &record.Overrides); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
