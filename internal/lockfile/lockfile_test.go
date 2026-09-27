package lockfile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSecondHolderRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	release, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	release()
	release2, err := Acquire(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}
