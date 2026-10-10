package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A transport-only run must not perform harness recovery/cleanup, even with
// leftover worker artifacts in a human device home.
func TestHumanOnlyRunSkipsWorkerRecovery(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	a := enrolledAt(t, "https://127.0.0.1:1", "")
	defer a.Close()
	marker := filepath.Join(a.home, outFilePrefix+"human-only-regression")
	if err := os.WriteFile(marker, []byte("retained worker artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Run(ctx, RunOptions{HumanOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("worker recovery touched artifact: %v", err)
	}
	if a.kicksLive.Load() {
		t.Fatal("human-only transport started worker socket")
	}
}
