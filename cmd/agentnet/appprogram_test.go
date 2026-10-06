package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Release builds can record their tag in Main.Version without retaining -ldflags.
// Exercise adoption through the installer with a real, never-executed binary.
func TestAppCommandAdoptsOfficialModuleVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("NSIS owns Windows PATH")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/misunders2d/agentnet\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() { panic(\"unrecognized commands must never execute\") }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "init", "--quiet")
	run("git", "add", "go.mod", "main.go")
	run("git", "-c", "user.name=Release Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	run("git", "tag", "v1.2.3")
	binary := filepath.Join(t.TempDir(), "official")
	run("go", "build", "-trimpath", "-o", binary, ".")
	info, err := buildinfo.ReadFile(binary)
	if err != nil || info.Main.Version != "v1.2.3" {
		t.Fatalf("release fixture metadata: %+v, %v", info, err)
	}
	original, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	fakeReleaseServer(t, &releaseStub{asset: original})
	t.Setenv("PATH", t.TempDir())
	for _, modified := range []bool{false, true} {
		home, user := t.TempDir(), t.TempDir()
		dst := appCommandPath(user)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			t.Fatal(err)
		}
		old := append([]byte(nil), original...)
		if modified {
			old = append(old, []byte("custom change")...)
		}
		if err := os.WriteFile(dst, old, 0755); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(t.TempDir(), "new-app-command")
		if err := os.WriteFile(src, []byte("updated app command"), 0755); err != nil {
			t.Fatal(err)
		}
		registrationErr := registerAppCommandTarget(context.Background(), home, dst)
		if modified && registrationErr == nil {
			t.Fatal("modified official target registered")
		}
		if !modified && registrationErr != nil {
			t.Fatal(registrationErr)
		}
		status := installAppCommand(context.Background(), src, home, user, false)
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if modified {
			if status.State != "custom" || !bytes.Equal(got, old) {
				t.Fatalf("modified release overwritten: %+v", status)
			}
		} else if status.State != "installed" || string(got) != "updated app command" {
			t.Fatalf("official release not adopted: %+v", status)
		}
	}
}

func TestAppCommandInstallsAndRefreshesOnlyOwnedCopy(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if runtime.GOOS == "windows" {
		t.Skip("NSIS owns Windows PATH")
	}
	root := t.TempDir()
	home := filepath.Join(root, "config")
	user := filepath.Join(root, "user")
	os.MkdirAll(home, 0700)
	os.MkdirAll(user, 0700)
	src := filepath.Join(root, "source")
	os.WriteFile(src, []byte("app one"), 0700)
	dst := appCommandPath(user)
	s := installAppCommand(context.Background(), src, home, user, false)
	if s.State != "installed" {
		t.Fatalf("install: %+v", s)
	}
	if b, _ := os.ReadFile(dst); string(b) != "app one" {
		t.Fatalf("wrong command %q", b)
	}
	os.WriteFile(src, []byte("app two"), 0700)
	s = installAppCommand(context.Background(), src, home, user, false)
	if s.State != "installed" {
		t.Fatalf("refresh: %+v", s)
	}
	if b, _ := os.ReadFile(dst); string(b) != "app two" {
		t.Fatalf("wrong refresh %q", b)
	}
	os.WriteFile(dst, []byte("my custom build"), 0700)
	s = installAppCommand(context.Background(), src, home, user, false)
	if s.State != "custom" {
		t.Fatalf("must preserve changed build: %+v", s)
	}
	if b, _ := os.ReadFile(dst); string(b) != "my custom build" {
		t.Fatal("custom build overwritten")
	}
	s = installAppCommand(context.Background(), src, home, user, true)
	if s.State != "installed" {
		t.Fatalf("explicit replacement: %+v", s)
	}
	if b, _ := os.ReadFile(dst + ".old"); string(b) != "my custom build" {
		t.Fatal("custom backup not retained")
	}
	s = installAppCommand(context.Background(), src, home, user, false)
	if s.State != "installed" {
		t.Fatal(s)
	}
	if b, _ := os.ReadFile(dst + ".old"); string(b) != "my custom build" {
		t.Fatal("unchanged start replaced custom backup")
	}
	for _, name := range []string{".profile", ".zprofile"} {
		b, err := os.ReadFile(filepath.Join(user, name))
		if err != nil || bytes.Count(b, []byte("# AgentNet command\n")) != 1 {
			t.Fatalf("PATH block duplicated: %s %q %v", name, b, err)
		}
	}
}

func TestAppCommandNeverOverwritesSymlinkTarget(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink")
	}
	root := t.TempDir()
	home := filepath.Join(root, "config")
	user := filepath.Join(root, "user")
	os.MkdirAll(home, 0700)
	os.MkdirAll(filepath.Dir(appCommandPath(user)), 0755)
	src := filepath.Join(root, "source")
	custom := filepath.Join(root, "custom")
	os.WriteFile(src, []byte("app"), 0700)
	os.WriteFile(custom, []byte("custom"), 0700)
	if err := os.Symlink(custom, appCommandPath(user)); err != nil {
		t.Fatal(err)
	}
	s := installAppCommand(context.Background(), src, home, user, false)
	if s.State != "custom" {
		t.Fatalf("symlink: %+v", s)
	}
	s = installAppCommand(context.Background(), src, home, user, true)
	if s.State != "installed" {
		t.Fatalf("explicit symlink replacement: %+v", s)
	}
	if st, err := os.Lstat(appCommandPath(user)); err != nil || !st.Mode().IsRegular() {
		t.Fatal("approved symlink not replaced safely")
	}
	if b, _ := os.ReadFile(custom); string(b) != "custom" {
		t.Fatal("symlink target overwritten")
	}
}
