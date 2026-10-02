package lockfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A refused Acquire leaves no descriptor behind, and a release closes the
// holder's: the count of open files returns to where it started.
func TestRefusalAndReleaseLeakNoDescriptor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("counts /proc/self/fd")
	}
	count := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(ents)
	}
	path := filepath.Join(t.TempDir(), "l")
	before := count()
	release, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	held := count()
	if held != before+1 {
		t.Fatalf("holding the lock opened %d descriptors, want 1", held-before)
	}
	for range 3 {
		if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
			t.Fatalf("second holder: %v", err)
		}
	}
	if n := count(); n != held {
		t.Fatalf("refused attempts leaked %d descriptor(s)", n-held)
	}
	release()
	release() // idempotent
	if n := count(); n != before {
		t.Fatalf("release left %d descriptor(s) open", n-before)
	}
}

// The lock is the OS's: when the holding process exits without releasing,
// the next process acquires at once. The child is this test binary.
func TestChildProcessExitReleases(t *testing.T) {
	if os.Getenv("LOCKFILE_CHILD") != "" {
		if _, err := Acquire(os.Getenv("LOCKFILE_CHILD")); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("held\n")
		os.Stdout.Close()
		time.Sleep(200 * time.Millisecond) // long enough for the parent's refused attempt
		os.Exit(0)                         // no release call: the OS drops the lock
	}
	path := filepath.Join(t.TempDir(), "l")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestChildProcessExitReleases$")
	cmd.Env = append(os.Environ(), "LOCKFILE_CHILD="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil || string(buf) != "held\n" {
		t.Fatalf("child did not report holding: %q %v", buf, err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock held by the child was taken: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("after the child's exit: %v", err)
	}
	release()
}
