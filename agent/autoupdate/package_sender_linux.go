package autoupdate

import (
	"context"
	"crypto/sha256"
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

// WriteRepositoryPackageRequest owns and closes output, which must be a pipe.
// The caller retains source. Sending preserves its offset and grants no install
// authority: the root helper must independently authorize the received bytes.
func WriteRepositoryPackageRequest(parent context.Context, output, source *os.File, target updatetrust.Target) (result error) {
	if output == nil {
		return fmt.Errorf("native package output pipe is required")
	}
	defer func() { result = errors.Join(result, output.Close()) }()
	if source == nil || !validPrivilegedPackageTarget(target) {
		return fmt.Errorf("invalid native package send request")
	}
	info, err := output.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("native package output must be a pipe")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := output.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("bound native package send: %w", err)
	}
	interrupted := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { interrupted <- output.SetWriteDeadline(time.Now()) })
	defer func() {
		if !stop() {
			result = errors.Join(result, <-interrupted)
		}
		if ctx.Err() != nil {
			result = errors.Join(result, ctx.Err())
		} else if !time.Now().Before(deadline) {
			result = errors.Join(result, context.DeadlineExceeded)
		}
	}()
	actual, err := packageTargetFromFile(ctx, source, target)
	if err != nil {
		return err
	}
	if actual != target {
		return fmt.Errorf("%w: source differs from requested package", ErrPublisherTrust)
	}
	header, err := json.Marshal(RepositoryPackageRequest{Schema: 1, Target: target})
	if err != nil {
		return err
	}
	if len(header) > 4096 {
		return fmt.Errorf("native package request header exceeds limit")
	}
	if err := binary.Write(output, binary.BigEndian, uint32(len(header))); err != nil {
		return fmt.Errorf("send native package header length: %w", err)
	}
	if _, err := output.Write(header); err != nil {
		return fmt.Errorf("send native package header: %w", err)
	}
	hash := sha256.New()
	reader := &packageSnapshotReader{ctx: ctx, reader: io.NewSectionReader(source, 0, target.SizeBytes)}
	length, err := io.Copy(io.MultiWriter(output, hash), reader)
	if err != nil {
		return fmt.Errorf("send native package bytes: %w", err)
	}
	if length != target.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != target.SHA256 {
		return fmt.Errorf("%w: source changed during package transmission", ErrPublisherTrust)
	}
	return nil
}
