// Package pushlock keeps one push running per machine and folds extra requests
// into it.
//
// Agent hooks ask for a push at the end of every turn, often several at once.
// The first request takes an exclusive file lock and pushes. A request that
// finds the lock taken leaves a pending marker and returns at once. The running
// push checks the marker when it finishes and goes round once more, so the last
// turn is always uploaded and at most one push runs at a time.
package pushlock

import (
	"errors"
	"os"
	"path/filepath"
)

const (
	lockName    = "push.lock"
	pendingName = "push.pending"
	// MaxRuns caps how many times one process loops on pending requests.
	// A marker still left after that is picked up by the next trigger or timer.
	MaxRuns = 5
)

// ErrBusy means another process holds the push lock.
var ErrBusy = errors.New("push already running")

// MarkPending records that a push was asked for.
func MarkPending(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, pendingName), nil, 0o600)
}

// TakePending clears the marker and reports whether it was set.
func TakePending(dir string) bool {
	return os.Remove(filepath.Join(dir, pendingName)) == nil
}

// Pending reports whether the marker is set, without clearing it.
func Pending(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, pendingName))
	return err == nil
}

// Run records a request, then pushes while holding the lock and while requests
// keep arriving. If another process holds the lock, Run returns ran=false with
// no error: the request is on disk and that process will push again for it.
//
// The order matters. A request is written before the lock is tried, and the
// holder looks for requests once more after it unlocks. So a request is either
// seen by the holder or finds the lock free, and none is lost.
func Run(dir string, push func() int) (code int, ran bool, err error) {
	if err := MarkPending(dir); err != nil {
		return 0, false, err
	}
	runs := 0
	for runs < MaxRuns {
		release, err := TryLock(dir)
		if errors.Is(err, ErrBusy) {
			return code, runs > 0, nil
		}
		if err != nil {
			return code, runs > 0, err
		}
		for runs < MaxRuns && TakePending(dir) {
			code = push()
			runs++
		}
		release()
		if !Pending(dir) {
			break
		}
	}
	return code, runs > 0, nil
}
