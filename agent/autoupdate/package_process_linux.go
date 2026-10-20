package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"printmaster/common/updatetrust"
)

// LaunchRepositoryPackage supervises the fixed root helper through both its
// response and exit. It is staged for root callers only; no sudo grant is
// installed. Caller cancellation closes the exchange but does not kill an
// in-flight installer. This call still waits for independent supervision.
func LaunchRepositoryPackage(ctx context.Context, source *os.File, target updatetrust.Target) (RepositoryPackageOutcome, error) {
	if os.Geteuid() != 0 {
		return RepositoryPackageOutcome{}, fmt.Errorf("native helper launch requires root; service-user delegation is not enabled")
	}
	if err := checkRootPackageExecutable(RepositoryPackageHelperPath); err != nil {
		return RepositoryPackageOutcome{}, err
	}
	return launchRepositoryPackage(ctx, source, target, 16*time.Minute, func(budget context.Context) *exec.Cmd {
		cmd := exec.CommandContext(budget, RepositoryPackageHelperPath)
		cmd.Dir = "/"
		cmd.Env = packageCommandEnvironment()
		return cmd
	})
}

func launchRepositoryPackage(ctx context.Context, source *os.File, target updatetrust.Target,
	timeout time.Duration, command func(context.Context) *exec.Cmd) (RepositoryPackageOutcome, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryPackageOutcome{}, err
	}
	if source == nil || !validPrivilegedPackageTarget(target) {
		return RepositoryPackageOutcome{}, fmt.Errorf("native helper launch requires source and valid package identity")
	}
	// The helper's own fifteen-minute budget normally expires first. Only
	// this independent outer budget may terminate the child; never an Agent
	// shutdown context. Timeout is ambiguous and requires protected inspection.
	budget, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := command(budget)
	childInput, request, err := os.Pipe()
	if err != nil {
		return RepositoryPackageOutcome{}, err
	}
	response, childOutput, err := os.Pipe()
	if err != nil {
		return RepositoryPackageOutcome{}, errors.Join(err, childInput.Close(), request.Close())
	}
	cmd.Stdin, cmd.Stdout = childInput, childOutput
	diagnostics := &commandOutput{}
	cmd.Stderr = diagnostics
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return RepositoryPackageOutcome{}, errors.Join(fmt.Errorf("start native helper: %w", err),
			childInput.Close(), childOutput.Close(), request.Close(), response.Close())
	}
	closeErr := errors.Join(childInput.Close(), childOutput.Close())
	exchangeCtx, stopExchange := context.WithCancel(ctx)
	stopBudget := context.AfterFunc(budget, stopExchange)
	outcome, exchangeErr := ExchangeRepositoryPackage(exchangeCtx, request, response, source, target)
	stopBudget()
	stopExchange()
	exitErr := cmd.Wait()
	if err := errors.Join(closeErr, exchangeErr, exitErr, budget.Err(), ctx.Err()); err != nil {
		return outcome, fmt.Errorf("native helper did not complete cleanly; inspect protected state before retry: %w; diagnostics: %s",
			err, diagnostics.bytes())
	}
	return outcome, nil
}
