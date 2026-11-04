//go:build unix

package cli

import "syscall"

// detached starts the background push in its own session, so it outlives the
// hook that started it and no signal to the agent's process group reaches it.
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
