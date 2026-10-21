package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"printmaster/common/updatetrust"
)

const repositoryPackageSocketPath = "/run/printmaster-agent-update.sock"

// SubmitRepositoryPackage sends one authenticated snapshot to the independently
// supervised systemd helper. An awaiting-startup reply is not update success or
// proof of helper exit; protected reconciliation must confirm the service later.
func SubmitRepositoryPackage(ctx context.Context, source *os.File, target updatetrust.Target) (RepositoryPackageOutcome, error) {
	if err := validateRepositoryPackageSocket(repositoryPackageSocketPath); err != nil {
		return RepositoryPackageOutcome{}, err
	}
	return exchangeRepositoryPackageSocket(ctx, repositoryPackageSocketPath, source, target)
}

func validateRepositoryPackageSocket(path string) error {
	if err := checkRootPackageAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect systemd package socket: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o660 {
		return fmt.Errorf("systemd package socket must be root-owned with mode 0660")
	}
	return nil
}

func exchangeRepositoryPackageSocket(ctx context.Context, path string, source *os.File, target updatetrust.Target) (outcome RepositoryPackageOutcome, result error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return outcome, fmt.Errorf("connect to systemd package helper: %w", err)
	}
	defer func() { result = errors.Join(result, connection.Close()) }()
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return outcome, fmt.Errorf("package helper connection is not a Unix stream")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return outcome, err
	}
	var peer *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return outcome, fmt.Errorf("inspect package helper peer: %w", err)
	}
	if peerErr != nil || peer == nil || peer.Uid != 0 {
		return outcome, errors.Join(fmt.Errorf("package helper peer must be root"), peerErr)
	}
	request, err := socket.File()
	if err != nil {
		return outcome, err
	}
	response, err := socket.File()
	if err != nil {
		return outcome, errors.Join(err, request.Close())
	}
	// Both handles refer to one duplex stream; neither side relies on EOF to
	// delimit a frame. Cancellation closes only our connection, not the helper.
	return ExchangeRepositoryPackage(ctx, request, response, source, target)
}
