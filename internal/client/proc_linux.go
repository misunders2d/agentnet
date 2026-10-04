//go:build linux

package client

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// deathSignal makes the kernel kill the harness when the daemon that
// started it dies, however it dies (SIGKILL, a crash): a run is never left
// going unseen, to be run a second time beside it.
func deathSignal(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGKILL }

// procStart names process pid for as long as this machine runs: the boot
// it runs in and its start time ("" if unknown). A pid that is reused
// later has another.
func procStart(pid int) string {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	_, start, ok := procStat(pid)
	if !ok {
		return ""
	}
	return strings.TrimSpace(string(boot)) + ":" + strconv.FormatUint(start, 10)
}

// procStat reads process pid's group and start time from /proc.
func procStat(pid int) (pgrp int, start uint64, ok bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	// pid (comm) state ppid pgrp ... : comm may hold anything, so the
	// fields are counted after its closing parenthesis (field 3 on).
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(string(data[i+1:]))
	if len(f) < 20 {
		return 0, 0, false
	}
	pgrp, err1 := strconv.Atoi(f[2])                // field 5
	start, err2 := strconv.ParseUint(f[19], 10, 64) // field 22
	return pgrp, start, err1 == nil && err2 == nil
}

// survivingGroup reports whether process group pgid still holds a run that
// a daemon recorded (runJob): its leader with the start recorded (start,
// procStart of the leader), or, the leader gone, a process started since
// in that group with the run's own environment (home its AGENTNET_HOME).
// A group id is never reused while any process is in that group, so one
// such process proves the group is that run's.
func survivingGroup(pgid int, start, home string) bool {
	boot, at, ok := strings.Cut(start, ":")
	if !ok || pgid <= 1 {
		return false
	}
	now, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(now)) != boot {
		return false // another boot: nothing of that run is left
	}
	t, err := strconv.ParseUint(at, 10, 64)
	if err != nil {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		pgrp, s, ok := procStat(pid)
		if !ok || pgrp != pgid || s < t {
			continue
		}
		if pid == pgid {
			if s == t {
				return true
			}
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil {
			continue
		}
		vars := strings.Split(string(env), "\x00")
		if slices.Contains(vars, "AGENTNET_HOME="+home) && slices.Contains(vars, BackgroundEnv+"=1") {
			return true
		}
	}
	return false
}
