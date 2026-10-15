//go:build integration

package storage

import "testing"

func TestDeviceMetadataPostgres(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, s *PostgresStore) {
		testDeviceMetadata(t, &s.BaseStore)
	})
}
