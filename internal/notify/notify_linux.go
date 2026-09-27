package notify

import (
	"errors"
	"os"
	"os/exec"
)

// Show sends a notification through the session's freedesktop notification
// server with notify-send (libnotify). Callers pass fixed text that does not
// start with "-".
func Show(title, body string) error {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && os.Getenv("XDG_RUNTIME_DIR") == "" {
		return errors.New("no desktop session (neither DBUS_SESSION_BUS_ADDRESS nor XDG_RUNTIME_DIR is set)")
	}
	if _, err := exec.LookPath("notify-send"); err != nil {
		return errors.New("notify-send not found (install libnotify)")
	}
	return run("notify-send", "--app-name=AgentNet", title, body)
}
