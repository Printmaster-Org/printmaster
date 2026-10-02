//go:build integration

package storage

import "testing"

func TestPostgresSecurityReview(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, s *PostgresStore) {
		checkReviewOneTimeJoinToken(t, s)
		checkReviewOwnedDeletion(t, s)
	})
}
