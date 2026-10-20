package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"printmaster/common/updatetrust"
)

// withAuthenticatedPackage copies an open input into a private snapshot before
// authenticating it. The consumer sees only that snapshot, never the mutable
// repository path. A privileged caller must first protect the parent directory
// and supply its own verifier backed by independently protected trust state.
func withAuthenticatedPackage(ctx context.Context, parent string, source *os.File, target updatetrust.Target,
	verifier packageFileVerifier, consume func(string, updatetrust.Authorization) error) (result error) {
	if source == nil || verifier == nil || consume == nil || target.SizeBytes <= 0 ||
		target.SizeBytes > maxRepositoryPackageSize || (target.Format != "deb" && target.Format != "rpm") {
		return fmt.Errorf("%w: invalid package snapshot request", ErrPublisherTrust)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("inspect package input: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != target.SizeBytes {
		return fmt.Errorf("%w: package input is not a regular file of the expected length", ErrPublisherTrust)
	}
	dir, err := os.MkdirTemp(parent, "package-snapshot-")
	if err != nil {
		return fmt.Errorf("create private package snapshot: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			result = errors.Join(result, fmt.Errorf("remove package snapshot: %w", err))
		}
	}()
	path := filepath.Join(dir, "printmaster-agent."+target.Format)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create snapshot file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close package snapshot: %w", err))
		}
	}()
	reader := &packageSnapshotReader{ctx: ctx, reader: io.NewSectionReader(source, 0, target.SizeBytes)}
	if length, err := io.Copy(file, reader); err != nil || length != target.SizeBytes {
		return fmt.Errorf("copy complete package snapshot (%d/%d bytes): %w", length, target.SizeBytes, errors.Join(err, io.ErrUnexpectedEOF))
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync package snapshot: %w", err)
	}
	if err := file.Chmod(0o400); err != nil {
		return fmt.Errorf("protect package snapshot: %w", err)
	}
	authorization, err := verifier.AuthorizeFile(ctx, target, file)
	if err != nil {
		return fmt.Errorf("%w: authenticate private package snapshot: %w", ErrPublisherTrust, err)
	}
	if authorization.Target != target {
		return fmt.Errorf("%w: verifier returned another snapshot identity", ErrPublisherTrust)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := consume(path, authorization); err != nil {
		return fmt.Errorf("consume authenticated package snapshot: %w", err)
	}
	return nil
}

const maxRepositoryPackageSize = 512 * 1024 * 1024

// packageTargetFromFile derives a candidate, not authority: TUF must still
// authenticate this digest using the independently trusted publisher root.
func packageTargetFromFile(ctx context.Context, file *os.File, target updatetrust.Target) (updatetrust.Target, error) {
	info, err := file.Stat()
	if err != nil {
		return updatetrust.Target{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxRepositoryPackageSize {
		return updatetrust.Target{}, fmt.Errorf("%w: package must be a nonempty regular file no larger than 512 MiB", ErrPublisherTrust)
	}
	hash := sha256.New()
	reader := &packageSnapshotReader{ctx: ctx, reader: io.NewSectionReader(file, 0, info.Size())}
	if _, err := io.Copy(hash, reader); err != nil {
		return updatetrust.Target{}, fmt.Errorf("hash repository package: %w", err)
	}
	target.SHA256, target.SizeBytes = hex.EncodeToString(hash.Sum(nil)), info.Size()
	return target, nil
}

type packageSnapshotReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *packageSnapshotReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
