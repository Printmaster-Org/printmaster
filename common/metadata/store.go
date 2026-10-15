package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"printmaster/common/storage"
)

type Store struct {
	DB    *sql.DB
	Query func(string) string
}

type Reader interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func (s Store) Init() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS device_metadata (
		serial TEXT PRIMARY KEY REFERENCES devices(serial) ON DELETE CASCADE,
		owner TEXT NOT NULL, version BIGINT NOT NULL, overrides TEXT NOT NULL, reported TEXT NOT NULL
	)`)
	return err
}

func (s Store) Load(ctx context.Context, r Reader, serial string) (*Record, error) {
	record := &Record{Serial: serial, Overrides: map[string]interface{}{}, Reported: map[string]interface{}{}}
	var overrides, reported string
	err := r.QueryRowContext(ctx, s.Query("SELECT owner,version,overrides,reported FROM device_metadata WHERE serial=?"), serial).
		Scan(&record.Owner, &record.Version, &overrides, &reported)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(overrides), &record.Overrides); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(reported), &record.Reported); err != nil {
		return nil, err
	}
	return record, nil
}

func (s Store) Write(ctx context.Context, tx *sql.Tx, record *Record) error {
	overrides, err := json.Marshal(record.Overrides)
	if err != nil {
		return err
	}
	reported, err := json.Marshal(record.Reported)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.Query(`INSERT INTO device_metadata(serial,owner,version,overrides,reported) VALUES(?,?,?,?,?)
		ON CONFLICT(serial) DO UPDATE SET owner=excluded.owner,version=excluded.version,overrides=excluded.overrides,reported=excluded.reported`),
		record.Serial, record.Owner, record.Version, string(overrides), string(reported))
	return err
}

// Protect runs under the caller's device-row write reservation.
func (s Store) Protect(ctx context.Context, tx *sql.Tx, d *storage.Device, owner string) error {
	record, err := s.Load(ctx, tx, d.Serial)
	if err != nil {
		return err
	}
	if record.Version == 0 || record.Owner != owner {
		return nil
	}
	values := Values(d)
	for field := range record.Overrides {
		record.Reported[field] = values[field]
	}
	if err := s.Write(ctx, tx, record); err != nil {
		return err
	}
	return Apply(d, record.Overrides)
}
