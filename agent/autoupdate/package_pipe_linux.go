package autoupdate

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Inherited standard streams may have been opened in blocking mode before Go
// initialized its poller. Duplicate ownership and register a nonblocking handle
// so deadlines/cancellation work for subprocess pipes and systemd Unix streams.
// O_NONBLOCK is shared with the owned original descriptor; callers must not
// concurrently reuse it outside the one request/response operation.
func pollablePackagePipe(file *os.File) (*os.File, error) {
	if err := validatePackageTransport(file); err != nil {
		return nil, err
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var fd int
	var duplicateErr error
	// File.Fd switches pollable files back to blocking mode. With duplex
	// duplicates that can strand the other direction inside an uncancellable
	// syscall; RawConn preserves O_NONBLOCK while acquiring the descriptor.
	err = raw.Control(func(original uintptr) {
		fd, duplicateErr = unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 0)
	})
	if err != nil || duplicateErr != nil {
		return nil, fmt.Errorf("duplicate native package stream: %w", errors.Join(err, duplicateErr))
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		closeErr := unix.Close(fd)
		return nil, fmt.Errorf("prepare native package stream: %w", errors.Join(err, closeErr))
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

func validatePackageTransport(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return fmt.Errorf("native package transport requires a pipe or Unix stream socket")
	}
	if info.Mode()&os.ModeSocket != 0 {
		raw, err := file.SyscallConn()
		if err != nil {
			return err
		}
		var socketErr error
		err = raw.Control(func(fd uintptr) {
			kind, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
			if err != nil {
				socketErr = err
				return
			}
			address, err := unix.Getsockname(int(fd))
			if err != nil {
				socketErr = err
				return
			}
			if _, ok := address.(*unix.SockaddrUnix); !ok || kind != unix.SOCK_STREAM {
				socketErr = fmt.Errorf("native package socket must be a Unix stream")
			}
		})
		if err != nil || socketErr != nil {
			return fmt.Errorf("inspect native package socket: %w", errors.Join(err, socketErr))
		}
	}
	return nil
}
