package autoupdate

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RepositoryPackageStartup is point-in-time systemd/process evidence, not an
// application health check or a guarantee that the service will stay running.
type RepositoryPackageStartup struct {
	PID          int    `json:"pid"`
	InvocationID string `json:"invocation_id"`
}

func (s RepositoryPackageStartup) valid() bool {
	id, err := hex.DecodeString(s.InvocationID)
	return s.PID > 1 && err == nil && len(id) == 16 && hex.EncodeToString(id) == s.InvocationID &&
		s.InvocationID != strings.Repeat("0", 32)
}

// ReconcilePrivilegedRepositoryPackage confirms only an awaiting-startup run
// explicitly named by the caller. Failed/indeterminate installs require owner
// repair, not this success path. Configuration must be root-controlled.
func ReconcilePrivilegedRepositoryPackage(ctx context.Context, stateDir, runID string) (RepositoryPackageTransaction, error) {
	var transaction RepositoryPackageTransaction
	err := withRootPackageState(stateDir, false, func() error {
		var err error
		transaction, err = reconcilePackageStartup(ctx, stateDir, runID, runPrivilegedPackageCommand,
			func(pid int) error { return verifyNativeServiceExecutable(pid, "/usr/bin/printmaster-agent") })
		return err
	})
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	return transaction, nil
}

func reconcilePackageStartup(ctx context.Context, stateDir, runID string, run repositoryCommand, verifyProcess func(int) error) (RepositoryPackageTransaction, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryPackageTransaction{}, err
	}
	transaction, err := readPackageTransaction(stateDir)
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	if runID == "" || runID != transaction.RunID {
		return RepositoryPackageTransaction{}, fmt.Errorf("native reconciliation run identifier does not match protected evidence")
	}
	if transaction.Phase == "startup-confirmed" {
		// Resume an interrupted evidence archive, never rerun the installer.
		return transaction, archivePackageTransaction(stateDir, transaction)
	}
	if transaction.Phase != "awaiting-startup" {
		return RepositoryPackageTransaction{}, fmt.Errorf("native package run %s is %s; owner repair/inspection required", runID, transaction.Phase)
	}
	version, err := queryNativePackageIdentity(ctx, transaction.Target, stateDir, "", run)
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	if version != transaction.Target.Version {
		return RepositoryPackageTransaction{}, fmt.Errorf("native package run %s has installed version %s, expected %s", runID, version, transaction.Target.Version)
	}
	first, err := queryNativeService(ctx, stateDir, run)
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	if err := verifyProcess(first.PID); err != nil {
		return RepositoryPackageTransaction{}, fmt.Errorf("running Agent is not the protected installed executable: %w", err)
	}
	second, err := queryNativeService(ctx, stateDir, run)
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	if first != second {
		return RepositoryPackageTransaction{}, fmt.Errorf("agent service invocation changed during native reconciliation")
	}
	version, err = queryNativePackageIdentity(ctx, transaction.Target, stateDir, "", run)
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	if version != transaction.Target.Version {
		return RepositoryPackageTransaction{}, fmt.Errorf("installed package changed during native reconciliation")
	}
	if err := verifyProcess(second.PID); err != nil {
		return RepositoryPackageTransaction{}, fmt.Errorf("agent executable changed during native reconciliation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return RepositoryPackageTransaction{}, err
	}
	transaction.Phase, transaction.Startup, transaction.UpdatedAt = "startup-confirmed", &second, time.Now().UTC()
	if transaction.UpdatedAt.Before(transaction.StartedAt) {
		return RepositoryPackageTransaction{}, fmt.Errorf("local clock precedes native installation intent")
	}
	data, err := json.Marshal(transaction)
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	if err := writePackageTransaction(transaction.path, data); err != nil {
		return RepositoryPackageTransaction{}, fmt.Errorf("persist native startup confirmation: %w", err)
	}
	return transaction, archivePackageTransaction(stateDir, transaction)
}

func queryNativeService(ctx context.Context, dir string, run repositoryCommand) (RepositoryPackageStartup, error) {
	output, err := run(ctx, 30*time.Second, dir, nil, "/usr/bin/systemctl", "--system", "--no-pager", "show",
		"--property=Id,LoadState,ActiveState,SubState,MainPID,InvocationID,NeedDaemonReload", "--", "printmaster-agent.service")
	if err != nil {
		return RepositoryPackageStartup{}, fmt.Errorf("inspect Agent systemd service: %w: %s", err, output)
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			return RepositoryPackageStartup{}, fmt.Errorf("malformed Agent systemd service evidence")
		}
		if _, duplicate := fields[key]; duplicate {
			return RepositoryPackageStartup{}, fmt.Errorf("duplicate Agent systemd service property %q", key)
		}
		fields[key] = value
	}
	if len(fields) != 7 || fields["Id"] != "printmaster-agent.service" || fields["LoadState"] != "loaded" ||
		fields["ActiveState"] != "active" || fields["SubState"] != "running" || fields["NeedDaemonReload"] != "no" {
		return RepositoryPackageStartup{}, fmt.Errorf("agent service is not a loaded, active running unit with current configuration")
	}
	pid, err := strconv.Atoi(fields["MainPID"])
	startup := RepositoryPackageStartup{PID: pid, InvocationID: fields["InvocationID"]}
	if err != nil || !startup.valid() {
		return RepositoryPackageStartup{}, fmt.Errorf("agent service has invalid process/invocation evidence")
	}
	return startup, nil
}

func verifyNativeServiceExecutable(pid int, installed string) error {
	if pid <= 1 {
		return fmt.Errorf("invalid Agent service PID")
	}
	if err := checkRootPackageAncestors(filepath.Dir(installed)); err != nil {
		return err
	}
	if err := validateInstallPath(installed); err != nil {
		return err
	}
	actual, err := os.Stat(installed)
	if err != nil {
		return err
	}
	// Follow only the kernel's procfs executable link, never a request pathname.
	running, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return err
	}
	if !os.SameFile(actual, running) {
		return fmt.Errorf("agent MainPID is running a different or replaced executable")
	}
	return nil
}

func archivePackageTransaction(stateDir string, transaction RepositoryPackageTransaction) (result error) {
	history := filepath.Join(stateDir, "transactions")
	if err := os.Mkdir(history, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create native transaction history: %w", err)
	}
	if err := checkRootPackageTree(history); err != nil {
		return err
	}
	path := filepath.Join(history, transaction.RunID+".json")
	data, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		existing, err := readPackageTransactionFile(path)
		if err != nil {
			return fmt.Errorf("inspect existing native transaction archive: %w", err)
		}
		previous, err := json.Marshal(existing)
		if err != nil || !bytes.Equal(previous, data) {
			return fmt.Errorf("native transaction archive conflicts with active evidence")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := writePackageTransaction(path, data); err != nil {
		return fmt.Errorf("persist native transaction archive: %w", err)
	}
	if err := syncPackageDirectory(history); err != nil {
		return fmt.Errorf("sync native transaction history: %w", err)
	}
	// Sync the state directory too: it owns the newly created history directory.
	dir, err := os.Open(stateDir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, dir.Close()) }()
	if err := dir.Sync(); err != nil {
		return err
	}
	if err := os.Remove(transaction.path); err != nil {
		return fmt.Errorf("retire archived native transaction: %w", err)
	}
	return dir.Sync()
}

func syncPackageDirectory(path string) (result error) {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, dir.Close()) }()
	return dir.Sync()
}
