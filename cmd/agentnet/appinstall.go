package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// The Linux shell calls this before registering launchers or opening a home.
// It reopens the returned package, so the normal startup and updater both use
// the stable copy. No identity, daemon, or app registration is touched here.
func runAppInstall(args []string, out io.Writer) error {
	if runtime.GOOS != "linux" || bundledWith != "app" || len(args) != 2 {
		return errors.New("app-install is only used by the Linux AppImage")
	}
	path, err := installAppImage(args[0], args[1])
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(path)
}

func installAppImage(src, data string) (string, error) {
	if !filepath.IsAbs(src) || !filepath.IsAbs(data) {
		return "", errors.New("the AppImage and application data directory must have absolute paths")
	}
	st, err := os.Lstat(src)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return "", errors.New("the AppImage must be an executable regular file, not a link")
	}
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	var header [11]byte
	_, err = io.ReadFull(f, header[:])
	f.Close()
	if err != nil || !bytes.Equal(header[:4], []byte("\x7fELF")) || !bytes.Equal(header[8:], []byte{'A', 'I', 2}) {
		return "", errors.New("the source is not an AppImage")
	}
	dir := filepath.Join(data, "agentnet")
	dst := filepath.Join(dir, "AgentNet.AppImage")
	if info, e := os.Lstat(dir); e == nil && !info.IsDir() {
		return "", errors.New("the AgentNet application directory is not a regular directory")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	if filepath.Clean(src) == dst {
		return dst, nil // already installed; never reopen ourselves in a loop
	}
	if err = secfile.EnsureDir(dir); err != nil {
		return "", err
	}
	release, err := lockfile.Acquire(updateLockPath(dst))
	if err != nil {
		return "", errors.New("another AgentNet installation is running; try again when it finishes")
	}
	defer release()
	want, err := appUpdateSourceSum(src, "")
	if err != nil {
		return "", err
	}
	if info, e := os.Lstat(dst); e == nil {
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			if sum, e := appUpdateSourceSum(dst, want); e == nil && sum == want {
				return dst, nil
			}
		}
		return "", fmt.Errorf("an existing file at %s was preserved. Open the installed AgentNet and use Update AgentNet; to reinstall, move that file aside first", dst)
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	if err = copyAppImage(src, dst, want); err != nil {
		return "", err
	}
	return dst, nil
}

// Copy beside the destination and check it before publication. Link provides
// an atomic create-only publication: even a concurrently created destination
// is never overwritten. The original download remains available on failure.
func copyAppImage(src, dst, want string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	tmp, err := secfile.CreateTemp(filepath.Dir(dst), ".agentnet-appimage-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = io.Copy(tmp, f); err == nil {
		err = tmp.Chmod(0o700)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	sum, err := fileSum(tmp.Name())
	if err != nil || hex.EncodeToString(sum) != want {
		return errors.New("the AppImage copy did not pass its checksum check; the download was preserved")
	}
	if _, err = appUpdateSourceSum(src, want); err != nil {
		return err
	}
	if err = os.Link(tmp.Name(), dst); err != nil {
		return err
	}
	if _, err = appUpdateSourceSum(dst, want); err != nil {
		return err
	}
	return secfile.SyncDir(filepath.Dir(dst))
}
