package notify

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lastID is the server's id of this process's last notification: each new
// one replaces it rather than stacking another banner. current is the
// notify-send still waiting for a click on it, if any.
var (
	mu      sync.Mutex
	lastID  string
	current *listener
)

type listener struct{ cmd *exec.Cmd }

// idTimeout bounds the wait for notify-send to report the notification id.
var idTimeout = timeout

// dispatchHook, when set (tests only), runs after a click was read and
// before it is dispatched.
var dispatchHook func()

// Show sends a silent notification through the session's freedesktop
// notification server with notify-send (libnotify 0.7.9 or later, for
// --print-id and --replace-id). Callers pass fixed text that does not
// start with "-".
func Show(title, body string) error { return ShowAction(title, body, nil, nil) }

// ShowAction is Show with a click. argv, if set, is sent as the
// omarchy-exec-argv hint: Omarchy's notification shell stores it with the
// notification and runs it (as an argument list, no shell) when the popup
// or its history entry is clicked, after this process or the popup is long
// gone. onClick, if set, is the standard freedesktop default action
// (libnotify 0.7.10 or later): it works on any server, but only while the
// notification is live, and runs in its own goroutine. ShowAction returns as
// soon as the server has the notification. At most one notification waits
// for a click: a newer one, or Close, stops the previous listener, and a
// click it read but had not dispatched yet is dropped.
func ShowAction(title, body string, argv []string, onClick func()) error {
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
	if len(argv) > 0 {
		data, err := json.Marshal(argv)
		if err != nil {
			return err
		}
		args = append(args, "--hint=string:omarchy-exec-argv:"+string(data))
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
	case <-time.After(idTimeout):
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32); err != nil {
		// Stop it and let every copy finish before reading its error output.
		cmd.Process.Kill()
		for range lines {
		}
		cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return errors.New("notify-send did not report a notification id: " + msg)
	}
	setLastID(strings.TrimSpace(id))
	l := &listener{cmd: cmd}
	current = l
	go func() {
		for line := range lines {
			if strings.TrimSpace(line) != "default" {
				continue
			}
			if dispatchHook != nil {
				dispatchHook()
			}
			mu.Lock()
			live := current == l
			mu.Unlock()
			if live {
				go onClick()
			}
		}
		cmd.Wait()
		mu.Lock()
		if current == l {
			current = nil
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

// stopWaiting ends the listener of the previous notification. Callers hold
// mu. Stopping notify-send does not matter to the banner: the next one
// replaces it by id, and a clicked or closed one needs no listener.
func stopWaiting() {
	if current != nil {
		current.cmd.Process.Kill()
		current = nil
	}
}

// Close stops listening for clicks.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	stopWaiting()
}
