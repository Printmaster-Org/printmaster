//go:build integration

package storage

import "testing"

func TestPostgresReportsSecurity(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, store *PostgresStore) {
		checkReportScopedQueries(t, store)
		checkReportsExactTenantMembership(t, store)
		checkReportRunSecurityOwnership(t, store)
		checkReportRunMetadataReads(t, store)
	})
}
