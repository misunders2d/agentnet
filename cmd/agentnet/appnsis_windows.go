package main

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func runNSISInstaller(ctx context.Context, installer, dir string) error {
	cmd := exec.CommandContext(ctx, installer)
	// NSIS requires /D last, without quotes even when its path has spaces.
	// CreateProcess runs this executable directly; no command shell is used.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: windows.ComposeCommandLine([]string{installer, "/S"}) + " /D=" + dir, HideWindow: true}
	return cmd.Run()
}
