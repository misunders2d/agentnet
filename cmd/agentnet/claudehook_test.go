package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
)

// Codec-only fixture: it does not stand in for a native Claude receipt.
func TestHookClaudeTakeResultShape(t *testing.T) {
	delivery := &client.ReplyReceiverDelivery{BindingID: "binding", InputID: "input", ClaimID: "claim", InputToken: "synthetic-token", RequestRef: "request", RequestBody: "original request"}
	for _, harness := range []string{"claude", "pi", "omp"} {
		body, err := json.Marshal(hookReceiverTakeResult(harness, "selected-session", delivery))
		if err != nil {
			t.Fatal(err)
		}
		if harness == "claude" {
			var response struct {
				Delivery *client.ReplyReceiverDelivery    `json:"delivery"`
				Channel  client.ClaudeChannelNotification `json:"channel"`
			}
			if err = json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(response.Delivery, delivery) || !reflect.DeepEqual(response.Channel, client.ClaudeReplyNotification("selected-session", delivery)) {
				t.Fatal("Claude take wrapper changed selected tuple or native notification")
			}
		} else {
			want, _ := json.Marshal(delivery)
			if !bytes.Equal(body, want) {
				t.Fatal("Pi/OMP take shape changed")
			}
		}
		empty, err := json.Marshal(hookReceiverTakeResult(harness, "selected-session", nil))
		if err != nil || string(empty) != "null" {
			t.Fatal("empty take fabricated channel payload")
		}
	}
}

