package notify

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// lastID is the server's id of this process's last notification: each new
// one replaces it rather than stacking another banner.
var (
	mu     sync.Mutex
	lastID string
)

// Show sends a silent notification through the session's freedesktop
// notification server with notify-send (libnotify 0.7.9 or later, for
// --print-id and --replace-id). Callers pass fixed text that does not
// start with "-".
func Show(title, body string) error {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && os.Getenv("XDG_RUNTIME_DIR") == "" {
		return errors.New("no desktop session (neither DBUS_SESSION_BUS_ADDRESS nor XDG_RUNTIME_DIR is set)")
	}
	if _, err := exec.LookPath("notify-send"); err != nil {
		return errors.New("notify-send not found (install libnotify)")
	}
	mu.Lock()
	defer mu.Unlock()
	args := []string{"--app-name=AgentNet", "--hint=boolean:suppress-sound:true", "--print-id"}
	if lastID != "" {
		args = append(args, "--replace-id="+lastID)
	}
	out, err := run("notify-send", append(args, title, body)...)
	if err != nil {
		return err
	}
	if id := strings.TrimSpace(out); id != "" {
		if _, err := strconv.ParseUint(id, 10, 32); err == nil {
			lastID = id
		}
	}
	return nil
}
