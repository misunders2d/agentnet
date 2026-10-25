package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func codexAnswerTestBridge(t *testing.T) (*codexRoomBridge, *bytes.Buffer) {
	t.Helper()
	in, out := io.Pipe()
	t.Cleanup(func() { in.Close(); out.Close() })
	output := new(bytes.Buffer)
	return &codexRoomBridge{input: out, output: output, thread: "thread", turn: "turn"}, output
}

func codexAnswerTestEvent(t *testing.T, b *codexRoomBridge, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	b.event(raw)
}

func TestCodexRoomCompletedAnswerSelection(t *testing.T) {
	type message struct {
		phase any // nil omits phase; RawMessage(null) covers an explicit null.
		text  string
	}
	final := message{"final_answer", "ANSWER"}
	legacy := message{nil, "LEGACY"}
	for _, tc := range []struct {
		name     string
		messages []message
		want     string
	}{
		{"final-then-empty", []message{final, {"final_answer", ""}}, "ANSWER"},
		{"final-then-whitespace", []message{final, {"final_answer", " \n\t"}}, "ANSWER"},
		{"final-then-commentary", []message{final, {"commentary", "PREAMBLE"}}, "ANSWER"},
		{"final-then-partial", []message{final, {"partial_answer", "PARTIAL"}}, "ANSWER"},
		{"final-then-legacy", []message{final, legacy}, "ANSWER"},
		{"final-then-null", []message{final, {json.RawMessage("null"), "LEGACY"}}, "ANSWER"},
		{"legacy-then-final", []message{legacy, final}, "ANSWER"},
		{"legacy-then-empty", []message{legacy, {nil, ""}}, "LEGACY"},
		{"legacy-then-commentary", []message{legacy, {"commentary", "PREAMBLE"}}, "LEGACY"},
		{"legacy-only", []message{legacy}, "LEGACY"},
		{"null-only", []message{{json.RawMessage("null"), "LEGACY"}}, "LEGACY"},
		{"latest-final", []message{final, {"final_answer", "LAST"}}, "LAST"},
		{"latest-legacy", []message{legacy, {nil, "LAST"}}, "LAST"},
		{"commentary-only", []message{{"commentary", "PREAMBLE"}}, ""},
		{"partial-only", []message{{"partial_answer", "PARTIAL"}}, ""},
		{"unknown-phase", []message{{"future_phase", "UNKNOWN"}}, ""},
		{"explicit-empty-phase", []message{{"", "UNKNOWN"}}, ""},
		{"empty-only", []message{{"final_answer", " \n"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, output := codexAnswerTestBridge(t)
			for i, m := range tc.messages {
				item := map[string]any{"id": fmt.Sprint(i), "type": "agentMessage", "text": m.text}
				if m.phase != nil {
					item["phase"] = m.phase
				}
				codexAnswerTestEvent(t, b, "item/completed", map[string]any{"threadId": "thread", "turnId": "turn", "item": item})
			}
			terminal := map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}}
			codexAnswerTestEvent(t, b, "turn/completed", terminal)
			codexAnswerTestEvent(t, b, "turn/completed", terminal) // No duplicate output.
			b.finish()
			if output.String() != tc.want {
				t.Fatalf("output %q, want %q", output.String(), tc.want)
			}
			wantFailure := ""
			if tc.want == "" {
				wantFailure = "Codex completed the turn without an answer"
			}
			if b.failureText() != wantFailure {
				t.Fatalf("failure %q, want %q", b.failureText(), wantFailure)
			}
		})
	}
}

func TestCodexRoomAnswerScopeAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, thread, turn, eventThread, eventTurn, status, want, failure string
		complete                                                          bool
	}{
		{"matching", "thread", "turn", "thread", "turn", "completed", "ANSWER", "", true},
		{"foreign-thread", "thread", "turn", "foreign", "turn", "completed", "", "without an answer", true},
		{"stale-turn", "thread", "turn", "thread", "old-turn", "completed", "", "without an answer", true},
		{"inactive-thread", "", "turn", "", "turn", "completed", "", "before starting a session", true},
		{"inactive-turn", "thread", "", "thread", "", "completed", "", "before the turn completed", true},
		{"failed-turn", "thread", "turn", "thread", "turn", "failed", "", `turn as "failed"`, true},
		{"interrupted-turn", "thread", "turn", "thread", "turn", "interrupted", "", `turn as "interrupted"`, true},
		{"native-eof", "thread", "turn", "thread", "turn", "", "", "before the turn completed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, output := codexAnswerTestBridge(t)
			b.thread, b.turn = tc.thread, tc.turn
			codexAnswerTestEvent(t, b, "item/completed", map[string]any{"threadId": tc.eventThread, "turnId": tc.eventTurn,
				"item": map[string]string{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": "ANSWER"}})
			if tc.complete {
				codexAnswerTestEvent(t, b, "turn/completed", map[string]any{"threadId": tc.thread, "turn": map[string]string{"id": tc.turn, "status": tc.status}})
			}
			b.finish()
			if output.String() != tc.want || tc.failure == "" && b.failureText() != "" || tc.failure != "" && !strings.Contains(b.failureText(), tc.failure) {
				t.Fatalf("output=%q failure=%q", output.String(), b.failureText())
			}
		})
	}
	t.Run("duplicate-item", func(t *testing.T) {
		b, output := codexAnswerTestBridge(t)
		for range 2 {
			codexAnswerTestEvent(t, b, "item/completed", map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{"id": "same-item", "type": "agentMessage", "phase": "final_answer", "text": "ANSWER"}})
		}
		codexAnswerTestEvent(t, b, "turn/completed", map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}})
		b.finish()
		if output.String() != "ANSWER" || b.answerID != "same-item" || b.failureText() != "" {
			t.Fatalf("output=%q selected=%q failure=%q", output.String(), b.answerID, b.failureText())
		}
	})
	t.Run("stream-and-start-are-not-completion", func(t *testing.T) {
		b, output := codexAnswerTestBridge(t)
		codexAnswerTestEvent(t, b, "item/agentMessage/delta", map[string]string{"threadId": "thread", "turnId": "turn", "itemId": "answer", "delta": "UNCOMPLETED"})
		codexAnswerTestEvent(t, b, "item/started", map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": "UNCOMPLETED"}})
		codexAnswerTestEvent(t, b, "turn/completed", map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}})
		b.finish()
		if output.Len() != 0 || b.failureText() != "Codex completed the turn without an answer" {
			t.Fatalf("output=%q failure=%q", output.String(), b.failureText())
		}
	})
	t.Run("foreign-items-preserve-final", func(t *testing.T) {
		b, output := codexAnswerTestBridge(t)
		for _, scope := range [][2]string{{"thread", "turn"}, {"foreign", "turn"}, {"thread", "old-turn"}} {
			text := "FOREIGN"
			if scope == [2]string{"thread", "turn"} {
				text = "ANSWER"
			}
			codexAnswerTestEvent(t, b, "item/completed", map[string]any{"threadId": scope[0], "turnId": scope[1], "item": map[string]string{"type": "agentMessage", "phase": "final_answer", "text": text}})
		}
		codexAnswerTestEvent(t, b, "turn/completed", map[string]any{"threadId": "foreign", "turn": map[string]string{"id": "turn", "status": "completed"}})
		if output.Len() != 0 || b.done {
			t.Fatal("foreign terminal completed this turn")
		}
		codexAnswerTestEvent(t, b, "turn/completed", map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}})
		b.finish()
		if output.String() != "ANSWER" || b.failureText() != "" {
			t.Fatalf("output=%q failure=%q", output.String(), b.failureText())
		}
	})
}

