//go:build !windows

package client

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd in its own process group and makes
// cancellation kill the whole group, so helpers the harness started stop
// too. A descendant that starts its own session or group escapes this. On
// Linux the harness also dies with the daemon (deathSignal), and a later
// daemon stops what is left of its group (runGroup, survivingGroup).
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	deathSignal(cmd.SysProcAttr)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// runGroup is the process group of a started cmd (ownProcessGroup).
func runGroup(cmd *exec.Cmd) int { return cmd.Process.Pid }

// killGroup kills every process of group pgid.
func killGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

// stopGroup kills what is left of cmd's process group once cmd has exited
// (see ownProcessGroup): helpers it left running in the background. A
// descendant that started its own session or group escapes this too.
func stopGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // ESRCH: nothing left
	}
}
