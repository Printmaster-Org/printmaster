package autoupdate

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

// RepositoryPackageRequest is the complete unprivileged request schema.
// It carries no paths, policy, commands, trust roots or metadata authority.
type RepositoryPackageRequest struct {
	Schema int                `json:"schema"`
	Target updatetrust.Target `json:"target"`
}

// ServePrivilegedRepositoryPackage owns and closes input, which must be a pipe.
// All other arguments are trusted helper configuration, not request fields.
// The wire format is a four-byte big-endian header length, canonical JSON
// header, and exactly Target.SizeBytes package bytes. One process consumes one
// frame; suffix bytes are never interpreted as commands or another request.
// No executable entrypoint or sudo permission is installed by this API.
func ServePrivilegedRepositoryPackage(ctx context.Context, root []byte, metadataURL, stateDir, policyPath string, input *os.File) (result error) {
	return serveRepositoryPackage(ctx, root, metadataURL, stateDir, policyPath, input, nil, runPrivilegedPackageCommand)
}

func serveRepositoryPackage(ctx context.Context, root []byte, metadataURL, stateDir, policyPath string, input *os.File,
	outcome *RepositoryPackageOutcome, run repositoryCommand) (result error) {
	if input == nil {
		return fmt.Errorf("native package request pipe is required")
	}
	// Take ownership even when configuration or privileges reject the request.
	defer func() { result = errors.Join(result, input.Close()) }()
	if os.Geteuid() != 0 {
		return fmt.Errorf("native package request handling requires root")
	}
	return withRootPackageState(stateDir, true, func() error {
		policy := func() (updatepolicy.PolicySpec, error) { return readRootPackagePolicy(policyPath) }
		// Reject disabled/missing configuration before accepting package bytes.
		// The consumer rereads policy at the final installation boundary.
		if _, err := policy(); err != nil {
			return err
		}
		return receiveRepositoryPackage(ctx, input, stateDir, func(source *os.File, target updatetrust.Target) error {
			if outcome != nil {
				outcome.Target = &target
			}
			return authenticateRootPackage(ctx, root, metadataURL, stateDir, source, target, nativePackageConsumerOutcome(ctx, stateDir, policy, outcome, run))
		})
	})
}

func receiveRepositoryPackage(parent context.Context, input *os.File, stateDir string,
	consume func(*os.File, updatetrust.Target) error) (result error) {
	pipe, err := pollablePackagePipe(input)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, pipe.Close()) }()
	input = pipe
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	// Pipes and Unix streams support read deadlines; reject other handles instead of
	// allowing an untrusted writer to hold the root intake lock indefinitely.
	deadline, _ := ctx.Deadline()
	if err := input.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("bound native request pipe: %w", err)
	}
	interrupted := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() {
		interrupted <- input.SetReadDeadline(time.Now())
	})
	finished := false
	finish := func() error {
		finished = true
		var finishErr error
		if !stop() {
			finishErr = errors.Join(finishErr, <-interrupted)
		}
		if err := input.SetReadDeadline(time.Time{}); err != nil {
			finishErr = errors.Join(finishErr, fmt.Errorf("clear native request deadline: %w", err))
		}
		if ctx.Err() != nil {
			finishErr = errors.Join(finishErr, ctx.Err())
		} else if !time.Now().Before(deadline) {
			finishErr = errors.Join(finishErr, context.DeadlineExceeded)
		}
		return finishErr
	}
	defer func() {
		if !finished {
			result = errors.Join(result, finish())
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	var length uint32
	if err := binary.Read(input, binary.BigEndian, &length); err != nil {
		return fmt.Errorf("read native request header length: %w", err)
	}
	if length == 0 || length > 4096 {
		return fmt.Errorf("native request header must be between 1 and 4096 bytes")
	}
	header := make([]byte, int(length))
	if _, err := io.ReadFull(input, header); err != nil {
		return fmt.Errorf("read native request header: %w", err)
	}
	request, err := decodeRepositoryPackageRequest(header)
	if err != nil {
		return err
	}
	source, err := os.CreateTemp(stateDir, ".native-request-*")
	if err != nil {
		return fmt.Errorf("create private native request input: %w", err)
	}
	defer func() {
		result = errors.Join(result, source.Close())
		if err := os.Remove(source.Name()); err != nil {
			result = errors.Join(result, fmt.Errorf("remove native request input: %w", err))
		}
	}()
	if _, err := io.CopyN(source, input, request.Target.SizeBytes); err != nil {
		return fmt.Errorf("receive complete native package: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := finish(); err != nil {
		return err
	}
	cancel()
	if err := parent.Err(); err != nil {
		return err
	}
	// Authorization uses section reads, so neither a caller-controlled offset
	// nor a service-user pathname is passed into the privileged consumer.
	return consume(source, request.Target)
}

func decodeRepositoryPackageRequest(header []byte) (RepositoryPackageRequest, error) {
	var request RepositoryPackageRequest
	if err := json.Unmarshal(header, &request); err != nil {
		return request, fmt.Errorf("decode native package request: %w", err)
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(header, canonical) || request.Schema != 1 || !validPrivilegedPackageTarget(request.Target) {
		return RepositoryPackageRequest{}, fmt.Errorf("native package request has invalid schema, identity or noncanonical fields")
	}
	return request, nil
}
