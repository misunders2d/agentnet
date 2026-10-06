package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAppCommandInstallsAndRefreshesOnlyOwnedCopy(t *testing.T) {
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
