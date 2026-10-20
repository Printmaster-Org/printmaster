package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"printmaster/common/updatetrust"
)

// WithPrivilegedRepositoryPackage is the root-side intake boundary, not an
// installation entrypoint. Root, metadataURL, stateDir and consume must come
// from trusted helper configuration/code, never the service-user request.
// Only source and target are untrusted inputs. The callback receives a private
// authenticated snapshot; it must finish consuming it before returning.
func WithPrivilegedRepositoryPackage(ctx context.Context, root []byte, metadataURL, stateDir string,
	source *os.File, target updatetrust.Target, consume func(string, updatetrust.Authorization) error) (result error) {
	if os.Geteuid() != 0 {
		return fmt.Errorf("repository package handoff requires root")
	}
	if target.Component != "agent" || target.Platform != "linux" || target.Channel != "stable" ||
		target.Arch != runtime.GOARCH || !stablePackageVersion.MatchString(target.Version) ||
		(target.Format != "deb" && target.Format != "rpm") || target.SizeBytes <= 0 ||
		target.SizeBytes > maxRepositoryPackageSize || source == nil || consume == nil {
		return fmt.Errorf("%w: invalid privileged package identity", ErrPublisherTrust)
	}
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || filepath.Dir(stateDir) == stateDir {
		return fmt.Errorf("privileged package state must be a dedicated absolute directory")
	}
	// Check existing ancestors before creating anything; never adopt, chmod or
	// repair service-owned trust state on behalf of an untrusted requester.
	if err := checkRootPackageAncestors(filepath.Dir(stateDir)); err != nil {
		return err
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create privileged package state: %w", err)
	}
	if err := checkRootPackageTree(stateDir); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(stateDir, "handoff.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open privileged package lock: %w", err)
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another privileged package handoff holds the lock: %w", err)
	}
	// Closing the descriptor releases the OS lock, including after process exit.
	// Keep metadata and its sibling integrity record exclusively root-owned.
	verifier, err := updatetrust.NewClient(updatetrust.Options{
		TrustedRoot: root, MetadataURL: metadataURL, CacheDir: filepath.Join(stateDir, "metadata"),
	})
	if err != nil {
		return fmt.Errorf("initialize independent privileged publisher trust: %w", err)
	}
	return withAuthenticatedPackage(ctx, stateDir, source, target, verifier, consume)
}

func checkRootPackageAncestors(path string) error {
	current := string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect privileged package ancestor: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			(info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("privileged package ancestor is not protected and root-owned: %s", current)
		}
	}
	return nil
}

func checkRootPackageTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect privileged package state: %w", walkErr)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 ||
			(info.IsDir() && info.Mode().Perm()&0o077 != 0) ||
			(!info.IsDir() && (!info.Mode().IsRegular() || stat.Nlink != 1 || info.Mode().Perm()&0o133 != 0)) {
			return fmt.Errorf("privileged package state is not private, ordinary and root-owned: %s", path)
		}
		return nil
	})
}
