package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Without a desktop session nothing is started and the reason is returned.
func TestHeadlessLinux(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("PATH", "") // a notifier must not be found or run either way
	if err := Show("AgentNet", "test"); err == nil {
		t.Fatal("headless Show succeeded")
	}
}

// Notifications are silent and each replaces the previous one.
func TestNotifySendArgs(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s|' \"$a\" >> " + log + "; done\necho >> " + log + "\necho 42\n"
	os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o700)
	t.Setenv("PATH", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	lastID = ""
	t.Cleanup(func() { lastID = "" })
	for range 2 {
		if err := Show("AgentNet", "2 requests"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	first := "--app-name=AgentNet|--hint=boolean:suppress-sound:true|--print-id|AgentNet|2 requests|"
	second := "--app-name=AgentNet|--hint=boolean:suppress-sound:true|--print-id|--replace-id=42|AgentNet|2 requests|"
	if len(lines) != 2 || lines[0] != first || lines[1] != second {
		t.Fatalf("notify-send calls:\n%s", data)
	}
}

// clickStub is a notify-send that prints an id, then waits; writing
// "click" to $STUB_DIR/trigger-<pid> makes it print the default action.
// It records its pid and whether it is still alive.
const clickStub = `#!/bin/sh
d="$STUB_DIR"
echo $$ >> "$d/pids"
for a in "$@"; do printf '%s|' "$a" >> "$d/args"; done; echo >> "$d/args"
echo 7
while :; do
  if [ -f "$d/trigger-$$" ]; then rm -f "$d/trigger-$$"; echo default; fi
  sleep 0.05
done
`

func stubPids(t *testing.T, dir string) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(dir, "pids"))
	return strings.Fields(string(data))
}

func alive(pid string) bool {
	_, err := os.Stat("/proc/" + pid)
	return err == nil
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

// A click runs the callback; a newer notification stops the previous
// listener first, so an old banner never fires; Close stops the last one.
func TestNotifyClick(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notify-send"), []byte(clickStub), 0o700)
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("STUB_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	lastID = ""
	t.Cleanup(func() { Close(); lastID = "" })

	clicks := make(chan string, 4)
	if err := ShowAction("AgentNet", "1 request", func() { clicks <- "first" }); err != nil {
		t.Fatal(err)
	}
	first := stubPids(t, dir)[0]
	os.WriteFile(filepath.Join(dir, "trigger-"+first), nil, 0o600)
	select {
	case c := <-clicks:
		if c != "first" {
			t.Fatalf("click: %s", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("click did not reach the callback")
	}

	if err := ShowAction("AgentNet", "2 requests", func() { clicks <- "second" }); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "previous listener stopped", func() bool { return !alive(first) })
	second := stubPids(t, dir)[1]
	os.WriteFile(filepath.Join(dir, "trigger-"+first), nil, 0o600) // a stale banner cannot fire
	os.WriteFile(filepath.Join(dir, "trigger-"+second), nil, 0o600)
	select {
	case c := <-clicks:
		if c != "second" {
			t.Fatalf("click after replacement: %s", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second click lost")
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	want0 := "--app-name=AgentNet|--hint=boolean:suppress-sound:true|--print-id|--action=default=Open|AgentNet|1 request|"
	want1 := "--app-name=AgentNet|--hint=boolean:suppress-sound:true|--print-id|--replace-id=7|--action=default=Open|AgentNet|2 requests|"
	if len(lines) != 2 || lines[0] != want0 || lines[1] != want1 {
		t.Fatalf("notify-send calls:\n%s", args)
	}

	// A plain notification also replaces (and stops) the listener.
	os.WriteFile(filepath.Join(dir, "notify-send"), []byte("#!/bin/sh\necho 7\n"), 0o700)
	if err := Show("AgentNet", "update"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "listener stopped by a plain notification", func() bool { return !alive(second) })
	select {
	case c := <-clicks:
		t.Fatalf("unexpected click %s", c)
	case <-time.After(200 * time.Millisecond):
	}
}

// Close stops a waiting listener: nothing is left running.
func TestNotifyCloseStopsListener(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notify-send"), []byte(clickStub), 0o700)
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("STUB_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	lastID = ""
	t.Cleanup(func() { lastID = "" })
	if err := ShowAction("AgentNet", "1 request", func() {}); err != nil {
		t.Fatal(err)
	}
	pid := stubPids(t, dir)[0]
	Close()
	waitUntil(t, "listener gone after Close", func() bool { return !alive(pid) })
}
