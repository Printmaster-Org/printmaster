//go:build integration

package storage

import "testing"

func TestPostgresMachineOwnership(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, s *PostgresStore) {
		checkMachineOwnership(t, s)
		checkPendingReviewTransitions(t, s)
	})
}
