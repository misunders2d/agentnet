package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Fixed system paths prevent a coding-agent PATH from selecting an unrelated
// installer. Tests replace these calls; no test invokes a real installer.
var appInstallerRun = func(ctx context.Context, program string, args ...string) error {
	if filepath.Ext(program) == ".exe" && len(args) == 2 && args[0] == "/S" && strings.HasPrefix(args[1], "/D=") {
		return runNSISInstaller(ctx, program, strings.TrimPrefix(args[1], "/D="))
	}
	return exec.CommandContext(ctx, program, args...).Run()
}
var appInstallerOutput = func(ctx context.Context, program string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, program, args...).Output()
}

func linuxPackageKind(app string) string {
	if !filepath.IsAbs(app) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := appInstallerOutput(ctx, "/usr/bin/dpkg-query", "-S", app); err == nil && strings.Contains(string(out), ": "+app) {
		return "deb"
	}
	if out, err := appInstallerOutput(ctx, "/usr/bin/rpm", "-qf", app); err == nil && strings.TrimSpace(string(out)) != "" {
		return "rpm"
	}
	return ""
}

func copyAppTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return errors.New("the installed app contains a file that cannot be backed up safely")
		}
		from, err := os.Open(path)
		if err != nil {
			return err
		}
		defer from.Close()
		to, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(to, from)
		if err == nil {
			err = to.Sync()
		}
		closeErr := to.Close()
		if err == nil {
			err = closeErr
		}
		return err
	})
}

func installWindowsApp(p appUpdatePlan) error {
	if p.Backup == "" {
		return errors.New("the previous app backup is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	installErr := appInstallerRun(ctx, p.Asset, "/S", "/D="+filepath.Dir(p.App))
	if installErr == nil && p.Version != "" {
		line, versionErr := fileVersion(ctx, filepath.Join(filepath.Dir(p.App), "agentnet.exe"))
		if versionErr != nil || !strings.HasPrefix(line, "agentnet "+p.Version+" (protocol ") {
			installErr = errors.New("the installer did not install the new AgentNet program")
		}
	}
	if err := installErr; err != nil {
		if restore := copyAppTree(p.Backup, filepath.Dir(p.App)); restore != nil {
			return fmt.Errorf("update failed (%v); previous app retained at %s; restore failed: %v", err, p.Backup, restore)
		}
		return errors.New("The update was cancelled or failed. Your previous app was restored.")
	}
	return nil
}

// Package updates occur while the original shell and daemon keep working.
// A checksum-verified copy of the original package is ready before asking
// for OS authorization, so a failed install can restore the same version.
func installLinuxPackage(p appUpdatePlan) error {
	sum, err := fileSum(p.Asset)
	if err != nil || hex.EncodeToString(sum) != p.Sum {
		return errors.New("The download changed after verification. Your current app is unchanged.")
	}
	previousApp, _ := fileSum(p.App)
	program := filepath.Join(filepath.Dir(p.App), "agentnet")
	previousProgram, _ := fileSum(program)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	manager := "/usr/bin/dpkg"
	args := []string{"--install"}
	if p.Kind == "rpm" {
		manager = "/usr/bin/rpm"
		args = []string{"--upgrade", "--oldpackage"}
	}
	install := func(asset string) error {
		return appInstallerRun(ctx, "/usr/bin/pkexec", append([]string{manager}, append(args, asset)...)...)
	}
	if err := install(p.Asset); err != nil {
		nowApp, appErr := fileSum(p.App)
		nowProgram, programErr := fileSum(program)
		if appErr == nil && bytes.Equal(previousApp, nowApp) && ((programErr == nil && bytes.Equal(previousProgram, nowProgram)) || (programErr != nil && previousProgram == nil)) {
			return errors.New("The update was cancelled or failed. Your current app is unchanged.")
		}
		if restore := install(p.Backup); restore != nil {
			return fmt.Errorf("The update did not finish. AgentNet is still running. Restoring its package also failed: %v", restore)
		}
		return errors.New("The update was cancelled or failed. Your previous package was restored.")
	}
	return nil
}
