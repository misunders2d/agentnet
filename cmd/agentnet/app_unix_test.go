//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A program started from the app launcher gets the login shell's PATH and
// existing per-user directories in front of its own, without duplicates.
func TestAppLoginShellPath(t *testing.T) {
	got, err := loginShellPath(fakeShell(t, "/opt/a:/usr/bin"), shellPathTimeout)
	if err != nil || got != "/opt/a:/usr/bin" {
		t.Fatalf("login PATH %q %v", got, err)
	}
	extra := t.TempDir()
	merged := mergePath("/usr/bin:/bin", "/opt/a:/usr/bin:relative", []string{extra, "/no/such/dir", "/bin"})
	if merged != "/opt/a:"+extra+":/usr/bin:/bin" {
		t.Fatalf("merged %q", merged)
	}
	slow := filepath.Join(t.TempDir(), "slow")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\n"), 0o700)
	start := time.Now()
	if _, err := loginShellPath(slow, 200*time.Millisecond); err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("a hanging login shell: %v after %s", err, time.Since(start))
	}
}
