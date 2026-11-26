package client

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// findCodexDesktopEngine is the running Codex Desktop engine on home: this
// user's bundled codex app-server process whose parent is that install's
// own ChatGPT executable, with home as its Codex home.
func findCodexDesktopEngine(home string) (codexEngine, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return codexEngine{}, err
	}
	var found []codexEngine
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid < 2 {
			continue
		}
		info, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
		if err != nil || !ownedByMe(info) {
			continue
		}
		parent, ticks, exe, err := codexProcess(pid)
		if err != nil || filepath.Base(exe) != "codex" {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !hasArg(raw, "app-server") {
			continue
		}
		_, _, parentExe, err := codexProcess(parent)
		if err != nil || parentExe != filepath.Join(filepath.Dir(filepath.Dir(exe)), "ChatGPT") {
			continue
		}
		if engineHome(pid) != home {
			continue
		}
		found = append(found, codexEngine{PID: pid, Ticks: ticks, Exe: exe})
	}
	switch len(found) {
	case 0:
		return codexEngine{}, errors.New("Codex Desktop is not running. Open it and that chat, then ask again")
	case 1:
		return found[0], nil
	}
	return codexEngine{}, errors.New("more than one Codex Desktop is running on this Codex home")
}

func hasArg(cmdline []byte, arg string) bool {
	for _, a := range bytes.Split(cmdline, []byte{0}) {
		if string(a) == arg {
			return true
		}
	}
	return false
}

// engineHome is the canonical Codex home of pid: its CODEX_HOME, or the
// default ~/.codex under its HOME.
func engineHome(pid int) string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return ""
	}
	var home, codexHome string
	for _, kv := range bytes.Split(raw, []byte{0}) {
		if v, ok := bytes.CutPrefix(kv, []byte("CODEX_HOME=")); ok {
			codexHome = string(v)
		} else if v, ok := bytes.CutPrefix(kv, []byte("HOME=")); ok {
			home = string(v)
		}
	}
	if codexHome == "" {
		if home == "" {
			return ""
		}
		codexHome = filepath.Join(home, ".codex")
	}
	real, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		return ""
	}
	abs, _ := filepath.Abs(real)
	return abs
}

// fileWrites wakes on writes to one file (inotify); no timer.
type fileWrites struct {
	wake    int
	changes chan struct{}
}

func watchFileWrites(path string) (*fileWrites, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	if _, err = unix.InotifyAddWatch(fd, path, unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF); err != nil {
		unix.Close(fd)
		return nil, err
	}
	var p [2]int
	if err = unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		unix.Close(fd)
		return nil, err
	}
	w := &fileWrites{wake: p[1], changes: make(chan struct{}, 1)}
	go func() {
		defer unix.Close(fd)
		defer unix.Close(p[0])
		buf := make([]byte, 4096)
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}, {Fd: int32(p[0]), Events: unix.POLLIN}}
		for {
			_, err := unix.Poll(fds, -1)
			if err == unix.EINTR {
				continue
			}
			if err != nil || fds[1].Revents != 0 {
				return
			}
			for {
				if n, err := unix.Read(fd, buf); n <= 0 || err != nil {
					break
				}
			}
			select {
			case w.changes <- struct{}{}:
			default:
			}
		}
	}()
	return w, nil
}

func (w *fileWrites) close() { unix.Close(w.wake) }
