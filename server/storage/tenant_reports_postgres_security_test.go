//go:build integration

package storage

import "testing"

func TestPostgresReportsSecurity(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, store *PostgresStore) {
		checkReportsExactTenantMembership(t, store)
		checkReportRunSecurityOwnership(t, store)
		checkReportRunMetadataReads(t, store)
	})
}