func TestCodexRoomAnswerDiagnosticsBounded(t *testing.T) {
	b, _ := codexAnswerTestBridge(t)
	var lines []string
	b.onDiagnostic = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	for i := range 40 {
		codexAnswerTestEvent(t, b, "item/agentMessage/delta", map[string]string{"threadId": "thread", "turnId": "turn", "itemId": "answer", "delta": "PRIVATE_STREAM_CANARY"})
		phase := "final_answer"
		if i == 0 {
			phase = "PRIVATE_PHASE_CANARY\nforged"
		}
		codexAnswerTestEvent(t, b, "item/completed", map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{
			"id": "item-\n" + strings.Repeat("x", 1000), "type": "agentMessage", "phase": phase, "text": "PRIVATE_ANSWER_CANARY"}})
	}
	codexAnswerTestEvent(t, b, "turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "failed", "error": map[string]string{"message": "PRIVATE_ERROR_CANARY"}}})
	b.finish()
	if len(lines) != codexDiagnosticMax+1 {
		t.Fatalf("diagnostic lines=%d, want %d", len(lines), codexDiagnosticMax+1)
	}
	for _, line := range lines {
		if len(line) > 1024 || strings.ContainsAny(line, "\n\r\x1b") || strings.Contains(line, "PRIVATE_") {
			t.Fatalf("unsafe diagnostic line: %q", line)
		}
	}
	last := lines[len(lines)-1]
	for _, want := range []string{"terminal=failed", "message_deltas=40", "events_suppressed=24", `answer_phase="final_answer"`, "answer_bytes=21"} {
		if !strings.Contains(last, want) {
			t.Fatalf("terminal summary missing %q: %s", want, last)
		}
	}
}

func TestCodexRoomRejectedAnswerDiagnostics(t *testing.T) {
	b, output := codexAnswerTestBridge(t)
	var diagnostics bytes.Buffer
	b.onDiagnostic = func(format string, args ...any) { fmt.Fprintf(&diagnostics, format+"\n", args...) }
	codexAnswerTestEvent(t, b, "item/completed", map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"type": "agentMessage", "phase": 4, "text": "PRIVATE_INVALID_CANARY"}})
	codexAnswerTestEvent(t, b, "item/agentMessage/delta", map[string]string{"threadId": "foreign", "turnId": "turn", "delta": "PRIVATE_FOREIGN_CANARY"})
	codexAnswerTestEvent(t, b, "item/agentMessage/delta", map[string]string{"threadId": "thread", "turnId": "turn", "delta": "PRIVATE_STREAM_CANARY"})
	codexAnswerTestEvent(t, b, "turn/completed", map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}})
	b.finish()
	log := diagnostics.String()
	if output.Len() != 0 || b.failureText() != "Codex completed the turn without an answer" || strings.Contains(log, "PRIVATE_") {
		t.Fatalf("output=%q failure=%q diagnostics=%q", output.String(), b.failureText(), log)
	}
	for _, want := range []string{"event=item/completed", "selection=invalid-params", "terminal=completed-no-answer", "message_deltas=1"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in %q", want, log)
		}
	}
}

func TestCodexRoomNativeRefusalTerminalDiagnostics(t *testing.T) {
	for _, ending := range []string{"callback-error", "stale-authority", "never-policy"} {
		t.Run(ending, func(t *testing.T) {
			b, output := codexAnswerTestBridge(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			b.ctx, b.nativeEnd, b.cwd = ctx, make(chan struct{}), "/owned/work"
			defer b.close()
			b.native = func(context.Context, nativeCallback) (json.RawMessage, error) {
				if ending == "callback-error" {
					return nil, errors.New("PRIVATE_NATIVE_ERROR_CANARY")
				}
				return json.RawMessage(`{"decision":"accept"}`), nil
			}
			b.nativeCheck = func(nativeCallback) bool { return ending != "stale-authority" }
			if ending == "never-policy" {
				b.nativePolicy = "never"
			}
			var diagnostics bytes.Buffer
			b.onDiagnostic = func(format string, args ...any) { fmt.Fprintf(&diagnostics, format+"\n", args...) }
			complete := make(chan struct{})
			b.nativeComplete = func(nativeCallback) { close(complete) }
			b.event([]byte(`{"id":"exact-rpc","method":"item/commandExecution/requestApproval","params":{"threadId":"thread","turnId":"turn","itemId":"exact-item","command":"PRIVATE_COMMAND_CANARY","cwd":"/owned/work","availableDecisions":["accept"]}}`))
			select {
			case <-complete:
			case <-ctx.Done():
				t.Fatal("native refusal did not complete")
			}
			b.finish()
			if !strings.HasPrefix(output.String(), needsHumanMarker+"\n"+nativeStatusPrefix) || b.failureText() != "" {
				t.Fatalf("output=%q failure=%q", output.String(), b.failureText())
			}
			log := diagnostics.String()
			if strings.Count(log, "terminal=needs_human") != 1 || strings.Contains(log, "PRIVATE_") {
				t.Fatalf("missing or unsafe terminal diagnostics: %q", log)
			}
		})
	}
}
