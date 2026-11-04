// Package version names the build, so a heartbeat says which binary a machine runs.
package version

import "runtime/debug"

// Version is set by release builds with
// -ldflags "-X github.com/usuario/sessions/internal/version.Version=<sha>".
var Version = ""

// String is the release version, else the git commit Go stamped into the build,
// else "dev". A build from a tree with uncommitted changes ends in "-dirty".
func String() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
