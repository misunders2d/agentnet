//go:build !windows

package client

import "syscall"

// ownConsole is Windows-only: elsewhere a terminal launcher gives the
// program its window.
func ownConsole() *syscall.SysProcAttr { return nil }
