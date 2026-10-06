package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
)

func ownedCommandTarget(t *testing.T, home, path string) {
	t.Helper()
	sum, e := fileSum(path)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(appCommandRecord{Path: path, Sum: hex.EncodeToString(sum)})
	if e = secfile.Write(filepath.Join(home, "app-command.json"), b); e != nil {
		t.Fatal(e)
	}
}

func TestAppCommandRegisteredPATHShadowRefreshesWithCanonical(t *testing.T) {
	home, user, bin := t.TempDir(), t.TempDir(), t.TempDir()
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	shadow := filepath.Join(bin, name)
	os.WriteFile(shadow, []byte("previous app CLI"), 0755)
	ownedCommandTarget(t, home, shadow)
	if e := registerAppCommandTarget(context.Background(), home, shadow); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin)
	src := filepath.Join(t.TempDir(), "bundled")
	os.WriteFile(src, []byte("new app CLI"), 0755)
	status := installAppCommand(context.Background(), src, home, user, false)
	if status.State != "installed" {
		t.Fatal(status)
	}
	if s := checkAppCommand(home, src); s.State != "installed" {
		t.Fatal(s)
	}
	canonical := appCommandPath(user)
	if canonical == "" {
		canonical = src
	}
	for _, path := range []string{shadow, canonical} {
		b, e := os.ReadFile(path)
		if e != nil || string(b) != "new app CLI" {
			t.Fatalf("unreconciled %s: %q %v", path, b, e)
		}
	}
	r, e := readAppCommandRecord(home)
	if e != nil || r.Path != canonical || len(r.Targets) == 0 {
		t.Fatalf("ownership lost: %+v %v", r, e)
	}
	if e := registerAppCommandTarget(context.Background(), home, canonical); e != nil {
		t.Fatal(e)
	}
	// A later app startup must update registered and canonical targets again.
	os.WriteFile(src, []byte("next app CLI"), 0755)
	if s := installAppCommand(context.Background(), src, home, user, false); s.State != "installed" {
		t.Fatal(s)
	}
	if b, _ := os.ReadFile(shadow); string(b) != "next app CLI" {
		t.Fatal("recorded shadow did not refresh")
	}
}

func TestAppCommandChangedRegisteredTargetPreservedAndIncomplete(t *testing.T) {
	home, user, bin := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PATH", bin)
	shadow := filepath.Join(bin, "agentnet")
	os.WriteFile(shadow, []byte("owned"), 0755)
	ownedCommandTarget(t, home, shadow)
	if e := registerAppCommandTarget(context.Background(), home, shadow); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(shadow, []byte("custom replacement"), 0755)
	before, _ := secfile.Read(filepath.Join(home, "app-command.json"))
	if s := checkAppCommand(home, shadow); s.State == "installed" {
		t.Fatal("read-only check accepted changed recorded bytes")
	}
	after, _ := secfile.Read(filepath.Join(home, "app-command.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("read-only check mutated ownership")
	}
	src := filepath.Join(t.TempDir(), "bundled")
	os.WriteFile(src, []byte("new app CLI"), 0755)
	s := installAppCommand(context.Background(), src, home, user, false)
	if s.State == "installed" {
		t.Fatal("partial update claimed complete")
	}
	if b, _ := os.ReadFile(shadow); string(b) != "custom replacement" {
		t.Fatal("custom target overwritten")
	}
	canonical := appCommandPath(user)
	if canonical == "" {
		canonical = src
	}
	if b, _ := os.ReadFile(canonical); string(b) != "new app CLI" {
		t.Fatal("canonical copy not refreshed")
	}
}

func TestRegisterAppCommandTargetRejectsUnknownSymlinkAndReadOnly(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "target")
	os.WriteFile(path, []byte("unknown"), 0755)
	if e := registerAppCommandTarget(context.Background(), home, path); e == nil {
		t.Fatal("unknown command adopted")
	}
	ownedCommandTarget(t, home, path)
	os.Chmod(path, 0444)
	if e := registerAppCommandTarget(context.Background(), home, path); e == nil {
		t.Fatal("read-only command adopted")
	}
	os.Chmod(path, 0755)
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		os.Symlink(path, link)
		if e := registerAppCommandTarget(context.Background(), home, link); e == nil {
			t.Fatal("symlink adopted")
		}
	}
	if e := registerAppCommandTarget(context.Background(), home, "relative"); e == nil {
		t.Fatal("relative target adopted")
	}
}
