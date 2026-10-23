package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func claudeAdapterFixture(t *testing.T) (*claudeStreamBridge, *json.Decoder, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	in, out := io.Pipe()
	output := new(bytes.Buffer)
	b := &claudeStreamBridge{ctx: ctx, input: out, output: output, ready: make(chan struct{}), pending: map[string]*piSteer{}, seen: map[string]string{}, prompt: "Original", session: "owned"}
	t.Cleanup(func() { cancel(); b.close(); in.Close() })
	claudeAdapterEvent(b, claudeAdapterUser("Original"))
	return b, json.NewDecoder(in), output
}
func claudeAdapterEvent(b *claudeStreamBridge, v any) { raw, _ := json.Marshal(v); b.event(raw) }
func claudeAdapterUser(text string) map[string]any {
	return map[string]any{"type": "user", "uuid": "native-uuid", "isReplay": true, "session_id": "owned", "parent_tool_use_id": nil, "message": map[string]string{"role": "user", "content": text}}
}
func claudeAdapterSteer(t *testing.T, b *claudeStreamBridge, d *json.Decoder) <-chan string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- b.steer(t.Context(), "correction", "Use English") }()
	var req struct {
		Type    string
		Message struct{ Role, Content string }
	}
	if err := d.Decode(&req); err != nil || req.Type != "user" || req.Message.Role != "user" || req.Message.Content != "Use English" {
		t.Fatalf("native input: %+v %v", req, err)
	}
	return done
}
func assertNativePending(t *testing.T, done <-chan string) {
	t.Helper()
	select {
	case r := <-done:
		t.Fatal("unproven consumption", r)
	default:
	}
}

func TestClaudeStreamConsumptionAndOneAnswer(t *testing.T) {
	b, d, out := claudeAdapterFixture(t)
	done := claudeAdapterSteer(t, b, d)
	assertNativePending(t, done)
	echo := claudeAdapterUser("Use English")
	echo["type"] = "assistant"
	claudeAdapterEvent(b, echo)
	assertNativePending(t, done)
	claudeAdapterEvent(b, claudeAdapterUser("Use English"))
	if r := <-done; r != steerAccepted {
		t.Fatal(r)
	}
	if r := b.steer(t.Context(), "correction", "Use English"); r != steerAccepted {
		t.Fatal("duplicate", r)
	}
	if r := b.steer(t.Context(), "correction", "Changed"); r != steerUnknown {
		t.Fatal("changed duplicate", r)
	}
	if out.Len() != 0 {
		t.Fatal("answer before result")
	}
	final := map[string]any{"type": "result", "session_id": "owned", "result": "Corrected final", "is_error": false}
	claudeAdapterEvent(b, final)
	claudeAdapterEvent(b, final)
	b.finish()
	if out.String() != "Corrected final\n" {
		t.Fatal(out.String())
	}
	if r := b.steer(t.Context(), "late", "Late"); r != steerQueued {
		t.Fatal("late input", r)
	}
}
func TestClaudeStreamRejectsUnprovenReplay(t *testing.T) {
	for _, field := range []string{"uuid", "isReplay", "parent_tool_use_id", "session_id", "message", "type"} {
		t.Run(field, func(t *testing.T) {
			b, d, _ := claudeAdapterFixture(t)
			done := claudeAdapterSteer(t, b, d)
			row := claudeAdapterUser("Use English")
			delete(row, field)
			claudeAdapterEvent(b, row)
			assertNativePending(t, done)
			b.close()
			if r := <-done; r != steerUnknown {
				t.Fatal(r)
			}
		})
	}
}

