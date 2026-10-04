//go:build !linux

package client

import "syscall"

// deathSignal: only Linux kills a child when its parent dies; elsewhere a
// harness may outlive a daemon that crashed.
func deathSignal(*syscall.SysProcAttr) {}

// procStart: no process start time is read here, so a run's process group
// is never recognized, nor stopped, by a later daemon (survivingGroup).
func procStart(int) string { return "" }

func survivingGroup(int, string, string) bool { return false }
