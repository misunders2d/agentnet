//go:build !windows

package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// privateSockDir returns TMPDIR/agentnet-UID, creating it owner-only, and
// refuses one that is a link, owned by someone else, or open to others (it
// may live in a shared /tmp).
func privateSockDir() (string, error) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("agentnet-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is not a private directory of this user", dir)
	}
	return dir, nil
}
