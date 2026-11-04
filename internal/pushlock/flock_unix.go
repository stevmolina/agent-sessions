//go:build unix

package pushlock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// TryLock takes the exclusive push lock without waiting. The kernel drops a
// flock when its process exits, so a crashed push never leaves a stale lock.
func TryLock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// Held reports whether some process holds the lock right now.
func Held(dir string) bool {
	release, err := TryLock(dir)
	if err != nil {
		return errors.Is(err, ErrBusy)
	}
	release()
	return false
}
