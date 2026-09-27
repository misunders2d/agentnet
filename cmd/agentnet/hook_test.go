package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Installing keeps every other setting and hook, is idempotent, backs up
// the old file, and remove takes out only AgentNet's handlers.
func TestHooksInstallMerge(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	orig := `{
  "model": "opus",
  "hooks": {
    "Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "notify-me"}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh"}]}]
  },
  "permissions": {"allow": ["Bash(ls)"]}
}`
	os.WriteFile(file, []byte(orig), 0o640)
	home := t.TempDir()
	if refusedOnWindows(t, home, file, orig) {
		return
	}
	if err := runHooks(home, []string{"install", "claude", "--file", file}); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(file)
	var cfg map[string]any
	if err := json.Unmarshal(first, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "opus" || cfg["permissions"] == nil {
		t.Fatalf("other settings lost: %s", first)
	}
	hooks := cfg["hooks"].(map[string]any)
	if s := string(first); !strings.Contains(s, "notify-me") || !strings.Contains(s, "guard.sh") {
		t.Fatalf("other hooks lost: %s", first)
	}
	for _, event := range hookEvents {
		groups := hooks[event].([]any)
		last := groups[len(groups)-1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		if cmd := last["command"].(string); !strings.HasSuffix(cmd, "hook claude") || !strings.Contains(cmd, `--home '`+home+`'`) {
			t.Fatalf("%s handler %q", event, cmd)
		}
	}
	if len(hooks["Stop"].([]any)) != 2 {
		t.Fatalf("Stop groups: %v", hooks["Stop"])
	}
	backups, _ := filepath.Glob(file + ".agentnet-backup-*")
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != orig {
		t.Fatal("backup differs from the original")
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %v", info.Mode())
	}

	// Again: nothing changes, no new backup.
	if err := runHooks(home, []string{"install", "claude", "--file", file}); err != nil {
		t.Fatal(err)
	}
	if second, _ := os.ReadFile(file); !bytes.Equal(first, second) {
		t.Fatalf("second install changed the file:\n%s", second)
	}
	if b, _ := filepath.Glob(file + ".agentnet-backup-*"); len(b) != 1 {
		t.Fatalf("second install wrote a backup: %v", b)
	}

	if err := runHooks(home, []string{"remove", "claude", "--file", file}); err != nil {
		t.Fatal(err)
	}
	removed, _ := os.ReadFile(file)
	if strings.Contains(string(removed), "hook claude") || !strings.Contains(string(removed), "notify-me") || !strings.Contains(string(removed), "guard.sh") {
		t.Fatalf("after remove: %s", removed)
	}
}

func TestHooksRefuseBadConfigAndUnsupportedHarness(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hooks.json")
	os.WriteFile(file, []byte(`{"hooks": [1, 2]}`), 0o600)
	if err := runHooks(t.TempDir(), []string{"install", "codex", "--file", file}); err == nil {
		t.Fatal("installed into a malformed hooks value")
	}
	if b, _ := os.ReadFile(file); string(b) != `{"hooks": [1, 2]}` {
		t.Fatalf("malformed file changed: %s", b)
	}
	os.WriteFile(file, []byte(`not json`), 0o600)
	if err := runHooks(t.TempDir(), []string{"install", "codex", "--file", file}); err == nil {
		t.Fatal("installed into invalid JSON")
	}
	if err := runHooks(t.TempDir(), []string{"install", "pi"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("pi: %v", err)
	}
	// A new file is created owner-only.
	fresh := filepath.Join(t.TempDir(), "sub", "hooks.json")
	if runtime.GOOS == "windows" {
		if err := runHooks(t.TempDir(), []string{"install", "codex", "--file", fresh}); err == nil || !strings.Contains(err.Error(), "not supported on Windows") {
			t.Fatalf("Windows install: %v", err)
		}
		if _, err := os.Stat(fresh); !os.IsNotExist(err) {
			t.Fatalf("Windows install created %s", fresh)
		}
		return
	}
	if err := runHooks(t.TempDir(), []string{"install", "codex", "--file", fresh}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(fresh); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new file: %v %v", info, err)
	}
}

// Without an enrolled agent the hook prints nothing and creates nothing,
// whatever it is given.
func TestHookWithoutAgentIsSilent(t *testing.T) {
	home := filepath.Join(t.TempDir(), "none")
	for _, in := range []string{`{"session_id":"s","hook_event_name":"UserPromptSubmit"}`, `garbage`, ``, `{"hook_event_name":"Stop"}`} {
		var out bytes.Buffer
		if err := runHook(home, []string{"claude"}, strings.NewReader(in), &out); err != nil || out.Len() != 0 {
			t.Fatalf("%q: %v %q", in, err, out.String())
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("home created: %v", err)
	}
}

// Only handlers exactly like the ones AgentNet writes are AgentNet's; a
// malformed known event is refused untouched; quick successive changes keep
// every backup.
func TestHooksOwnershipAndBackups(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	orig := `{"hooks": {"Stop": [{"hooks": [
  {"type": "command", "command": "'/opt/agentnet/custom_guard.sh' --home '/x' hook claude", "timeout": 10},
  {"type": "command", "command": "\"/usr/bin/agentnet\" --home \"/x\" hook claude", "timeout": 10},
  {"type": "command", "command": "'/usr/bin/agentnet' --home '/x' hook claude", "timeout": 10, "note": "mine"}
]}]}}`
	os.WriteFile(file, []byte(orig), 0o600)
	home := t.TempDir()
	if refusedOnWindows(t, home, file, orig) {
		return
	}
	if err := runHooks(home, []string{"install", "claude", "--file", file}); err != nil {
		t.Fatal(err)
	}
	if err := runHooks(home, []string{"remove", "claude", "--file", file}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(file)
	for _, keep := range []string{"custom_guard.sh", `\"/usr/bin/agentnet\"`, `"note": "mine"`} {
		if !strings.Contains(string(after), keep) {
			t.Fatalf("removed a handler that is not AgentNet's (%s):\n%s", keep, after)
		}
	}
	backups, _ := filepath.Glob(file + ".agentnet-backup-*")
	if len(backups) != 2 {
		t.Fatalf("backups %v", backups)
	}
	found := false
	for _, b := range backups {
		if data, _ := os.ReadFile(b); string(data) == orig {
			found = true
		}
	}
	if !found {
		t.Fatal("the original is no longer in any backup")
	}

	for _, bad := range []string{`{"hooks": {"Stop": {"hooks": []}}}`, `{"hooks": {"SessionStart": "x"}}`} {
		os.WriteFile(file, []byte(bad), 0o600)
		if err := runHooks(home, []string{"install", "claude", "--file", file}); err == nil {
			t.Fatalf("installed into %s", bad)
		}
		if b, _ := os.ReadFile(file); string(b) != bad {
			t.Fatalf("malformed file changed: %s", b)
		}
	}
}

// The hook command survives a POSIX shell unchanged, whatever the paths
// contain apart from a quote.
func TestHookCommandQuoting(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := hookCommand(t.TempDir(), "claude"); err == nil {
			t.Fatal("Windows hook command not refused")
		}
		return
	}
	home := filepath.Join(t.TempDir(), "a $HOME `id` \\x \" b")
	cmd, err := hookCommand(home, "codex")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", "set -- "+cmd+`; printf '%s\n' "$@"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	want := strings.Join([]string{exe, "--home", home, "hook", "codex"}, "\n") + "\n"
	if string(out) != want {
		t.Fatalf("shell saw\n%s\nwant\n%s", out, want)
	}
	if _, err := hookCommand(filepath.Join(t.TempDir(), "it's"), "claude"); err == nil {
		t.Fatal("single quote accepted")
	}
}

// refusedOnWindows checks that installing on Windows is refused with the
// file left as it was, and reports whether this is Windows.
func refusedOnWindows(t *testing.T, home, file, orig string) bool {
	t.Helper()
	if runtime.GOOS != "windows" {
		return false
	}
	if err := runHooks(home, []string{"install", "claude", "--file", file}); err == nil || !strings.Contains(err.Error(), "not supported on Windows") {
		t.Fatalf("Windows install: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != orig {
		t.Fatal("Windows install changed the file")
	}
	return true
}

// The merge itself is the same on every platform: adding is idempotent,
// removing restores the rest, and only the exact AgentNet shape is removed.
func TestMergeHooksPortable(t *testing.T) {
	const cmd = "'/usr/local/bin/agentnet' --home '/h' hook claude"
	parse := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	orig := `{"x": 1, "hooks": {"Stop": [{"hooks": [
		{"type": "command", "command": "'/opt/agentnet/guard.sh' --home '/h' hook claude", "timeout": 10}]}]}}`
	once, err := mergeHooks(parse(orig), "claude", cmd)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(once)
	twice, _ := mergeHooks(parse(string(a)), "claude", cmd)
	b, _ := json.Marshal(twice)
	if string(a) != string(b) || strings.Count(string(a), "hook claude") != 5 {
		t.Fatalf("not idempotent:\n%s\n%s", a, b)
	}
	removed, _ := mergeHooks(parse(string(b)), "claude", "")
	r, _ := json.Marshal(removed)
	want, _ := json.Marshal(parse(orig))
	if string(r) != string(want) {
		t.Fatalf("remove:\n%s\nwant\n%s", r, want)
	}
	if _, err := mergeHooks(parse(`{"hooks": {"Stop": "x"}}`), "claude", cmd); err == nil {
		t.Fatal("malformed known event accepted")
	}
}
