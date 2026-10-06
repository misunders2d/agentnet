package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/secfile"
)

type appCommandStatus struct {
	Path    string `json:"cli_path"`
	State   string `json:"cli_state"`
	Problem string `json:"cli_problem,omitempty"`
}

type appCommandRecord struct {
	Path string `json:"path"`
	Sum  string `json:"sum"`
}

func appCommandPath(userHome string) string {
	if runtime.GOOS == "windows" {
		return ""
	} // NSIS adds its installed directory to user PATH.
	return filepath.Join(userHome, ".local", "bin", "agentnet")
}

// Only unchanged app-owned bytes or checksum-proven official bytes may be
// replaced without a person's explicit choice. Never execute an unknown copy.
func appOwnsCommand(ctx context.Context, src, dst, recordPath string) bool {
	have, err := fileSum(dst)
	if err != nil {
		return false
	}
	if want, err := fileSum(src); err == nil && bytes.Equal(have, want) {
		return true
	}
	var record appCommandRecord
	if b, err := secfile.Read(recordPath); err == nil && json.Unmarshal(b, &record) == nil && record.Path == dst && record.Sum == hex.EncodeToString(have) {
		return true
	}
	info, err := buildinfo.ReadFile(dst)
	if err != nil {
		return false
	}
	tag := ""
	for _, setting := range info.Settings {
		if setting.Key == "-ldflags" {
			for _, word := range strings.Fields(setting.Value) {
				const key = "github.com/misunders2d/agentnet/internal/protocol.Version="
				if strings.HasPrefix(word, key) {
					tag = strings.TrimPrefix(word, key)
				}
			}
		}
	}
	if _, ok := parseRelease(tag); !ok {
		return false
	}
	sums, err := releaseChecksums(ctx, tag)
	if err != nil {
		return false
	}
	return sums[assetName()] == hex.EncodeToString(have)
}

func installAppCommand(ctx context.Context, src, home, userHome string, replace bool) appCommandStatus {
	dst := appCommandPath(userHome)
	if dst == "" {
		return appCommandStatus{Path: src, State: "installed"}
	}
	state := appCommandStatus{Path: dst, State: "installed"}
	fail := func(err error) appCommandStatus {
		state.State = "error"
		state.Problem = "Could not install the agentnet command: " + err.Error()
		return state
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fail(err)
	}
	release, err := lockfile.Acquire(updateLockPath(dst))
	if err != nil {
		return fail(errors.New("another command update is running"))
	}
	defer release()
	recordPath := filepath.Join(home, "app-command.json")
	if st, err := os.Lstat(dst); err == nil {
		if (!st.Mode().IsRegular() && !(replace && st.Mode()&os.ModeSymlink != 0)) || (!replace && !appOwnsCommand(ctx, src, dst, recordPath)) {
			state.State = "custom"
			state.Problem = "Your own agentnet command is here. Keep it, or choose Replace command."
			return state
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	want, err := fileSum(src)
	if err != nil {
		return fail(err)
	}
	if st, statErr := os.Lstat(dst); statErr == nil && st.Mode().IsRegular() {
		if have, err := fileSum(dst); err == nil && bytes.Equal(have, want) {
			return finishAppCommand(home, userHome, dst, want, state)
		}
	}
	copied, err := stableCopy(src, filepath.Join(home, "app-command-stage"))
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(filepath.Join(home, "app-command-stage"))
	// Stage beside destination for atomic replacement, including different filesystems.
	data, err := os.ReadFile(copied)
	if err != nil {
		return fail(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".agentnet-app-*")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(0755)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	if _, err = os.Stat(dst); err == nil {
		if err = replaceExecutable(dst, tmp.Name()); err != nil {
			return fail(err)
		}
	} else if err = os.Rename(tmp.Name(), dst); err != nil {
		return fail(err)
	}
	sum, err := fileSum(dst)
	if err != nil {
		return fail(err)
	}
	return finishAppCommand(home, userHome, dst, sum, state)
}

func finishAppCommand(home, userHome, dst string, sum []byte, state appCommandStatus) appCommandStatus {
	b, _ := json.Marshal(appCommandRecord{Path: dst, Sum: hex.EncodeToString(sum)})
	if err := secfile.Write(filepath.Join(home, "app-command.json"), b); err != nil {
		state.State = "error"
		state.Problem = "The command location could not be saved: " + err.Error()
		return state
	}
	if err := ensureAppCommandPATH(userHome); err != nil {
		state.Problem = "Command installed; could not update your shell path: " + err.Error()
	}
	return state
}

func ensureAppCommandPATH(userHome string) error {
	block := "\n# AgentNet command\ncase :$PATH: in *:\"$HOME/.local/bin\":*) ;; *) export PATH=\"$HOME/.local/bin:$PATH\" ;; esac\n"
	names := []string{".profile", ".zprofile"}
	if _, err := os.Stat(filepath.Join(userHome, ".bash_profile")); err == nil {
		names = append(names, ".bash_profile")
	}
	for _, name := range names {
		path := filepath.Join(userHome, name)
		b, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if bytes.Contains(b, []byte("# AgentNet command\n")) {
			continue
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(block)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (r *appRunner) installCommand(replace bool) appCommandStatus {
	src, err := os.Executable()
	if err != nil {
		return appCommandStatus{State: "error", Problem: err.Error()}
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return appCommandStatus{State: "error", Problem: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return installAppCommand(ctx, src, r.home, userHome, replace)
}
