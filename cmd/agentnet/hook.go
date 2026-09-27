package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// hookHarnesses are the harnesses whose hook contracts (stdin event JSON,
// hookSpecificOutput.additionalContext, Stop decision "block") AgentNet
// implements. Others are not claimed.
var hookHarnesses = []string{"claude", "codex"}

// hookEvents are the events AgentNet installs.
var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"}

// runHook is called by a harness hook. It never fails loudly: without an
// enrolled agent, or on any error, it prints nothing and exits 0, so a
// broken or missing AgentNet never disturbs the session. It creates nothing
// in a missing home.
func runHook(home string, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || !slices.Contains(hookHarnesses, args[0]) {
		return fmt.Errorf("usage: hook %s (reads the hook event on stdin)", strings.Join(hookHarnesses, "|"))
	}
	var in struct {
		SessionID      string `json:"session_id"`
		HookEventName  string `json:"hook_event_name"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 64<<20)).Decode(&in); err != nil {
		return nil
	}
	if !slices.Contains(hookEvents, in.HookEventName) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(home, "identity.json")); err != nil {
		return nil
	}
	a, err := client.Open(home)
	if err != nil {
		return nil
	}
	defer a.Close()
	at, err := a.Attention(client.HookEvent{Harness: args[0], Session: in.SessionID, Event: in.HookEventName, StopActive: in.StopHookActive})
	if err != nil || at.Text == "" {
		if err == nil {
			at.Commit() // records a new session's starting point
		}
		return nil
	}
	var out any
	if in.HookEventName == "Stop" {
		out = map[string]string{"decision": "block", "reason": at.Text}
	} else {
		out = map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": in.HookEventName, "additionalContext": at.Text}}
	}
	data, _ := json.Marshal(out)
	if _, err := stdout.Write(append(data, '\n')); err != nil {
		return nil // not shown, so not consumed
	}
	at.Commit()
	return nil
}

// hookConfigPath is where each harness reads user-level hooks.
func hookConfigPath(harness string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if harness == "codex" {
		if dir := os.Getenv("CODEX_HOME"); dir != "" {
			return filepath.Join(dir, "hooks.json"), nil
		}
		return filepath.Join(home, ".codex", "hooks.json"), nil
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// hookCommand is the command line a harness runs for AgentNet's hook.
func hookCommand(home, harness string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return "", err
	}
	if home, err = filepath.Abs(home); err != nil {
		return "", err
	}
	if strings.ContainsRune(exe+home, '"') {
		return "", errors.New(`paths with '"' are not supported in hook commands`)
	}
	return `"` + exe + `" --home "` + home + `" hook ` + harness, nil
}

// isAgentNetHook recognizes a handler this command installed.
func isAgentNetHook(handler any, harness string) bool {
	h, ok := handler.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := h["command"].(string)
	return strings.Contains(cmd, "agentnet") && strings.HasSuffix(cmd, " hook "+harness)
}

// mergeHooks returns config with every AgentNet handler for harness removed
// and, if command is set, one AgentNet handler per event added. Everything
// else in config is kept.
func mergeHooks(config map[string]any, harness, command string) (map[string]any, error) {
	if config == nil {
		config = map[string]any{}
	}
	hooks, ok := config["hooks"].(map[string]any)
	if config["hooks"] != nil && !ok {
		return nil, errors.New(`"hooks" is not an object`)
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue // not ours to interpret
		}
		var kept []any
		for _, g := range groups {
			group, ok := g.(map[string]any)
			handlers, hok := group["hooks"].([]any)
			if !ok || !hok {
				kept = append(kept, g)
				continue
			}
			var own []any
			for _, h := range handlers {
				if !isAgentNetHook(h, harness) {
					own = append(own, h)
				}
			}
			if len(own) == len(handlers) {
				kept = append(kept, g)
			} else if len(own) > 0 {
				copied := map[string]any{}
				for k, v := range group {
					copied[k] = v
				}
				copied["hooks"] = own
				kept = append(kept, copied)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if command != "" {
		for _, event := range hookEvents {
			groups, _ := hooks[event].([]any)
			hooks[event] = append(groups, map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 10}},
			})
		}
	}
	if len(hooks) == 0 {
		delete(config, "hooks")
	} else {
		config["hooks"] = hooks
	}
	return config, nil
}

// runHooks shows, installs or removes AgentNet's hooks in a harness's
// user-level hook configuration.
func runHooks(home string, args []string) error {
	usage := errors.New("usage: hooks show|install|remove claude|codex [--file PATH]")
	if len(args) < 2 {
		return usage
	}
	action, harness := args[0], args[1]
	if !slices.Contains(hookHarnesses, harness) {
		return fmt.Errorf("hooks for %q are not supported: only %s hook contracts are implemented; other agents can use `agentnet inbox` and `agentnet conversation`", harness, strings.Join(hookHarnesses, " and "))
	}
	file := ""
	switch rest := args[2:]; {
	case len(rest) == 2 && rest[0] == "--file":
		file = rest[1]
	case len(rest) != 0:
		return usage
	}
	command, err := hookCommand(home, harness)
	if err != nil {
		return err
	}
	if action == "show" {
		fragment, _ := mergeHooks(nil, harness, command)
		data, _ := json.MarshalIndent(fragment, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if action != "install" && action != "remove" {
		return usage
	}
	if file == "" {
		if file, err = hookConfigPath(harness); err != nil {
			return err
		}
	}
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var config map[string]any
	if len(strings.TrimSpace(string(old))) > 0 {
		if err := json.Unmarshal(old, &config); err != nil {
			return fmt.Errorf("%s is not a JSON object (%v); nothing changed", file, err)
		}
	}
	if action == "remove" {
		command = ""
	}
	config, err = mergeHooks(config, harness, command)
	if err != nil {
		return fmt.Errorf("%s: %v; nothing changed", file, err)
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	data = append(data, '\n')
	if string(data) == string(old) {
		fmt.Printf("%s already up to date\n", file)
		return nil
	}
	if len(old) > 0 {
		backup := file + ".agentnet-backup-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, old, 0o600); err != nil {
			return err
		}
		fmt.Printf("backup %s\n", backup)
	}
	if err := writeFileAtomic(file, data); err != nil {
		return err
	}
	fmt.Printf("%s %s: AgentNet hooks for %s (%s)\n", map[string]string{"install": "updated", "remove": "removed from"}[action], file, harness, strings.Join(hookEvents, ", "))
	if action == "install" {
		if harness == "codex" {
			fmt.Println("next: in Codex open /hooks, review and trust the four AgentNet hooks, then start a new session; untrusted hooks do not run")
		} else {
			fmt.Println("next: start a new Claude Code session (running sessions may not pick up the change)")
		}
	}
	return nil
}

// writeFileAtomic replaces path with data, keeping an existing file's mode.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentnet-hooks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
