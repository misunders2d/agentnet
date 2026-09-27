//go:build !windows

package client

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd in its own process group and makes
// cancellation kill the whole group, so helpers the harness started stop
// too. A descendant that starts its own session or group escapes this.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
