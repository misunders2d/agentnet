package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

// piEvents are the events AgentNet's Pi extension sends (agentnet-pi.ts):
// the session lifecycle, Idle for arrivals while Pi waits, and Ack once it
// has handed the text to Pi.
var piEvents = []string{"SessionStart", "UserPromptSubmit", "Idle", "Stop", "Ack"}

// runHook is called by a harness hook. It never fails loudly: without an
// enrolled agent, or on any error, it prints nothing and exits 0, so a
// broken or missing AgentNet never disturbs the session. It creates nothing
// in a missing home.
func runHook(home string, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || (!slices.Contains(hookHarnesses, args[0]) && args[0] != "pi" && args[0] != "omp") {
		return fmt.Errorf("usage: hook %s|pi (reads the hook event on stdin)", strings.Join(hookHarnesses, "|"))
	}
	pi := args[0] == "pi" || args[0] == "omp"
	events := harnessHookEvents(args[0])
	if pi {
		events = piEvents
	}
	if os.Getenv(client.BackgroundEnv) == "1" {
		return nil // a session the AgentNet worker started: not the user's
	}
	var in struct {
		TranscriptPath string                           `json:"transcript_path"`
		SessionID      string                           `json:"session_id"`
		HookEventName  string                           `json:"hook_event_name"`
		StopHookActive bool                             `json:"stop_hook_active"`
		Pos            int64                            `json:"pos"`     // Ack (pi)
		HasPos         bool                             `json:"has_pos"` // Ack (pi)
		Release        string                           `json:"release"` // Ack (pi)
		ReceiverAction string                           `json:"receiver_action"`
		Registration   *client.ReplySessionRegistration `json:"registration"`
		Owner          *client.ReplySessionCall         `json:"owner"`
		ReceiverAck    *client.ReplyReceiverAck         `json:"receiver_ack"`
		ReplySession   string                           `json:"reply_session"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 64<<20)).Decode(&in); err != nil {
		return nil
	}
	if in.ReceiverAction == "" && !slices.Contains(events, in.HookEventName) {
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
	if in.ReceiverAction != "" {
		if !pi && args[0] != "claude" {
			return nil
		}
		var result any
		switch in.ReceiverAction {
		case "register":
			if !pi || in.Registration == nil {
				return nil
			}
			in.Registration.Harness = args[0]
			result, err = a.RegisterReplySession(*in.Registration)
		case "channel-owner":
			if args[0] != "claude" {
				return nil
			}
			result, err = a.ClaudeReplyChannelOwner(in.SessionID)
		case "take":
			if in.Owner == nil {
				return nil
			}
			delivery, e := a.TakeReplyReceiverInput(*in.Owner)
			err = e
			result = hookReceiverTakeResult(args[0], in.Owner.SessionID, delivery)
		case "ack":
			if in.ReceiverAck == nil {
				return nil
			}
			accepted, e := a.AckReplyReceiverInput(*in.ReceiverAck)
			err = e
			result = map[string]bool{"accepted": accepted}
		case "close":
			if !pi || in.Owner == nil {
				return nil
			}
			in.Owner.CloseReason = "detached"
			if in.HookEventName == "SessionShutdown" {
				in.Owner.CloseReason = "shutdown"
			}
			err = a.CloseReplySession(*in.Owner)
			result = map[string]bool{"closed": err == nil}
		default:
			return nil
		}
		if err != nil {
			return nil
		}
		return json.NewEncoder(stdout).Encode(result)
	}
	if args[0] == "codex" || args[0] == "claude" {
		if in.TranscriptPath != "" {
			var handle string
			var e error
			if args[0] == "claude" {
				handle, e = a.ClaudeReplySessionHook(in.HookEventName, in.SessionID, in.TranscriptPath)
			} else {
				handle, e = a.CodexReplySessionHook(in.HookEventName, in.SessionID, in.TranscriptPath)
			}
			if e == nil {
				in.ReplySession = handle
			}
		}
		if in.HookEventName == "SessionEnd" {
			return nil
		}
	}
	if in.HookEventName == "Ack" {
		a.AckAttention(args[0], in.SessionID, in.Pos, in.HasPos, in.Release)
		return nil
	}
	at, err := a.Attention(client.HookEvent{Harness: args[0], Session: in.SessionID, Event: in.HookEventName, StopActive: in.StopHookActive, ReplySession: in.ReplySession})
	if err != nil || at.Text == "" {
		if err == nil {
			at.Commit() // records a new session's starting point
		}
		return nil
	}
	if pi {
		// Pi's extension shows the text itself and acknowledges it only
		// after that (event Ack), so nothing is recorded here.
		pos, hasPos, release := at.Ack()
		data, _ := json.Marshal(map[string]any{"text": at.Text, "ack": map[string]any{"pos": pos, "has_pos": hasPos, "release": release}})
		stdout.Write(append(data, '\n'))
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

// Formatting does not acknowledge native persistence or change delivery ownership.
func hookReceiverTakeResult(harness, sid string, delivery *client.ReplyReceiverDelivery) any {
	if harness == "claude" && delivery != nil {
		return map[string]any{"delivery": delivery, "channel": client.ClaudeReplyNotification(sid, delivery)}
	}
	return delivery
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

// hookCommand is the command line a harness runs for AgentNet's hook. Both
// harnesses run it with a POSIX shell on Linux and macOS, so the paths are
// single-quoted; on Windows the shell is not verified, so it is refused.
func hookCommand(home, harness string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("hooks are not supported on Windows yet: how the harnesses run hook commands there is not verified")
	}
	exe, err := selfExe() // a stable copy when the app runs from a passing place (appexe.go)
	if err != nil {
		return "", err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return "", err
	}
	if home, err = filepath.Abs(home); err != nil {
		return "", err
	}
	if strings.ContainsAny(exe+home, "'\n") {
		return "", errors.New("paths containing a single quote or newline are not supported in hook commands")
	}
	return "'" + exe + "' --home '" + home + "' hook " + harness, nil
}

// ownHookCommand matches exactly the commands hookCommand writes.
var ownHookCommand = regexp.MustCompile(`^'([^'\n]+)' --home '[^'\n]+' hook (claude|codex)$`)

// isAgentNetHook recognizes exactly a handler this command installed: the
// same keys, type and command shape, naming this or another agentnet
// executable.
func isAgentNetHook(handler any, harness string) bool {
	h, ok := handler.(map[string]any)
	if !ok || len(h) != 3 || h["type"] != "command" || h["timeout"] != float64(hookTimeout) {
		return false
	}
	cmd, _ := h["command"].(string)
	m := ownHookCommand.FindStringSubmatch(cmd)
	if m == nil || m[2] != harness {
		return false
	}
	if exe, err := os.Executable(); err == nil && m[1] == exe {
		return true
	}
	if exe, err := selfExe(); err == nil && m[1] == exe {
		return true
	}
	base := filepath.Base(m[1])
	return base == "agentnet" || base == "agentnet.exe"
}

func harnessHookEvents(harness string) []string {
	if harness == "codex" || harness == "claude" {
		return append(append([]string(nil), hookEvents...), "SessionEnd")
	}
	return hookEvents
}

const hookTimeout = 10 // seconds

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
	for _, event := range harnessHookEvents(harness) {
		if v, ok := hooks[event]; ok {
			if _, isList := v.([]any); !isList {
				return nil, fmt.Errorf("hooks.%s is not a list", event)
			}
		}
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
		for _, event := range harnessHookEvents(harness) {
			groups, _ := hooks[event].([]any)
			hooks[event] = append(groups, map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": float64(hookTimeout)}},
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
	usage := errors.New("usage: hooks show|install|remove claude|codex|pi [--file PATH] [--channel (Claude only)]")
	if len(args) < 2 {
		return usage
	}
	action, harness := args[0], args[1]
	channel := false
	var rest []string
	for i := 2; i < len(args); i++ {
		if args[i] == "--channel" {
			if channel {
				return usage
			}
			channel = true
			continue
		}
		rest = append(rest, args[i])
		// A file argument remains a literal path, even if named --channel.
		if args[i] == "--file" && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	if channel && harness != "claude" {
		return errors.New("hooks --channel is supported only for Claude")
	}
	if harness == "pi" || harness == "omp" {
		return runNativeHooks(home, harness, action, rest)
	}
	if !slices.Contains(hookHarnesses, harness) {
		return fmt.Errorf("hooks for %q are not supported: only %s hook contracts are implemented; other agents can use `agentnet inbox` and `agentnet conversation`", harness, strings.Join(hookHarnesses, " and "))
	}
	file := ""
	switch {
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
		var shown any = fragment
		if channel {
			mcp, err := applyClaudeChannelAssets(home, action)
			if err != nil {
				return err
			}
			shown = map[string]any{"settings": fragment, "mcpConfig": mcp}
		}
		data, _ := json.MarshalIndent(shown, "", "  ")
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
	var channelConfig json.RawMessage
	if channel {
		channelConfig, err = applyClaudeChannelAssets(home, action)
		if err != nil {
			return fmt.Errorf("settings unchanged; Claude channel asset operation refused or incomplete: %w", err)
		}
	}
	channelNotice := func() {
		if !channel {
			return
		}
		if action == "install" {
			fmt.Println("AgentNet Claude channel files installed; native channel is NOT enabled by this install.")
			fmt.Println("local MCP config fragment:", string(channelConfig))
			fmt.Println("next: pass this fragment with --mcp-config, explicitly admit server:agentnet with --dangerously-load-development-channels server:agentnet, and complete native channel consent; keep original tools, skills, settings and permissions")
			fmt.Println("requires: Linux and a Claude Code that marks the processes it starts (CLAUDECODE=1) and gives them its session ID (CLAUDE_CODE_SESSION_ID, documented from 2.1.224); a reply counts only on its exact channel record in that session's transcript; each session is bound to its exact running claude executable and registers again after Claude is updated")
			fmt.Println("tested natively so far: Claude 2.1.286 on Linux, official SDK 1.31.0, Node 26.10.0; other versions are not yet tested end to end")
		} else {
			fmt.Println("owned Claude channel files removed (or absent); running native sessions are not closed or reassigned")
		}
	}
	if string(data) == string(old) {
		fmt.Printf("%s already up to date\n", file)
		channelNotice()
		return nil
	}
	if len(old) > 0 {
		backup, err := writeBackup(file, old)
		if err != nil {
			if channel {
				return fmt.Errorf("Claude channel assets already processed; settings backup failed, settings unchanged: %w", err)
			}
			return err
		}
		fmt.Printf("backup %s\n", backup)
	}
	if err := writeFileAtomic(file, data); err != nil {
		if channel {
			return fmt.Errorf("Claude channel assets already processed; settings write failed: %w", err)
		}
		return err
	}
	fmt.Printf("%s %s: AgentNet hooks for %s (%s)\n", map[string]string{"install": "updated", "remove": "removed from"}[action], file, harness, strings.Join(harnessHookEvents(harness), ", "))
	if action == "install" {
		if harness == "codex" {
			fmt.Printf("next: in Codex open /hooks, review and trust the %d AgentNet hooks, then start a new session; untrusted hooks do not run\n", len(harnessHookEvents(harness)))
		} else {
			fmt.Println("next: start a new Claude Code session (running sessions may not pick up the change)")
		}
	}
	channelNotice()
	return nil
}

// writeBackup saves data next to file under a new, never reused name.
func writeBackup(file string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".agentnet-backup-"+time.Now().UTC().Format("20060102T150405Z")+"-*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
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
