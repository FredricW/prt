//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive, non-blocking flock on f; false when another
// open file description (another process) holds it.
func tryLock(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // a file descriptor fits an int
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		}
		return false, err
	}
}
