package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func readAppCommandRecord(home string) (appCommandRecord, error) {
	var r appCommandRecord
	b, e := secfile.Read(filepath.Join(home, "app-command.json"))
	if errors.Is(e, os.ErrNotExist) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, errors.New("invalid saved app command ownership")
	}
	return r, nil
}
func (r *appCommandRecord) setTarget(t appCommandTarget) {
	if r.Path == t.Path {
		r.Sum = t.Sum
	}
	for i := range r.Targets {
		if r.Targets[i].Path == t.Path {
			r.Targets[i] = t
			return
		}
	}
	r.Targets = append(r.Targets, t)
}

// Registration proves bytes without executing them. A writable sibling is
// required for atomic replacement; Windows may hold the executable open until
// this requesting CLI exits, so registration does not open it for writing.
func validateAppCommandTarget(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("command target must be an absolute clean path")
	}
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0222 == 0 {
		return errors.New("command target must be a writable regular file; symlinks are preserved")
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".agentnet-target-check-*")
	if e != nil {
		return e
	}
	name := f.Name()
	e = f.Close()
	os.Remove(name)
	return e
}
func registerAppCommandTarget(ctx context.Context, home, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("command target must be an absolute clean path")
	}
	release, e := lockfile.Acquire(filepath.Join(home, "app-command.lock"))
	if e != nil {
		return e
	}
	defer release()
	return registerAppCommandTargetLocked(ctx, home, path)
}
func registerAppCommandTargetLocked(ctx context.Context, home, path string) error {
	release, e := lockfile.Acquire(updateLockPath(path))
	if e != nil {
		return e
	}
	defer release()
	if e = validateAppCommandTarget(path); e != nil {
		return e
	}
	beforeInfo, e := os.Lstat(path)
	if e != nil {
		return e
	}
	before, e := fileSum(path)
	if e != nil {
		return e
	}
	if !appOwnsCommand(ctx, "", path, filepath.Join(home, "app-command.json")) {
		return errors.New("command target is custom or its official checksum cannot be verified")
	}
	sum, e := fileSum(path)
	if e != nil {
		return e
	}
	afterInfo, statErr := os.Lstat(path)
	if statErr != nil || !afterInfo.Mode().IsRegular() || afterInfo.Mode().Perm()&0222 == 0 || !os.SameFile(beforeInfo, afterInfo) {
		return errors.New("command target changed during ownership verification")
	}
	if !bytes.Equal(before, sum) {
		return errors.New("command target changed during ownership verification")
	}
	r, e := readAppCommandRecord(home)
	if e != nil {
		return e
	}
	r.setTarget(appCommandTarget{Path: path, Sum: hex.EncodeToString(sum)})
	b, _ := json.Marshal(r)
	return secfile.Write(filepath.Join(home, "app-command.json"), b)
}

func installAppCommand(ctx context.Context, src, home, userHome string, replace bool) appCommandStatus {
	dst := appCommandPath(userHome)
	if dst == "" {
		dst = src
	}
	fail := func(e error) appCommandStatus { return appCommandStatus{Path: dst, State: "error", Problem: e.Error()} }
	release, e := lockfile.Acquire(filepath.Join(home, "app-command.lock"))
	if e != nil {
		return fail(e)
	}
	defer release()
	r, e := readAppCommandRecord(home)
	if e != nil {
		return fail(e)
	}
	targets := []string{}
	for _, t := range r.Targets {
		if t.Path != dst {
			targets = append(targets, t.Path)
		}
	}
	// LookPath inspects names and permissions only; never runs the candidate.
	var discoveryProblem *appCommandStatus
	if effective, e := exec.LookPath("agentnet"); e == nil {
		effective, e = filepath.Abs(effective)
		if e == nil && effective != dst && effective != src {
			if e = registerAppCommandTargetLocked(ctx, home, effective); e != nil {
				discoveryProblem = &appCommandStatus{Path: effective, State: "error", Problem: "The command earlier on PATH could not be verified: " + e.Error() + ". Move this command off PATH or restore a verified official command at " + effective + ", then retry."}
			} else {
				targets = append(targets, effective)
			}
		}
	}
	state := installAppCommandAt(ctx, src, home, userHome, dst, replace, true)
	if state.State != "installed" {
		return state
	}
	seen := map[string]bool{dst: true}
	for _, target := range targets {
		if seen[target] {
			continue
		}
		seen[target] = true
		if e = validateAppCommandTarget(target); e != nil {
			return appCommandStatus{Path: target, State: "error", Problem: fmt.Sprintf("Required command target %s: %v. Restore this managed command target, then retry.", target, e)}
		}
		s := installAppCommandAt(ctx, src, home, userHome, target, false, false)
		if s.State != "installed" {
			if s.State == "custom" {
				s.Problem = "The command is custom or its official checksum cannot be verified. Restore an unchanged app-owned or verified official command at this path, then retry."
			}
			s.State = "error"
			s.Problem = "Required command target " + target + ": " + s.Problem
			return s
		}
	}
	if discoveryProblem != nil {
		return *discoveryProblem
	}
	state.Problem = strings.TrimSpace(state.Problem)
	return checkAppCommand(home, src)
}

// Fresh read-only completion proof. The bundled source is trusted by the app;
// names, prior success labels and release tags never substitute for its bytes.
func checkAppCommand(home, src string) appCommandStatus {
	r, e := readAppCommandRecord(home)
	state := appCommandStatus{Path: r.Path, State: "error"}
	fail := func(problem string) appCommandStatus { state.Problem = problem; return state }
	if e != nil {
		return fail(e.Error())
	}
	if r.Path == "" {
		return fail("No managed command ownership is recorded.")
	}
	want, e := fileSum(src)
	if e != nil {
		return fail("Cannot verify the bundled command.")
	}
	expected := hex.EncodeToString(want)
	targets := append([]appCommandTarget{{Path: r.Path, Sum: r.Sum}}, r.Targets...)
	if effective, e := exec.LookPath("agentnet"); e == nil {
		if path, e := filepath.Abs(effective); e == nil {
			targets = append(targets, appCommandTarget{Path: path, Sum: expected})
			state.Path = path
		}
	}
	for _, target := range targets {
		if !filepath.IsAbs(target.Path) {
			state.Path = target.Path
			return fail("Invalid managed command path.")
		}
		st, e := os.Lstat(target.Path)
		if e != nil || !st.Mode().IsRegular() {
			state.Path = target.Path
			return fail("Command target is missing or is a symlink: " + target.Path)
		}
		have, e := fileSum(target.Path)
		if e != nil || target.Sum != expected || !bytes.Equal(have, want) {
			state.Path = target.Path
			return fail("Command target does not match the running app: " + target.Path)
		}
	}
	state.State = "installed"
	return state
}
