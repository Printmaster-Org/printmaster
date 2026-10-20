package autoupdate

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

// InstallPrivilegedRepositoryPackage is an in-process native installer for a
// trusted helper, not an externally callable CLI. All configuration and policy
// sources must be root-controlled. It does not resolve missing dependencies or
// restart/reconcile the Agent; the automatic update entrypoint stays disabled.
// A nil error means only native database validation, not Agent startup success.
func InstallPrivilegedRepositoryPackage(ctx context.Context, root []byte, metadataURL, stateDir string,
	source *os.File, target updatetrust.Target, policy func() (updatepolicy.PolicySpec, error)) error {
	return WithPrivilegedRepositoryPackage(ctx, root, metadataURL, stateDir, source, target,
		nativePackageConsumer(ctx, stateDir, policy))
}

func nativePackageConsumer(ctx context.Context, stateDir string, policy func() (updatepolicy.PolicySpec, error)) func(string, updatetrust.Authorization) error {
	return nativePackageConsumerOutcome(ctx, stateDir, policy, nil)
}

func nativePackageConsumerOutcome(ctx context.Context, stateDir string, policy func() (updatepolicy.PolicySpec, error),
	outcome *RepositoryPackageOutcome) func(string, updatetrust.Authorization) error {
	return func(snapshot string, authorization updatetrust.Authorization) error {
		if outcome != nil {
			// Once a protected consumer is entered, any failure (including a
			// possibly visible journal write) requires inspection, not replay.
			outcome.Status = "inspection-required"
		}
		journal, err := newPackageTransaction(stateDir, authorization.Target)
		if err != nil {
			return err
		}
		if outcome != nil {
			outcome.RunID = journal.RunID
		}
		if err := consumeNativePackage(ctx, snapshot, authorization, policy, runPrivilegedPackageCommand, journal.record); err != nil {
			return fmt.Errorf("native package run %s (%s): %w", journal.RunID, journal.Phase, err)
		}
		if outcome != nil {
			outcome.Status = "awaiting-startup"
		}
		return nil
	}
}

func runPrivilegedPackageCommand(parent context.Context, timeout time.Duration, dir string, _ []string, name string, args ...string) ([]byte, error) {
	switch name {
	case "/usr/bin/dpkg", "/usr/bin/dpkg-query", "/usr/bin/dpkg-deb", "/usr/bin/rpm", "/usr/bin/systemctl":
	default:
		return nil, fmt.Errorf("unsupported privileged package executable %q", name)
	}
	if err := checkRootPackageAncestors(filepath.Dir(name)); err != nil {
		return nil, fmt.Errorf("unsafe privileged package executable directory: %w", err)
	}
	if err := validateInstallPath(name); err != nil {
		return nil, fmt.Errorf("unsafe privileged package executable: %w", err)
	}
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return nil, fmt.Errorf("privileged package executable must be root-owned")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C", "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive"}
	return (&Manager{}).executeUpdateCommand(ctx, timeout, cmd)
}

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
	if !validPrivilegedPackageTarget(target) || source == nil || consume == nil {
		return fmt.Errorf("%w: invalid privileged package identity", ErrPublisherTrust)
	}
	return withRootPackageState(stateDir, true, func() error {
		return authenticateRootPackage(ctx, root, metadataURL, stateDir, source, target, consume)
	})
}

func validPrivilegedPackageTarget(target updatetrust.Target) bool {
	digest, err := hex.DecodeString(target.SHA256)
	return err == nil && len(digest) == 32 && target.SHA256 == hex.EncodeToString(digest) &&
		target.Component == "agent" && target.Platform == "linux" && target.Channel == "stable" &&
		target.Arch == runtime.GOARCH && stablePackageVersion.MatchString(target.Version) &&
		(target.Format == "deb" || target.Format == "rpm") && target.SizeBytes > 0 &&
		target.SizeBytes <= maxRepositoryPackageSize
}

func authenticateRootPackage(ctx context.Context, root []byte, metadataURL, stateDir string, source *os.File,
	target updatetrust.Target, consume func(string, updatetrust.Authorization) error) error {
	verifier, err := updatetrust.NewClient(updatetrust.Options{
		TrustedRoot: root, MetadataURL: metadataURL, CacheDir: filepath.Join(stateDir, "metadata"),
	})
	if err != nil {
		return fmt.Errorf("initialize independent privileged publisher trust: %w", err)
	}
	return withAuthenticatedPackage(ctx, stateDir, source, target, verifier, consume)
}

// Inspection and installation share one lock, but inspection never creates a
// state tree or initializes publisher trust over missing/corrupt state.
func withRootPackageState(stateDir string, create bool, consume func() error) (result error) {
	if os.Geteuid() != 0 {
		return fmt.Errorf("repository package state access requires root")
	}
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || filepath.Dir(stateDir) == stateDir {
		return fmt.Errorf("privileged package state must be a dedicated absolute directory")
	}
	// Check existing ancestors before creating anything; never adopt, chmod or
	// repair service-owned trust state on behalf of an untrusted requester.
	if err := checkRootPackageAncestors(filepath.Dir(stateDir)); err != nil {
		return err
	}
	if create {
		if err := os.Mkdir(stateDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create privileged package state: %w", err)
		}
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
	return consume()
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
