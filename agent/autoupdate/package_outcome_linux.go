package autoupdate

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"printmaster/common/updatetrust"
)

// RepositoryPackageOutcome is a transport observation, not authority to retry
// or replace protected root journal evidence. It never claims startup success.
type RepositoryPackageOutcome struct {
	Schema int                 `json:"schema"`
	Status string              `json:"status"`
	RunID  string              `json:"run_id,omitempty"`
	Target *updatetrust.Target `json:"target,omitempty"`
}

// ServePrivilegedRepositoryPackageResponse owns both pipe handles. It emits a
// bounded reply even after operation cancellation, using a separate five-second
// write budget. The original error remains available to the helper's caller;
// raw command output, file paths and policy are not serialized to the Agent.
func ServePrivilegedRepositoryPackageResponse(ctx context.Context, root []byte, metadataURL, stateDir, policyPath string,
	input, output *os.File) (result error) {
	return serveRepositoryPackageResponse(ctx, root, metadataURL, stateDir, policyPath, input, output, runPrivilegedPackageCommand)
}

func serveRepositoryPackageResponse(ctx context.Context, root []byte, metadataURL, stateDir, policyPath string,
	input, output *os.File, run repositoryCommand) (result error) {
	if output == nil {
		if input != nil {
			return errors.Join(fmt.Errorf("native package response pipe is required"), input.Close())
		}
		return fmt.Errorf("native package response pipe is required")
	}
	defer func() { result = errors.Join(result, output.Close()) }()
	info, pipeErr := output.Stat()
	if pipeErr != nil || info.Mode()&os.ModeNamedPipe == 0 {
		failure := errors.Join(fmt.Errorf("native package response requires a pipe"), pipeErr)
		if input != nil {
			failure = errors.Join(failure, input.Close())
		}
		return failure
	}
	outcome := RepositoryPackageOutcome{Schema: 1, Status: "rejected"}
	err := serveRepositoryPackage(ctx, root, metadataURL, stateDir, policyPath, input, &outcome, run)
	if err != nil && outcome.Status == "awaiting-startup" {
		// A close/cleanup/lock failure can follow native completion. Do not
		// conceal it behind a success-shaped transport reply.
		outcome.Status = "inspection-required"
	}
	replyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(err, writeRepositoryPackageOutcome(replyCtx, output, outcome))
}

func writeRepositoryPackageOutcome(ctx context.Context, output *os.File, outcome RepositoryPackageOutcome) (result error) {
	pipe, err := pollablePackagePipe(output)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, pipe.Close()) }()
	output = pipe
	finish, err := boundPackageOutcomePipe(ctx, output, true)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, finish()) }()
	if err := validateRepositoryPackageOutcome(outcome); err != nil {
		return err
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return fmt.Errorf("native package outcome exceeds frame limit")
	}
	if err := binary.Write(output, binary.BigEndian, uint32(len(data))); err != nil {
		return fmt.Errorf("write native outcome length: %w", err)
	}
	if _, err := output.Write(data); err != nil {
		return fmt.Errorf("write native outcome: %w", err)
	}
	return nil
}

// ReadRepositoryPackageOutcome owns input. The exact requested target must
// match for actionable replies. Rejections may lack identity when parsing or
// configuration failed before receipt; they are never success or retry grants.
func ReadRepositoryPackageOutcome(parent context.Context, input *os.File, expected updatetrust.Target) (outcome RepositoryPackageOutcome, result error) {
	if input == nil {
		return outcome, fmt.Errorf("native package outcome pipe is required")
	}
	defer func(file *os.File) {
		result = errors.Join(result, file.Close())
		if result != nil {
			outcome = RepositoryPackageOutcome{}
		}
	}(input)
	if !validPrivilegedPackageTarget(expected) {
		return outcome, fmt.Errorf("invalid expected native package outcome target")
	}
	pipe, err := pollablePackagePipe(input)
	if err != nil {
		return outcome, err
	}
	defer func() { result = errors.Join(result, pipe.Close()) }()
	input = pipe
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	finish, err := boundPackageOutcomePipe(ctx, input, false)
	if err != nil {
		return outcome, err
	}
	defer func() {
		result = errors.Join(result, finish())
		if result != nil {
			outcome = RepositoryPackageOutcome{}
		}
	}()
	var length uint32
	if err := binary.Read(input, binary.BigEndian, &length); err != nil {
		return outcome, fmt.Errorf("read native outcome length: %w", err)
	}
	if length == 0 || length > 4096 {
		return outcome, fmt.Errorf("native package outcome length is invalid")
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(input, data); err != nil {
		return outcome, fmt.Errorf("read complete native outcome: %w", err)
	}
	if err := json.Unmarshal(data, &outcome); err != nil {
		return RepositoryPackageOutcome{}, fmt.Errorf("decode native outcome: %w", err)
	}
	canonical, err := json.Marshal(outcome)
	if err != nil || !bytes.Equal(data, canonical) {
		return RepositoryPackageOutcome{}, fmt.Errorf("native package outcome is not canonical")
	}
	if err := validateRepositoryPackageOutcome(outcome); err != nil {
		return RepositoryPackageOutcome{}, err
	}
	if outcome.Target != nil && *outcome.Target != expected {
		return RepositoryPackageOutcome{}, fmt.Errorf("native package outcome target differs from request")
	}
	return outcome, nil
}

func validateRepositoryPackageOutcome(outcome RepositoryPackageOutcome) error {
	if outcome.Schema != 1 || outcome.Target != nil && !validPrivilegedPackageTarget(*outcome.Target) {
		return fmt.Errorf("native package outcome identity/schema is invalid")
	}
	if outcome.RunID != "" {
		id, err := hex.DecodeString(outcome.RunID)
		if err != nil || len(id) != 16 || hex.EncodeToString(id) != outcome.RunID {
			return fmt.Errorf("native package outcome run identity is invalid")
		}
	}
	switch outcome.Status {
	case "rejected":
		if outcome.RunID != "" {
			return fmt.Errorf("rejected native package outcome contains run authority")
		}
	case "inspection-required":
		if outcome.Target == nil {
			return fmt.Errorf("native package inspection outcome lacks target")
		}
	case "awaiting-startup":
		if outcome.Target == nil || outcome.RunID == "" {
			return fmt.Errorf("native package completion outcome lacks target/run")
		}
	default:
		return fmt.Errorf("unknown native package outcome status")
	}
	return nil
}

func boundPackageOutcomePipe(ctx context.Context, file *os.File, write bool) (func() error, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return nil, fmt.Errorf("native package outcome requires a pipe")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, fmt.Errorf("native package outcome requires a deadline")
	}
	setDeadline := file.SetReadDeadline
	if write {
		setDeadline = file.SetWriteDeadline
	}
	if err := setDeadline(deadline); err != nil {
		return nil, err
	}
	interrupted := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { interrupted <- setDeadline(time.Now()) })
	return func() error {
		var err error
		if !stop() {
			err = <-interrupted
		}
		err = errors.Join(err, setDeadline(time.Time{}))
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		} else if !time.Now().Before(deadline) {
			err = errors.Join(err, context.DeadlineExceeded)
		}
		return err
	}, nil
}
