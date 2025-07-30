//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package remote

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lock locks file for this process, created when missing: another process
// holding it, it fails at once.
func lock(file string) (*os.File, error) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s is held by another process of the agent", file)
		}
		return nil, err
	}
	return f, nil
}

// unlock releases the lock of f.
func unlock(f *os.File) error {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
