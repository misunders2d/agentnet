package main

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/misunders2d/agentnet/internal/secfile"
)

// bundledWith is "app" in the agentnet program built into the AgentNet app
// (desktop/build.sh: -ldflags "-X main.bundledWith=app"): the app updates
// it as a whole, so `agentnet update` refuses to replace it.
var bundledWith string

// appStable is set when the AgentNet app runs this program from a place
// that does not last: an AppImage's mount (a new /tmp/.mount_* at every
// start, gone after quitting) or an unsigned macOS app, which can run
// translocated from a random read-only path. Hook commands must then name
// a stable copy (selfExe). The home it is kept in comes with it.
var appStable struct {
	on   bool
	home string
}

// selfExe is the agentnet program that hook commands and harness
// extensions run. Normally this one; under appStable a copy kept at
// <home>/bin/agentnet, made or refreshed when its bytes differ, so hooks
// installed from the app keep working after it restarts or moves.
func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil || !appStable.on {
		return exe, err
	}
	return stableCopy(exe, appStable.home)
}

// stableCopy keeps an owner-only copy of exe at <home>/bin/agentnet: written
// to a temporary file in that directory and renamed over the old copy only
// when the bytes differ.
func stableCopy(exe, home string) (string, error) {
	dir := filepath.Join(home, appStableExeDir)
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	want, err := fileSum(exe)
	if err != nil {
		return "", err
	}
	if have, err := fileSum(dst); err == nil && bytes.Equal(have, want) {
		return dst, nil
	}
	if err := secfile.EnsureDir(dir); err != nil {
		return "", err
	}
	src, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(dir, ".agentnet-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

func fileSum(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
