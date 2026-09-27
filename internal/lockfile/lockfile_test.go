package lockfile

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
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

func TestWaitBlocksUntilReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l")
	release, _ := Acquire(path)
	got := make(chan func())
	go func() { r, _ := Wait(path); got <- r }()
	select {
	case <-got:
		t.Fatal("Wait returned while locked")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case r := <-got:
		r()
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after release")
	}
}
