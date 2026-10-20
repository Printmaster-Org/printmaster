package autoupdate

import (
	"context"
	"fmt"
)

// RepositoryPackageRecovery describes package database state, not Agent health,
// publisher authorization or permission to retry/roll back the transaction.
type RepositoryPackageRecovery struct {
	Transaction      RepositoryPackageTransaction `json:"transaction"`
	InstalledVersion string                       `json:"installed_version"`
	Disposition      string                       `json:"disposition"`
}

// AssessPrivilegedRepositoryPackage inspects an interrupted run offline without
// installing, restarting, repairing, clearing or replaying anything. The state
// directory is supplied by trusted helper configuration.
func AssessPrivilegedRepositoryPackage(ctx context.Context, stateDir string) (RepositoryPackageRecovery, error) {
	var recovery RepositoryPackageRecovery
	err := withRootPackageState(stateDir, false, func() error {
		var err error
		recovery, err = assessPackageTransaction(ctx, stateDir, runPrivilegedPackageCommand)
		return err
	})
	if err != nil {
		return RepositoryPackageRecovery{}, err
	}
	return recovery, nil
}

func assessPackageTransaction(ctx context.Context, stateDir string, run repositoryCommand) (RepositoryPackageRecovery, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryPackageRecovery{}, err
	}
	transaction, err := readPackageTransaction(stateDir)
	if err != nil {
		return RepositoryPackageRecovery{}, err
	}
	version, err := queryNativePackageIdentity(ctx, transaction.Target, stateDir, "", run)
	if err != nil {
		return RepositoryPackageRecovery{}, fmt.Errorf("native package run %s requires owner inspection: %w", transaction.RunID, err)
	}
	if err := ctx.Err(); err != nil {
		return RepositoryPackageRecovery{}, err
	}
	disposition := "owner-repair-required"
	switch version {
	case transaction.Target.Version:
		disposition = "candidate-installed"
	case transaction.PreviousVersion:
		disposition = "previous-installed"
	}
	return RepositoryPackageRecovery{Transaction: transaction, InstalledVersion: version, Disposition: disposition}, nil
}
