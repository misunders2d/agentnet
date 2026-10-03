//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonCommandRepairsServicePathBeforeOpeningHome(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	agentnet := filepath.Join(bin, "agentnet")
	if err := os.WriteFile(agentnet, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	serviceBin := filepath.Join(t.TempDir(), "usr", "bin")
	if err := os.MkdirAll(serviceBin, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", serviceBin)
	oldArg0 := os.Args[0]
	os.Args[0] = agentnet
	t.Cleanup(func() { os.Args[0] = oldArg0 })
	badHome := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badHome, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--home", badHome, "daemon"}); err == nil {
		t.Fatal("daemon unexpectedly opened a file as its home")
	}
	want := bin + string(os.PathListSeparator) + serviceBin
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("daemon PATH = %q, want %q", got, want)
	}
}

func TestDaemonInstallDirFindsResponderLaunchersOnServicePath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "user bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	agentnet := filepath.Join(bin, "agentnet")
	if err := os.WriteFile(agentnet, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude", "pi", "omp"} {
		body := "#!/bin/sh\nprintf '%s\\n' " + name + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	serviceRoot := t.TempDir()
	localSystemBin := filepath.Join(serviceRoot, "usr", "local", "bin")
	systemBin := filepath.Join(serviceRoot, "usr", "bin")
	for _, dir := range []string{localSystemBin, systemBin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	servicePath := localSystemBin + string(os.PathListSeparator) + systemBin
	t.Setenv("PATH", servicePath)
	if _, err := exec.LookPath("codex"); err == nil {
		t.Fatal("isolated service PATH unexpectedly finds codex before repair")
	}

	keepDaemonInstallDirOnPath(agentnet)
	wantPath := bin + string(os.PathListSeparator) + servicePath
	if got := os.Getenv("PATH"); got != wantPath {
		t.Fatalf("PATH = %q, want %q", got, wantPath)
	}
	for _, name := range []string{"codex", "claude", "pi", "omp"} {
		found, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("%s not found after repair: %v", name, err)
		}
		if found != filepath.Join(bin, name) {
			t.Fatalf("%s resolved to %q", name, found)
		}
		out, err := exec.Command(name).Output()
		if err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
		if strings.TrimSpace(string(out)) != name {
			t.Fatalf("%s output = %q", name, out)
		}
	}

	// An in-place update restarts the daemon with the same environment. The
	// next startup must keep the install directory once, not grow PATH.
	keepDaemonInstallDirOnPath(agentnet)
	if got := os.Getenv("PATH"); got != wantPath {
		t.Fatalf("PATH after restart = %q, want %q", got, wantPath)
	}
}
