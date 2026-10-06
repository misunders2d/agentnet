//go:build !windows

package client

import (
	"os"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestFixtureStoreRestrictsSharedHome(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0777); err != nil {
		t.Fatal(err)
	}
	seedFixtureStore(t, home)
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("fixture home permissions = %o, want 700", info.Mode().Perm())
	}
	_, path := paths(home)
	if _, err := secfile.Read(path); err != nil {
		t.Fatalf("fixture permissions: %v", err)
	}
}
