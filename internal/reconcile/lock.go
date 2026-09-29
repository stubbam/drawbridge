package reconcile

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// FileLock is a Locker backed by flock(2) on a file, shared by every Drawbridge process
// that reconciles.
type FileLock struct {
	Path string
}

// Lock blocks until the lock is free, then takes it.
func (l FileLock) Lock() (func(), error) {
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd()) //nolint:gosec // G115: a file descriptor fits an int.
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking %s: %w", l.Path, err)
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
