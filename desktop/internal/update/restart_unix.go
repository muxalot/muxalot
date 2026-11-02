//go:build !windows

package update

import (
	"os"
	"syscall"
)

func restart(path string) error { return syscall.Exec(path, os.Args, os.Environ()) }
