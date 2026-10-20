//go:build !windows

package notify

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Execute the shipped request through a fake osascript even on Linux. This
// proves argv and errors, not AppleScript parsing or visible/audible delivery.
func TestAppleScriptNotificationArgsAndFailure(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	stub := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$NOTIFY_ARGS\"\nif [ \"$NOTIFY_FAIL\" = 1 ]; then echo 'fixture notifier refused' >&2; exit 7; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("NOTIFY_ARGS", args)
	t.Setenv("NOTIFY_FAIL", "")
	title, body := "AgentNet \"quoted\"", "Private text\nwith `code` and $(literal)"
	if err := showAppleScript(title, body); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv) sound name (item 3 of argv)", "-e", "end run", title, body, "Glass", ""}
	if actual := strings.Split(string(got), "\x00"); !reflect.DeepEqual(actual, want) {
		t.Fatalf("osascript argv: %q", actual)
	}
	t.Setenv("NOTIFY_FAIL", "1")
	if err := showAppleScript(title, body); err == nil || !strings.Contains(err.Error(), "fixture notifier refused") || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("notifier failure: %v", err)
	}
}
