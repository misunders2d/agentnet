//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// loginShellPath asks the person's login shell for its PATH: `$SHELL -l -c`
// (a login shell reads the profile files; -i is not used, since that loads
// aliases and job control and can wait for a terminal), bounded by timeout.
// The markers keep anything the profile prints out of the answer.
func loginShellPath(shell string, timeout time.Duration) (string, error) {
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", `printf '%s%s%s' `+pathStart+` "$PATH" `+pathEnd)
	cmd.Stdin = nil
	cmd.Stderr = nil
	cmd.Env = os.Environ()
	// A profile that hangs is stopped with everything it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	i := bytes.LastIndex(out, []byte(pathStart))
	j := bytes.LastIndex(out, []byte(pathEnd))
	if i < 0 || j < i {
		return "", errors.New("no PATH in the login shell's answer")
	}
	return string(out[i+len(pathStart) : j]), nil
}

// addLoginShellPath gives this process the PATH the person's login shell
// has, plus well-known per-user directories, so the coding agents it starts
// are found as in a terminal. Failures leave PATH as it is.
func addLoginShellPath(logf func(string, ...any)) {
	login, err := loginShellPath(os.Getenv("SHELL"), shellPathTimeout)
	if err != nil {
		logf("the login shell's PATH could not be read (%v); coding agents are looked for in well-known directories", err)
	}
	home, _ := os.UserHomeDir()
	os.Setenv("PATH", mergePath(os.Getenv("PATH"), login, userBinDirs(home)))
}
