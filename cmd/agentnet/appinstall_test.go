package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/lockfile"
)

func appImageFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux AppImage installation")
	}
	root := t.TempDir()
	downloads := filepath.Join(root, "Downloads")
	if err := os.Mkdir(downloads, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(downloads, "AgentNet.AppImage")
	data := append([]byte{'\x7f', 'E', 'L', 'F', 2, 1, 1, 0, 'A', 'I', 2}, []byte("synthetic package, never executed")...)
	if err := os.WriteFile(source, data, 0700); err != nil {
		t.Fatal(err)
	}
	return source, filepath.Join(root, ".local", "share"), data
}

func TestAppImageInstallSurvivesDownloadRemoval(t *testing.T) {
	source, data, want := appImageFixture(t)
	path, err := installAppImage(source, data)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(data, "agentnet", "AgentNet.AppImage") {
		t.Fatal(path)
	}
	before, err := os.Stat(path)
	if err != nil || before.Mode().Perm() != 0700 {
		t.Fatalf("owner-only executable: %v %v", before, err)
	}
	for _, src := range []string{source, path} {
		if same, e := installAppImage(src, data); e != nil || same != path {
			t.Fatalf("idempotence: %s %v", same, e)
		}
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("reinstallation replaced identical file", err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if got, e := os.ReadFile(path); e != nil || !bytes.Equal(got, want) {
		t.Fatalf("stable copy: %q %v", got, e)
	}
	if _, err = appUpdateSourceSum(path, ""); err != nil {
		t.Fatal("stable copy cannot be updated", err)
	}
	if same, e := installAppImage(path, data); e != nil || same != path {
		t.Fatalf("stable reopen depends on download: %s %v", same, e)
	}
}

func TestAppImageInstallPreservesConflictsAndSource(t *testing.T) {
	for _, kind := range []string{"different", "directory", "symlink", "install-dir-symlink", "permission", "locked"} {
		t.Run(kind, func(t *testing.T) {
			source, data, original := appImageFixture(t)
			dir := filepath.Join(data, "agentnet")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(dir, "AgentNet.AppImage")
			var err error
			switch kind {
			case "different":
				err = os.WriteFile(dst, []byte("newer or unrelated installation"), 0700)
			case "directory":
				err = os.Mkdir(dst, 0700)
			case "symlink":
				err = os.Symlink(source, dst)
			case "install-dir-symlink":
				if err = os.Remove(dir); err == nil {
					err = os.Symlink(filepath.Dir(source), dir)
				}
			case "permission":
				if os.Geteuid() == 0 {
					t.Skip("root ignores fixture directory permissions")
				}
				err = os.Remove(dir)
				if err == nil {
					err = os.Chmod(data, 0500)
				}
				t.Cleanup(func() { os.Chmod(data, 0700) })
			case "locked":
				var unlock func()
				unlock, err = lockfile.Acquire(updateLockPath(dst))
				if err == nil {
					defer unlock()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if path, err := installAppImage(source, data); err == nil || path != "" {
				t.Fatalf("unsafe install: %q %v", path, err)
			}
			if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, original) {
				t.Fatal("source changed", err)
			}
			if kind == "different" {
				if got, err := os.ReadFile(dst); err != nil || string(got) != "newer or unrelated installation" {
					t.Fatal("existing installation changed", err)
				}
			}
		})
	}
}

func TestAppImageInstallRejectsWrongSourceAndChecksum(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "symlink", "not-executable", "wrong-format", "checksum"} {
		t.Run(kind, func(t *testing.T) {
			source, data, _ := appImageFixture(t)
			var err error
			switch kind {
			case "missing", "directory", "symlink":
				err = os.Remove(source)
				if err == nil && kind == "directory" {
					err = os.Mkdir(source, 0700)
				} else if err == nil && kind == "symlink" {
					err = os.Symlink("missing", source)
				}
			case "not-executable":
				err = os.Chmod(source, 0600)
			case "wrong-format":
				err = os.WriteFile(source, []byte("not an AppImage"), 0700)
			case "checksum":
				err = os.MkdirAll(filepath.Join(data, "agentnet"), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(data, "agentnet", "AgentNet.AppImage")
			if kind == "checksum" {
				err = copyAppImage(source, dst, strings.Repeat("0", 64))
			} else {
				_, err = installAppImage(source, data)
			}
			if err == nil {
				t.Fatal("invalid source installed")
			}
			if _, err = os.Lstat(dst); !os.IsNotExist(err) {
				t.Fatal("failed install published a destination", err)
			}
		})
	}
}

func TestAppImageCopyNeverReplacesDestination(t *testing.T) {
	source, data, b := appImageFixture(t)
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(data, "existing")
	if err := os.WriteFile(dst, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if err := copyAppImage(source, dst, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("existing destination replaced")
	}
	if got, _ := os.ReadFile(dst); string(got) != "keep me" {
		t.Fatal("destination changed")
	}
}

func TestAppImageInstallCommandOnlyReportsVerifiedCopy(t *testing.T) {
	source, data, _ := appImageFixture(t)
	old := bundledWith
	t.Cleanup(func() { bundledWith = old })
	var out bytes.Buffer
	bundledWith = ""
	if err := runAppInstall([]string{source, data}, &out); err == nil || out.Len() != 0 {
		t.Fatal("standalone command installed the app")
	}
	bundledWith = "app"
	if err := runAppInstall([]string{source + ".missing", data}, &out); err == nil || out.Len() != 0 {
		t.Fatal("failed installation reported success")
	}
	if err := runAppInstall([]string{source, data}, &out); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := json.Unmarshal(out.Bytes(), &path); err != nil || path != filepath.Join(data, "agentnet", "AgentNet.AppImage") {
		t.Fatalf("installation result %s: %v", out.String(), err)
	}
}