// Technical stream failures fail the run with their exact reason; only a
// native approval, input or permission denial asks the owner (oct23 T2).
func TestClaudeStreamIdentityPermissionAndIncomplete(t *testing.T) {
	for _, c := range []struct{ mode, failure string }{
		{"foreign-session", "Claude switched to another session; no answer was used"},
		{"foreign-result", "Claude reported a result for another session; no answer was used"},
		{"error-result", "Claude reported an error: API Error: 529 overloaded"},
		{"error-subtype", "Claude reported an error (error_max_turns)"},
		{"empty-result", "Claude finished without an answer"},
		{"incomplete", "Claude ended before its request settled"},
		{"malformed", "Claude sent an unreadable protocol line; no answer was used"},
		{"approval", ""},
		{"denied-result", ""},
		{"permission-denied", ""},
	} {
		t.Run(c.mode, func(t *testing.T) {
			b, _, out := claudeAdapterFixture(t)
			switch c.mode {
			case "foreign-session":
				claudeAdapterEvent(b, map[string]any{"type": "system", "subtype": "init", "session_id": "foreign"})
			case "foreign-result":
				claudeAdapterEvent(b, map[string]any{"type": "result", "result": "partial"})
			case "error-result":
				claudeAdapterEvent(b, map[string]any{"type": "result", "subtype": "success", "session_id": "owned", "is_error": true, "result": "API Error: 529 overloaded"})
			case "error-subtype":
				claudeAdapterEvent(b, map[string]any{"type": "result", "subtype": "error_max_turns", "session_id": "owned", "is_error": true})
			case "empty-result":
				claudeAdapterEvent(b, map[string]any{"type": "result", "subtype": "success", "session_id": "owned", "result": " "})
			case "approval":
				claudeAdapterEvent(b, map[string]any{"type": "control_request"})
			case "denied-result":
				claudeAdapterEvent(b, map[string]any{"type": "result", "session_id": "owned", "result": "partial", "permission_denials": []any{map[string]string{"tool_name": "Bash"}}})
			case "permission-denied":
				claudeAdapterEvent(b, map[string]any{"type": "system", "subtype": "permission_denied"})
			case "incomplete":
				b.finish()
			case "malformed":
				b.event([]byte("{"))
			}
			b.finish()
			if strings.Contains(out.String(), "partial") || b.failureText() != c.failure {
				t.Fatalf("output %q failure %q", out.String(), b.failureText())
			}
			if human := strings.HasPrefix(out.String(), needsHumanMarker+"\n"+nativeStatusPrefix); human != (c.failure == "") || !human && out.Len() != 0 {
				t.Fatalf("output %q failure %q", out.String(), b.failureText())
			}
		})
	}
}
func TestOMPRPCDistinctReceiptAndSettledPrompt(t *testing.T) {
	b, d, out := piBridgeFixture(t)
	b.omp = true
	done, req := piSteerStart(t, b, d)
	piEvent(b, map[string]any{"type": "response", "id": req["id"], "command": "steer", "success": true})
	assertNativePending(t, done)
	piEvent(b, map[string]any{"type": "message_start", "message": map[string]string{"role": "user", "content": "Use English"}})
	assertNativePending(t, done)
	receipt := map[string]any{"type": "message_start", "messageId": "native-msg", "message": map[string]any{"role": "user", "steering": true, "attribution": "user", "content": []any{map[string]string{"type": "text", "text": "Use English"}}}}
	piEvent(b, receipt)
	if r := <-done; r != steerAccepted {
		t.Fatal(r)
	}
	piEvent(b, map[string]any{"type": "message_end", "message": map[string]string{"role": "assistant", "content": "Corrected OMP", "stopReason": "stop"}})
	b.event([]byte(`{"type":"agent_end"}`))
	b.event([]byte(`{"type":"agent_settled","aborted":false}`))
	b.event([]byte(`{"type":"prompt_result","id":"foreign","status":"completed","sessionSettled":true}`))
	if out.Len() != 0 {
		t.Fatal("OMP published without exact settled prompt", out.String())
	}
	b.event([]byte(`{"type":"prompt_result","id":"agentnet-initial","status":"completed","sessionSettled":true}`))
	b.event([]byte(`{"type":"prompt_result","id":"agentnet-initial","status":"completed","sessionSettled":true}`))
	if out.String() != "Corrected OMP\n" {
		t.Fatal(out.String())
	}
}
func TestOMPRPCRejectsReceiptAndUnsettledCompletion(t *testing.T) {
	for _, field := range []string{"messageId", "steering", "attribution"} {
		t.Run(field, func(t *testing.T) {
			b, d, _ := piBridgeFixture(t)
			b.omp = true
			done, _ := piSteerStart(t, b, d)
			m := map[string]any{"role": "user", "content": "Use English", "steering": true, "attribution": "user"}
			r := map[string]any{"type": "message_start", "messageId": "native-msg", "message": m}
			if field == "messageId" {
				delete(r, field)
			} else {
				delete(m, field)
			}
			piEvent(b, r)
			assertNativePending(t, done)
			b.close()
			if x := <-done; x != steerUnknown {
				t.Fatal(x)
			}
		})
	}
	for _, row := range []string{`{"type":"prompt_result","id":"agentnet-initial","status":"completed","sessionSettled":false}`, `{"type":"prompt_result","id":"agentnet-initial","status":"error","sessionSettled":true}`} {
		b, _, out := piBridgeFixture(t)
		b.omp = true
		b.answer = "partial"
		b.event([]byte(row))
		if out.Len() != 0 || !strings.HasPrefix(b.failureText(), "OMP ") {
			t.Fatalf("output %q failure %q", out.String(), b.failureText())
		}
	}
}
func TestClaudePartialMessagesCapabilityIsOptional(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake CLI fixture")
	}
	for _, mode := range []string{"supported", "old", "near-match", "refused", "timeout", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "claude")
			log := filepath.Join(dir, "calls")
			source := `#!/bin/sh
if [ "$1" = "--help" ]; then
 printf 'help|%s|%s\n' "$CLAUDE_CAPABILITY_SETTING" "$PWD" >> "$CLAUDE_CAPABILITY_LOG"
 case "$CLAUDE_CAPABILITY_MODE" in
 supported) printf '  --include-partial-messages  Include partial chunks\n';;
 old) printf '  --replay-user-messages\n';;
 near-match) printf '  --include-partial-messages-unsupported\n';;
 refused) exit 9;;
 timeout) exec sleep 30;;
 oversized) printf '  --include-partial-messages\n'; head -c 70000 /dev/zero;;
 esac
 exit 0
fi
printf 'work\n' >> "$CLAUDE_CAPABILITY_LOG"
IFS= read -r request
printf '{"type":"result","session_id":"owned","result":"original answer","is_error":false}\n'
`
			if err := os.WriteFile(script, []byte(source), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, script, "-p", "--output-format", "text", "--session-id", "owned")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "CLAUDE_CAPABILITY_MODE="+mode, "CLAUDE_CAPABILITY_LOG="+log, "CLAUDE_CAPABILITY_SETTING=owner-setting")
			var result bytes.Buffer
			began := time.Now()
			b, err := newClaudeStreamBridge(ctx, cmd, "Original", &result, "owned")
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			if time.Since(began) > 2*time.Second {
				t.Fatal("optional help exceeded two-second bound")
			}
			partial := false
			for _, arg := range cmd.Args {
				partial = partial || arg == "--include-partial-messages"
			}
			if partial != (mode == "supported") {
				t.Fatalf("mode %s optional flag=%t", mode, partial)
			}
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			b.finish()
			if result.String() != "original answer\n" {
				t.Fatal("optional probe changed result", result.String())
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if string(calls) != "help|owner-setting|"+dir+"\nwork\n" {
				t.Fatalf("probe lost environment/directory or repeated work: %q", calls)
			}
		})
	}
}

