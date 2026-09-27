package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
		if cmd := last["command"].(string); !strings.HasSuffix(cmd, "hook claude") || !strings.Contains(cmd, `--home "`+home+`"`) {
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
