//go:build windows

package client

import (
	"os"
	"path/filepath"

	"github.com/misunders2d/agentnet/internal/secfile"
)

// privateSockDir returns %TEMP%\agentnet (the user's own temporary
// directory), restricted to the current user.
func privateSockDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "agentnet")
	return dir, secfile.EnsureDir(dir)
}
