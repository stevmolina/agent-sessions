//go:build !unix

package pushlock

import "errors"

// TryLock is only implemented on macOS and Linux, the machines that push.
func TryLock(dir string) (release func(), err error) {
	return nil, errors.New("push lock unsupported on this platform")
}

// Held is always false where locking is unsupported.
func Held(dir string) bool { return false }
