package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The root service, not the Agent process or its connection, owns restart and
// reconciliation. Never retry installation or start a partially configured
// package after a native failure.
func completeRepositoryPackageService(parent context.Context, stateDir string, outcome *RepositoryPackageOutcome,
	run repositoryCommand, verifyProcess func(int) error) error {
	if outcome.Status != "awaiting-startup" || outcome.RunID == "" {
		return fmt.Errorf("native service completion requires an awaiting-startup run")
	}
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	output, err := run(ctx, 30*time.Second, stateDir, nil, "/usr/bin/systemctl", "--system", "--no-pager",
		"restart", "--", "printmaster-agent.service")
	if err != nil {
		return failRepositoryPackageStartup(stateDir, outcome.RunID,
			fmt.Errorf("restart Agent after native installation: %w: %s", err, output))
	}
	var lastErr error
	for {
		var transaction RepositoryPackageTransaction
		err := withRootPackageState(stateDir, false, func() error {
			var err error
			transaction, err = reconcilePackageStartup(ctx, stateDir, outcome.RunID, run, verifyProcess)
			return err
		})
		if err == nil {
			if outcome.Target == nil || transaction.Target != *outcome.Target {
				return fmt.Errorf("native startup confirmation target changed")
			}
			if stateDir == packageHelperStateDir {
				if err := writeRepositoryPackageObservation(packageHelperResultPath, transaction); err != nil {
					return err
				}
			}
			outcome.Status = "startup-confirmed"
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return failRepositoryPackageStartup(stateDir, outcome.RunID, errors.Join(ctx.Err(), lastErr))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func failRepositoryPackageStartup(stateDir, runID string, failure error) error {
	recordErr := withRootPackageState(stateDir, false, func() error {
		transaction, err := readPackageTransaction(stateDir)
		if err != nil {
			return err
		}
		if transaction.RunID != runID || transaction.Phase != "awaiting-startup" {
			return fmt.Errorf("cannot record startup failure over different native evidence")
		}
		transaction.Phase = "recovery-required"
		transaction.UpdatedAt = time.Now().UTC()
		if transaction.UpdatedAt.Before(transaction.StartedAt) {
			return fmt.Errorf("clock precedes native installation intent")
		}
		data, err := json.Marshal(transaction)
		if err != nil {
			return err
		}
		if err := writePackageTransaction(transaction.path, data); err != nil {
			return err
		}
		if stateDir == packageHelperStateDir {
			return writeRepositoryPackageObservation(packageHelperResultPath, transaction)
		}
		return nil
	})
	return errors.Join(failure, recordErr)
}
