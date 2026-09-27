package secfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteReadAndRejectLoosened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key")
	if err := Write(path, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || string(got) != "secret" {
		t.Fatalf("Read = %q, %v", got, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("world-readable secret accepted")
	}
}

func TestCreateTempIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	f, err := CreateTemp(dir, "x-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := check(f.Name()); err != nil {
		t.Fatal(err)
	}
}
