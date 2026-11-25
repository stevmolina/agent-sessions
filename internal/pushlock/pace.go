package pushlock

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TriggerInterval is the least time between pushes started by agent hooks on one
// machine. A turn that ends sooner leaves only its pending marker, and the next
// allowed run (the 15 minute timer, or a later trigger) serves it.
//
// Before 2026-11-24 every turn started a push. Together with the timer that kept
// the Neon database awake all month and used up the free plan's compute quota.
const TriggerInterval = 15 * time.Minute

const lastRunName = "push.last-run"

// MarkRun records that a push started now. Debounced triggers count from it.
func MarkRun(dir string, now time.Time) error {
	return writeStamp(filepath.Join(dir, lastRunName), now)
}

// TriggerAllowed reports whether a hook may start a push now: no push started
// within TriggerInterval. A missing or unreadable stamp allows it, and so does a
// stamp from the future (a clock that jumped back), so a bad file never stops
// pushes for good.
func TriggerAllowed(dir string, now time.Time) bool {
	last, ok := readStamp(filepath.Join(dir, lastRunName))
	if !ok || last.After(now) {
		return true
	}
	return now.Sub(last) >= TriggerInterval
}

func writeStamp(path string, t time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(t.UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readStamp(path string) (time.Time, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
