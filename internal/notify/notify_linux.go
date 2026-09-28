package notify

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lastID is the server's id of this process's last notification: each new
// one replaces it rather than stacking another banner. waiting is the
// notify-send still waiting for a click on it, if any.
var (
	mu      sync.Mutex
	lastID  string
	waiting *exec.Cmd
)

// Show sends a silent notification through the session's freedesktop
// notification server with notify-send (libnotify 0.7.9 or later, for
// --print-id and --replace-id). Callers pass fixed text that does not
// start with "-".
func Show(title, body string) error { return ShowAction(title, body, nil) }

// ShowAction is Show where clicking the notification calls onClick, in its
// own goroutine (libnotify 0.7.10 or later, for --action). It returns as
// soon as the server has the notification. At most one notification waits
// for a click: a newer one, or Close, stops the previous listener first,
// so a replaced banner can never fire twice.
func ShowAction(title, body string, onClick func()) error {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && os.Getenv("XDG_RUNTIME_DIR") == "" {
		return errors.New("no desktop session (neither DBUS_SESSION_BUS_ADDRESS nor XDG_RUNTIME_DIR is set)")
	}
	if _, err := exec.LookPath("notify-send"); err != nil {
		return errors.New("notify-send not found (install libnotify)")
	}
	mu.Lock()
	defer mu.Unlock()
	stopWaiting()
	args := []string{"--app-name=AgentNet", "--hint=boolean:suppress-sound:true", "--print-id"}
	if lastID != "" {
		args = append(args, "--replace-id="+lastID)
	}
	if onClick == nil {
		out, err := run("notify-send", append(args, title, body)...)
		if err != nil {
			return err
		}
		setLastID(strings.TrimSpace(out))
		return nil
	}
	args = append(args, "--action=default=Open", title, body)
	cmd := exec.Command("notify-send", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errors.New("notify-send: " + err.Error())
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var id string
	select {
	case id = <-lines:
	case <-time.After(timeout):
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32); err != nil {
		cmd.Process.Kill()
		go drain(lines, cmd)
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return errors.New("notify-send did not report a notification id: " + msg)
	}
	setLastID(strings.TrimSpace(id))
	waiting = cmd
	go func() {
		for line := range lines {
			if strings.TrimSpace(line) == "default" {
				go onClick()
			}
		}
		cmd.Wait()
		mu.Lock()
		if waiting == cmd {
			waiting = nil
		}
		mu.Unlock()
	}()
	return nil
}

func setLastID(id string) {
	if _, err := strconv.ParseUint(id, 10, 32); err == nil {
		lastID = id
	}
}

// drain reaps a notify-send that was given up on.
func drain(lines <-chan string, cmd *exec.Cmd) {
	for range lines {
	}
	cmd.Wait()
}

// stopWaiting ends the listener of the previous notification. Callers hold
// mu. Stopping notify-send does not matter to the banner: the next one
// replaces it by id, and a clicked or closed one needs no listener.
func stopWaiting() {
	if waiting != nil {
		waiting.Process.Kill()
		waiting = nil
	}
}

// Close stops listening for clicks.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	stopWaiting()
}
