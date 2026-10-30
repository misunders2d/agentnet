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

// A holder that only checks the lock briefly does not refuse the waiter;
// one that keeps it does, after the window (MEL-698).
func TestAcquireWithinOutlastsBriefHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	brief, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(50*time.Millisecond, brief)
	release, err := AcquireWithin(path, 5*time.Second)
	if err != nil {
		t.Fatalf("lock after a brief holder: %v", err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock taken within the window is not held: %v", err)
	}
	release()

	held, _ := Acquire(path)
	defer held()
	start := time.Now()
	if _, err := AcquireWithin(path, 100*time.Millisecond); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock still held after the window: %v", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Fatalf("refused after %v, before the window ended", waited)
	}
}
