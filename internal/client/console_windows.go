package client

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// ownConsole starts a console program in a new console window of its own
// (the daemon has none to share), for a click that runs agentnet open.
func ownConsole() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
}
