package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func claudeHooksOutput(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old; r.Close(); w.Close() }()
	e = runHooks(home, args)
	w.Close()
	data, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(data), e
}

func TestClaudeChannelDispatchShowReadOnlyAndPassiveBytes(t *testing.T) {
	claudeTestNode(t)
	home := filepath.Join(t.TempDir(), "absent-agent-home")
	file := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"model":"native-original","permissions":{"allow":["Bash(ls)"]}}`)
	os.WriteFile(file, original, 0600)
	out, e := claudeHooksOutput(t, home, "show", "claude", "--channel", "--file", file)
	if e != nil {
		t.Fatal(e)
	}
	var result map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(out))
	if e = dec.Decode(&result); e != nil || len(result) != 2 || result["settings"] == nil || result["mcpConfig"] == nil {
		t.Fatalf("not one settings/MCP object %v", e)
	}
	if e = dec.Decode(new(any)); e != io.EOF {
		t.Fatal("show emitted more than one JSON object")
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, original) {
		t.Fatal("show changed settings")
	}
	if _, e = os.Stat(home); !os.IsNotExist(e) {
		t.Fatal("show created private home/assets")
	}
	plain, e := claudeHooksOutput(t, home, "show", "claude", "--file", file)
	if e != nil {
		t.Fatal(e)
	}
	command, _ := hookCommand(home, "claude")
	fragment, _ := mergeHooks(nil, "claude", command)
	want, _ := json.MarshalIndent(fragment, "", "  ")
	if plain != string(want)+"\n" {
		t.Fatal("passive show bytes changed")
	}
	// A literal --file value is not interpreted as an opt-in flag.
	literal, e := claudeHooksOutput(t, home, "show", "claude", "--file", "--channel")
	if e != nil || literal != plain {
		t.Fatal("literal file path gained channel authority")
	}
}

func TestClaudeChannelDispatchInstallIdempotentAndRemove(t *testing.T) {
	claudeTestNode(t)
	home := t.TempDir()
	file := filepath.Join(t.TempDir(), "settings.json")
	original := `{"model":"native-original","permissions":{"allow":["Bash(ls)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-guard"}]}]}}`
	os.WriteFile(file, []byte(original), 0640)
	out, e := claudeHooksOutput(t, home, "install", "claude", "--file", file, "--channel")
	if e != nil || !strings.Contains(out, "NOT enabled") || !strings.Contains(out, "--dangerously-load-development-channels server:agentnet") || !strings.Contains(out, `"mcpServers"`) {
		t.Fatalf("install/activation distinction absent: %v", e)
	}
	first, _ := os.ReadFile(file)
	if !strings.Contains(string(first), "native-original") || !strings.Contains(string(first), "user-guard") || !strings.Contains(string(first), "Bash(ls)") {
		t.Fatal("native settings/permissions/hooks lost")
	}
	if stat, _ := os.Stat(file); stat.Mode().Perm() != 0640 {
		t.Fatal("settings mode changed")
	}
	asset := filepath.Join(home, "hooks", "claude-channel", "agentnet.bundle.mjs")
	if _, e = os.Stat(asset); e != nil {
		t.Fatal(e)
	}
	// Existing metadata is already up to date, but missing local channel files
	// still must be restored and its standard fragment shown.
	os.Remove(asset)
	out, e = claudeHooksOutput(t, home, "install", "claude", "--channel", "--file", file)
	if e != nil || !strings.Contains(out, "already up to date") || !strings.Contains(out, `"mcpServers"`) {
		t.Fatalf("up-to-date metadata skipped channel assets/fragment: %v", e)
	}
	if _, e = os.Stat(asset); e != nil {
		t.Fatal(e)
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(first, got) {
		t.Fatal("idempotent metadata changed")
	}
	backups, _ := filepath.Glob(file + ".agentnet-backup-*")
	if len(backups) != 1 {
		t.Fatal("idempotent channel install duplicated settings backup")
	}
	foreign := filepath.Join(filepath.Dir(asset), "keep-user-file")
	os.WriteFile(foreign, []byte("not owned"), 0600)
	t.Setenv("PATH", "")
	if _, e = claudeHooksOutput(t, home, "remove", "claude", "--channel", "--file", file); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(asset); !os.IsNotExist(e) {
		t.Fatal("owned bundle not removed")
	}
	if _, e = os.Stat(foreign); e != nil {
		t.Fatal("foreign file removed")
	}
	last, _ := os.ReadFile(file)
	if strings.Contains(string(last), "hook claude") || !strings.Contains(string(last), "user-guard") || !strings.Contains(string(last), "Bash(ls)") {
		t.Fatal("remove changed non-AgentNet metadata/permissions")
	}
}

func TestClaudeChannelDispatchValidationBeforeMutations(t *testing.T) {
	claudeTestNode(t)
	for _, bad := range []string{`{"hooks":[]}`, `{"hooks":{"SessionStart":{}}}`, `{`, `[]`} {
		home := filepath.Join(t.TempDir(), "absent-agent-home")
		file := filepath.Join(t.TempDir(), "settings.json")
		os.WriteFile(file, []byte(bad), 0600)
		if _, e := claudeHooksOutput(t, home, "install", "claude", "--channel", "--file", file); e == nil {
			t.Fatal("bad settings accepted")
		}
		if got, _ := os.ReadFile(file); string(got) != bad {
			t.Fatal("invalid settings changed")
		}
		if _, e := os.Stat(home); !os.IsNotExist(e) {
			t.Fatal("invalid settings still created assets")
		}
	}
	home := t.TempDir()
	dir := filepath.Join(home, "hooks", "claude-channel")
	os.MkdirAll(dir, 0700)
	foreign := filepath.Join(dir, "LICENSES.txt")
	os.WriteFile(foreign, []byte("foreign"), 0600)
	file := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"permissions":{"allow":["Bash(ls)"]}}`)
	os.WriteFile(file, original, 0600)
	for _, action := range []string{"install", "remove"} {
		if _, e := claudeHooksOutput(t, home, action, "claude", "--channel", "--file", file); e == nil {
			t.Fatal("foreign asset accepted")
		}
		if got, _ := os.ReadFile(file); !bytes.Equal(got, original) {
			t.Fatal("foreign asset refusal still mutated settings")
		}
	}
	for _, args := range [][]string{
		{"install", "claude", "--channel", "--channel", "--file", file},
		{"install", "codex", "--channel", "--file", file},
		{"install", "pi", "--channel", "--file", file},
		{"install", "omp", "--channel", "--file", file},
		{"launch", "claude", "--channel", "--file", file},
		{"install", "claude", "--channel", "unknown", "--file", file},
	} {
		if _, e := claudeHooksOutput(t, home, args...); e == nil {
			t.Fatal("invalid channel args accepted")
		}
		if got, _ := os.ReadFile(file); !bytes.Equal(got, original) {
			t.Fatal("invalid args mutated settings")
		}
	}
}

func TestClaudeChannelDispatchPassiveNeedsNoNodeOrAssets(t *testing.T) {
	claudeTestNode(t)
	t.Setenv("PATH", "")
	home := t.TempDir()
	file := filepath.Join(t.TempDir(), "settings.json")
	for _, action := range []string{"show", "install", "remove"} {
		out, e := claudeHooksOutput(t, home, action, "claude", "--file", file)
		if e != nil || strings.Contains(out, "mcpServers") || strings.Contains(out, "channel files") {
			t.Fatalf("passive %s gained channel behavior: %v", action, e)
		}
	}
	if _, e := os.Stat(filepath.Join(home, "hooks", "claude-channel")); !os.IsNotExist(e) {
		t.Fatal("passive CLI installed SDK assets")
	}
}
