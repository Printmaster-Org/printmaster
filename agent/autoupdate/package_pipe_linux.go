package autoupdate

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Inherited standard streams may have been opened in blocking mode before Go
// initialized its poller. Duplicate ownership and register a nonblocking handle
// so deadlines/cancellation work for real subprocess pipes, not just os.Pipe.
// O_NONBLOCK is shared with the owned original descriptor; callers must not
// concurrently reuse it outside the one request/response operation.
func pollablePackagePipe(file *os.File) (*os.File, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return nil, fmt.Errorf("native package transport requires a pipe")
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate native package pipe: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		closeErr := unix.Close(fd)
		return nil, fmt.Errorf("prepare native package pipe: %w", errors.Join(err, closeErr))
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}
