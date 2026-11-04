//go:build !unix

package cli

import "syscall"

func detached() *syscall.SysProcAttr { return nil }
