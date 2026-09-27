package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
