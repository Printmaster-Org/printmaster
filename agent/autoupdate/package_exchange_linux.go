package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"

	"printmaster/common/updatetrust"
)

// ExchangeRepositoryPackage owns request and response pipes or Unix streams,
// not source or the helper process. The caller must supervise the trusted helper
// separately; a valid reply alone cannot prove process success or service startup.
func ExchangeRepositoryPackage(parent context.Context, request, response, source *os.File,
	target updatetrust.Target) (RepositoryPackageOutcome, error) {
	if request == nil || response == nil {
		err := fmt.Errorf("native package exchange requires request and response streams")
		if request != nil {
			err = errors.Join(err, request.Close())
		}
		if response != nil {
			err = errors.Join(err, response.Close())
		}
		return RepositoryPackageOutcome{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	sent := make(chan error, 1)
	go func() {
		err := WriteRepositoryPackageRequest(ctx, request, source, target)
		if err != nil {
			cancel()
		}
		sent <- err
	}()
	outcome, readErr := ReadRepositoryPackageOutcome(ctx, response, target)
	if readErr != nil || outcome.Status != "awaiting-startup" {
		// Early configuration rejection may arrive while the sender is still
		// blocked. Cancel both directions rather than waiting for pipe capacity.
		cancel()
	}
	sendErr := <-sent
	if readErr != nil || sendErr != nil {
		return RepositoryPackageOutcome{}, fmt.Errorf("native package exchange incomplete; inspect protected state before retry: %w",
			errors.Join(readErr, sendErr))
	}
	if outcome.Status != "awaiting-startup" {
		return outcome, fmt.Errorf("native package helper reported %s; do not automatically retry", outcome.Status)
	}
	return outcome, nil
}
