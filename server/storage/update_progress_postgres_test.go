//go:build integration

package storage

import "testing"

func TestAgentUpdateProgressPostgres(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, s *PostgresStore) {
		testAgentUpdateProgress(t, &s.BaseStore)
	})
}