// Build only private local test enrollment metadata. No listener, daemon,
// harness, provider, network, user configuration or real session is started.
func hookClaudeFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = id.Save(filepath.Join(home, "identity.json")); err != nil {
		t.Fatal(err)
	}
	if a, e := client.Open(home); e == nil {
		a.Close()
		t.Fatal("unfinished test enrollment accepted")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for key, value := range map[string]string{"enrolled": "1", "address": "synthetic/hook", "hub": "https://127.0.0.1:1", "hub_cert": "", "realm_id": "synthetic-hook-realm"} {
		if _, err = db.Exec(`INSERT OR REPLACE INTO config(k,v) VALUES(?,?)`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	return home, db
}

func TestHookClaudeNativeReceiverRoutes(t *testing.T) {
	home, db := hookClaudeFixture(t)
	t.Setenv(client.BackgroundEnv, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	file := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(file, []byte("{\"type\":\"session\",\"id\":\"selected-session\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(harness string, payload any) []byte {
		t.Helper()
		raw, _ := json.Marshal(payload)
		var out bytes.Buffer
		if err := runHook(home, []string{harness}, bytes.NewReader(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	registration := client.ReplySessionRegistration{SessionID: "selected-session", File: file}
	// Claude has native lifecycle authority only: arbitrary registration and
	// channel-owner outside the qualified ancestor remain silent/refused.
	if len(call("claude", map[string]any{"receiver_action": "register", "registration": registration})) != 0 {
		t.Fatal("Claude generic registration allowed")
	}
	if len(call("claude", map[string]any{"receiver_action": "channel-owner", "session_id": "selected-session"})) != 0 {
		t.Fatal("non-native Claude channel owner allowed")
	}
	start := call("claude", map[string]any{"hook_event_name": "SessionStart", "session_id": "selected-session", "transcript_path": file})
	if len(start) != 0 {
		var legacy struct {
			HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
		}
		if json.Unmarshal(start, &legacy) != nil || legacy.HookSpecificOutput == nil {
			t.Fatal("unbound SessionStart changed legacy metadata shape")
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM reply_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("non-native Claude lifecycle created receiver")
	}
	if len(call("claude", map[string]any{"hook_event_name": "SessionEnd", "session_id": "unregistered-end"})) != 0 {
		t.Fatal("Claude SessionEnd emitted attention")
	}
	if err := db.QueryRow(`SELECT count(*) FROM attention WHERE harness='claude' AND session='unregistered-end'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("Claude SessionEnd consumed legacy attention")
	}
	for _, harness := range []string{"pi", "omp"} {
		raw := call(harness, map[string]any{"receiver_action": "register", "registration": registration})
		var registered client.ReplySessionOwner
		if json.Unmarshal(raw, &registered) != nil || registered.Harness != harness || registered.Handle == "" || registered.OwnerToken == "" {
			t.Fatal("Pi/OMP registration route changed")
		}
		owner := client.ReplySessionCall{Handle: registered.Handle, Generation: registered.Generation, OwnerToken: registered.OwnerToken, SessionID: registration.SessionID, File: registration.File}
		if string(call(harness, map[string]any{"receiver_action": "take", "owner": owner})) != "null\n" {
			t.Fatal("Pi/OMP empty take shape changed")
		}
		if string(call("claude", map[string]any{"receiver_action": "take", "owner": owner})) != "null\n" {
			t.Fatal("Claude empty take wrapped nil delivery")
		}
		if len(call("claude", map[string]any{"receiver_action": "close", "owner": owner})) != 0 {
			t.Fatal("Claude generic close granted")
		}
		if len(call(harness, map[string]any{"receiver_action": "channel-owner", "session_id": owner.SessionID})) != 0 {
			t.Fatal("Pi/OMP gained Claude channel-owner route")
		}
		if string(call(harness, map[string]any{"receiver_action": "close", "owner": owner})) != "{\"closed\":true}\n" {
			t.Fatal("Pi/OMP close route changed")
		}
	}
	if len(call("claude", map[string]any{"receiver_action": "take", "owner": client.ReplySessionCall{Handle: "wrong"}})) != 0 {
		t.Fatal("wrong Claude owner take not silent")
	}
	if len(call("claude", map[string]any{"receiver_action": "ack", "receiver_ack": client.ReplyReceiverAck{ReplySessionCall: client.ReplySessionCall{Handle: "wrong"}}})) != 0 {
		t.Fatal("wrong Claude receipt ACK not silent")
	}
	t.Setenv(client.BackgroundEnv, "1")
	before := 0
	if err := db.QueryRow(`SELECT count(*) FROM reply_sessions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"SessionStart", "SessionEnd", "UserPromptSubmit", "PostToolUse", "Stop"} {
		if len(call("claude", map[string]any{"hook_event_name": event, "session_id": "background", "transcript_path": file})) != 0 {
			t.Fatal("background native hook emitted output")
		}
	}
	if len(call("claude", map[string]any{"receiver_action": "channel-owner", "session_id": "background"})) != 0 {
		t.Fatal("background channel hook emitted output")
	}
	if err := db.QueryRow(`SELECT count(*) FROM reply_sessions`).Scan(&count); err != nil || count != before {
		t.Fatal("background native hook changed registry")
	}
	if err := db.QueryRow(`SELECT count(*) FROM attention WHERE session='background'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("background hook changed metadata cursor")
	}
}

func TestHookClaudeDetachedMergePreservesHandlers(t *testing.T) {
	const command = "'/owned/agentnet' --home '/owned/home' hook claude"
	original := `{"hooks":{"SessionEnd":[{"hooks":[{"type":"command","command":"human-session-cleanup","timeout":7}]}],"UserPromptSubmit":[{"hooks":[{"type":"command","command":"human-prompt-handler","timeout":3}]}]}}`
	parse := func(raw string) map[string]any {
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) != nil {
			t.Fatal("fixture JSON")
		}
		return m
	}
	first, err := mergeHooks(parse(original), "claude", command)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	second, err := mergeHooks(parse(string(a)), "claude", command)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) || !strings.Contains(string(a), "human-session-cleanup") || !strings.Contains(string(a), "human-prompt-handler") {
		t.Fatal("Claude merge changed user handlers or lost idempotence")
	}
	hooks := first["hooks"].(map[string]any)
	for _, event := range harnessHookEvents("claude") {
		found := 0
		for _, entry := range hooks[event].([]any) {
			for _, handler := range entry.(map[string]any)["hooks"].([]any) {
				if isAgentNetHook(handler, "claude") {
					found++
				}
			}
		}
		if found != 1 {
			t.Fatalf("event %s has %d own handlers", event, found)
		}
	}
	removed, err := mergeHooks(parse(string(a)), "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := json.Marshal(removed)
	want, _ := json.Marshal(parse(original))
	if !bytes.Equal(r, want) {
		t.Fatal("remove changed existing human hooks")
	}
}

func TestHookInstallOutputUsesCurrentEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native hook installation is unqualified on Windows")
	}
	for _, harness := range []string{"claude", "codex"} {
		file := filepath.Join(t.TempDir(), "hooks.json")
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stdout
		os.Stdout = writer
		installErr := runHooks(t.TempDir(), []string{"install", harness, "--file", file})
		os.Stdout = original
		writer.Close()
		output, readErr := io.ReadAll(reader)
		reader.Close()
		if installErr != nil || readErr != nil {
			t.Fatal("private fixture install/output failed")
		}
		if !strings.Contains(string(output), "("+strings.Join(harnessHookEvents(harness), ", ")+")") {
			t.Fatal("installed event list omitted native lifecycle event")
		}
		if harness == "codex" && !strings.Contains(string(output), fmt.Sprintf("trust the %d AgentNet hooks", len(harnessHookEvents(harness)))) {
			t.Fatal("Codex instruction guessed hook count")
		}
	}
}