func TestNativeAdapterFailedStartJoinsInput(t *testing.T) {
	for _, name := range []string{"claude", "omp"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing"), "-p", "--output-format", "text", "--session-id", "owned")
			var closeBridge func()
			if name == "claude" {
				b, e := newClaudeStreamBridge(ctx, cmd, strings.Repeat("x", 1<<20), io.Discard, "owned")
				if e != nil {
					t.Fatal(e)
				}
				closeBridge = b.close
				args := strings.Join(cmd.Args, " ")
				for _, x := range []string{"--session-id owned", "--output-format stream-json", "--input-format stream-json", "--replay-user-messages"} {
					if !strings.Contains(args, x) {
						t.Fatal(args)
					}
				}
			} else {
				b, e := newPiRPCBridge(ctx, cmd, strings.Repeat("x", 1<<20), io.Discard, "omp")
				if e != nil {
					t.Fatal(e)
				}
				closeBridge = b.close
				if !b.omp {
					t.Fatal("OMP protocol not selected")
				}
			}
			if e := cmd.Start(); e == nil {
				t.Fatal("unexpected process")
			}
			done := make(chan struct{})
			go func() { closeBridge(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("blocked startup leaked")
			}
		})
	}
}
func TestNativeAdapterQueuedWriteEndAndCancelFence(t *testing.T) {
	for _, name := range []string{"claude", "omp"} {
		for _, mode := range []string{"ended", "cancelled"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var out bytes.Buffer
				var send func() error
				var end func()
				var unlock func()
				var closeBridge func()
				if name == "claude" {
					b := &claudeStreamBridge{ctx: t.Context(), input: adapterWriteCloser{&out}, output: io.Discard, ready: make(chan struct{}), pending: map[string]*piSteer{"id": {text: "x", result: make(chan string, 1)}}, active: true}
					b.writeMu.Lock()
					unlock = b.writeMu.Unlock
					claim := b.pending["id"]
					send = func() error { return b.send(ctx, "id", claim, map[string]string{"type": "user"}) }
					end = func() { b.mu.Lock(); b.endLocked(); b.mu.Unlock() }
					closeBridge = b.close
				} else {
					b := &piRPCBridge{ctx: t.Context(), input: adapterWriteCloser{&out}, output: io.Discard, ready: make(chan struct{}), pending: map[string]*piSteer{"id": {text: "x", result: make(chan string, 1)}}, active: true, omp: true}
					b.writeMu.Lock()
					unlock = b.writeMu.Unlock
					claim := b.pending["id"]
					send = func() error { return b.send(ctx, "id", claim, map[string]string{"type": "steer"}) }
					end = func() { b.event([]byte(`{"type":"agent_end"}`)) }
					closeBridge = b.close
				}
				done := make(chan error, 1)
				go func() { done <- send() }()
				if mode == "ended" {
					end()
				} else {
					cancel()
				}
				unlock()
				if e := <-done; e == nil {
					t.Fatal("stale serial write sent")
				}
				closeBridge()
				if out.Len() != 0 {
					t.Fatal("native write after end/cancel")
				}
			})
		}
	}
}

type adapterWriteCloser struct{ io.Writer }

func (adapterWriteCloser) Close() error { return nil }
func TestNativeAdapterFramingAndAnswerLimits(t *testing.T) {
	b, _, out := claudeAdapterFixture(t)
	data := []byte(strings.Repeat("x", codexLineMax+1))
	b.Write(data)
	if len(b.buf) != 0 || out.Len() != 0 || b.failureText() != "Claude sent a protocol line over the size bound" {
		t.Fatal("unbounded native frame")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	in, pipe := io.Pipe()
	defer in.Close()
	sink := &limitedBuffer{max: maxOutput}
	c := &claudeStreamBridge{ctx: ctx, input: pipe, output: sink, ready: make(chan struct{}), session: "owned", pending: map[string]*piSteer{}}
	claudeAdapterEvent(c, map[string]any{"type": "result", "session_id": "owned", "result": strings.Repeat("a", maxOutput*2)})
	if len(c.answer) != maxOutput+1 || sink.Len() != maxOutput || !sink.truncated {
		t.Fatal("answer limits lost")
	}
}
