//go:build !windows

package static

import (
	"io/fs"
	"os"
	"syscall"
)

func skinOwned(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0022 == 0
}
