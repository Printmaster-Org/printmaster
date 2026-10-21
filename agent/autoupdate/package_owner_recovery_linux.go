package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// This separate administrator command has no service-user delegation. It never
// installs packages, restarts services or resets installing/ambiguous evidence.
func RunRepositoryPackageRecovery(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 1 && args[0] == "inspect" {
		recovery, err := AssessPrivilegedRepositoryPackage(ctx, packageHelperStateDir)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(recovery)
	}
	if len(args) != 2 || args[0] != "confirm-repair" {
		return fmt.Errorf("usage: printmaster-agent-update-recovery inspect | confirm-repair ROOT_RUN_ID (root only)")
	}
	return withRootPackageState(packageHelperStateDir, false, func() error {
		transaction, err := confirmOwnerPackageRepair(ctx, packageHelperStateDir, args[1], runPrivilegedPackageCommand,
			func(pid int) error { return verifyNativeServiceExecutable(pid, "/usr/bin/printmaster-agent") })
		if err != nil {
			return err
		}
		if err := writeRepositoryPackageObservation(packageHelperResultPath, transaction); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(transaction)
	})
}

func confirmOwnerPackageRepair(ctx context.Context, stateDir, runID string, run repositoryCommand,
	verifyProcess func(int) error) (RepositoryPackageTransaction, error) {
	transaction, err := readPackageTransaction(stateDir)
	if err != nil {
		return transaction, err
	}
	if runID == "" || transaction.RunID != runID ||
		(transaction.Phase != "recovery-required" && transaction.Phase != "owner-repaired") {
		return transaction, fmt.Errorf("owner confirmation requires the exact recovery-required run; installing/ambiguous evidence cannot be reset")
	}
	if transaction.Phase == "owner-repaired" {
		return transaction, archivePackageTransaction(stateDir, transaction)
	}
	version, err := queryNativePackageIdentity(ctx, transaction.Target, stateDir, "", run)
	if err != nil {
		return transaction, err
	}
	if version != transaction.PreviousVersion && version != transaction.Target.Version {
		return transaction, fmt.Errorf("repaired package is neither the recorded previous nor candidate version")
	}
	first, err := queryNativeService(ctx, stateDir, run)
	if err != nil {
		return transaction, err
	}
	if err := verifyProcess(first.PID); err != nil {
		return transaction, err
	}
	second, err := queryNativeService(ctx, stateDir, run)
	if err != nil || first != second {
		return transaction, errors.Join(fmt.Errorf("repaired Agent invocation is not stable"), err)
	}
	installed, err := queryNativePackageIdentity(ctx, transaction.Target, stateDir, "", run)
	if err != nil || installed != version {
		return transaction, errors.Join(fmt.Errorf("repaired package changed during confirmation"), err)
	}
	if err := verifyProcess(second.PID); err != nil {
		return transaction, err
	}
	if err := ctx.Err(); err != nil {
		return transaction, err
	}
	transaction.Phase, transaction.Startup = "owner-repaired", &second
	transaction.RepairedVersion, transaction.UpdatedAt = version, time.Now().UTC()
	if transaction.UpdatedAt.Before(transaction.StartedAt) {
		return transaction, fmt.Errorf("clock precedes native installation intent")
	}
	data, err := json.Marshal(transaction)
	if err != nil {
		return transaction, err
	}
	if err := writePackageTransaction(transaction.path, data); err != nil {
		return transaction, err
	}
	return transaction, archivePackageTransaction(stateDir, transaction)
}
