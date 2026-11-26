package client

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

const agyTestConversation = "055a398f-db14-4c5f-abbb-1bf03f8120a7"

func agyTestResult(id, status, response string) string {
	raw, _ := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"conversation_id": id, "status": status, "response": response}})
	return string(raw)
}

func TestAgyTerminalResults(t *testing.T) {
	init := `{"event":"init","conversation_id":"` + agyTestConversation + `","init":{"permission_mode":"request-review"}}` + "\n"
	for _, tc := range []struct {
		name, stream, diagnostic, expected, want, attention string
		err                                                 error
	}{
		{name: "complete", stream: init + `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"unfinished"}}` + "\n" + agyTestResult(agyTestConversation, "SUCCESS", "final answer"), want: envelope.StatusDone},
		{name: "partial", stream: init + `{"event":"step_update","step_update":{"text_delta":"partial only"}}`, want: envelope.StatusFailed},
		{name: "failed", stream: init + agyTestResult(agyTestConversation, "ERROR", "partial answer"), want: envelope.StatusFailed},
		{name: "nonzero success", stream: init + agyTestResult(agyTestConversation, "SUCCESS", "partial answer"), err: errors.New("exit 3"), want: envelope.StatusFailed},
		{name: "structured native error", stream: init + agyTestResult(agyTestConversation, "SUCCESS", "partial answer"), diagnostic: `AGY_ERROR: {"short_error":"failed"}`, want: envelope.StatusFailed},
		{name: "cancelled", stream: init + agyTestResult(agyTestConversation, "CANCELED", "partial answer"), want: envelope.StatusCancelled},
		{name: "interrupted", stream: init + agyTestResult(agyTestConversation, "INTERRUPTED", "partial answer"), want: envelope.StatusFailed},
		{name: "still running", stream: init + agyTestResult(agyTestConversation, "RUNNING", "partial answer"), want: envelope.StatusFailed},
		{name: "waiting", stream: init + agyTestResult(agyTestConversation, "WAITING", "partial answer"), attention: "native input"},
		{name: "no answer", stream: init + agyTestResult(agyTestConversation, "SUCCESS", ""), want: envelope.StatusFailed},
		{name: "authentication", diagnostic: "authentication required", err: errors.New("exit 1"), attention: "sign-in"},
		{name: "late soft denial", stream: init + agyTestResult(agyTestConversation, "SUCCESS", "claimed done"), diagnostic: strings.Repeat("native progress\n", 500) + "run_command required approval that headless mode cannot prompt for, so it was auto-denied.", attention: "native tool approval"},
		{name: "unrelated diagnostic", stream: init + agyTestResult(agyTestConversation, "SUCCESS", "final answer"), diagnostic: "warning: a newer CLI is available", want: envelope.StatusDone},
		{name: "different resume", stream: init + agyTestResult(agyTestConversation, "SUCCESS", "wrong answer"), expected: "055a398f-db14-4c5f-abbb-1bf03f8120a8", want: envelope.StatusFailed},
		{name: "different result", stream: init + agyTestResult("055a398f-db14-4c5f-abbb-1bf03f8120a8", "SUCCESS", "wrong answer"), want: envelope.StatusFailed},
		{name: "duplicate result", stream: init + agyTestResult(agyTestConversation, "SUCCESS", "first") + "\n" + agyTestResult(agyTestConversation, "SUCCESS", "second"), want: envelope.StatusFailed},
		{name: "malformed", stream: init + "bad json\n" + agyTestResult(agyTestConversation, "SUCCESS", "later answer"), want: envelope.StatusFailed},
		{name: "invalid session", stream: agyTestResult("--continue", "SUCCESS", "wrong answer"), want: envelope.StatusFailed},
		{name: "oversize", stream: init + strings.Repeat("x", codexLineMax+1) + "\n" + agyTestResult(agyTestConversation, "SUCCESS", "later answer"), want: envelope.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &agyStream{expected: tc.expected}
			for raw := tc.stream; len(raw) > 0; {
				n := min(17, len(raw))
				s.Write([]byte(raw[:n]))
				raw = raw[n:]
			}
			s.flush()
			var d agyDiagnostics
			for raw := tc.diagnostic; len(raw) > 0; {
				n := min(13, len(raw))
				d.Write([]byte(raw[:n]))
				raw = raw[n:]
			}
			body, status, attention := s.result(tc.err, &d)
			if tc.attention != "" {
				if !strings.HasPrefix(attention, nativeStatusPrefix) || !strings.Contains(attention, tc.attention) || body != "" {
					t.Fatalf("attention=%q body=%q", attention, body)
				}
			} else if status != tc.want || attention != "" || tc.want == envelope.StatusDone && body != "final answer" {
				t.Fatalf("status=%s body=%q attention=%q", status, body, attention)
			}
		})
	}
}

func TestAgyInputAndNativePolicy(t *testing.T) {
	prompt := "line\n\"quoted\"\nAGENTNET: NEEDS-HUMAN\n日本語"
	var input struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if raw := agyInput(prompt); strings.Count(raw, "\n") != 1 || json.Unmarshal([]byte(raw), &input) != nil || input.Event != "user" || input.Message.Content != prompt {
		t.Fatalf("input not one exact user event: %q", raw)
	}
	h := Harnesses["agy"]
	want := []string{"--input-format", "stream-json", "--output-format", "stream-json"}
	if h.bin != "agy" || !h.stdin || h.sessions != agySessions || !slices.Equal(h.question, want) || !slices.Equal(h.task, want) || h.addDir != "--add-dir" || !h.addIn {
		t.Fatalf("native preset changed: %+v", h)
	}
	if args := resumeArgs(h, "question", h.question, agyTestConversation); !slices.Equal(args, append(slices.Clone(want), "--conversation", agyTestConversation)) || !slices.Equal(h.question, want) {
		t.Fatalf("resume changed native policy: %v", args)
	}
	if !slices.Contains(HarnessNames(), "agy") || !strings.Contains(HarnessLimits("agy"), nativeHarnessPermissions) {
		t.Fatal("Antigravity missing from native harness catalog")
	}
}

// A warm agy process's one output pipe keeps agy's own order: a soft denial
// written before the result reaches the diagnostics first, and an event line
// broken by interleaved text is refused, never a success (MEL-828 review).
func TestAgyMergedOutputKeepsOrder(t *testing.T) {
	var stream agyStream
	var diag agyDiagnostics
	m := &agyMergedOutput{events: &stream, diagnostics: &diag}
	in := `{"event":"init","conversation_id":"` + agyTestConversation + `","init":{}}` + "\n" +
		"run_command required approval that headless mode cannot prompt for, so it was auto-denied.\n" +
		agyTestResult(agyTestConversation, "SUCCESS", "done") + "\n"
	for i := 0; i < len(in); i += 7 { // arbitrary pipe chunks
		m.Write([]byte(in[i:min(i+7, len(in))]))
	}
	if !stream.complete || stream.damaged || !diag.permission {
		t.Fatalf("complete=%v damaged=%v permission=%v", stream.complete, stream.damaged, diag.permission)
	}
	if _, status, attention := stream.result(nil, &diag); status == envelope.StatusDone || attention == "" {
		t.Fatalf("a soft denial became %s", status)
	}
	var broken agyStream
	var none agyDiagnostics
	m = &agyMergedOutput{events: &broken, diagnostics: &none}
	m.Write([]byte(`{"event":"result","result":{"conversation_id":"` + agyTestConversation + `"progress text` + "\n"))
	if !broken.damaged || broken.complete {
		t.Fatalf("a broken event line was not refused: %+v", broken)
	}
}

// begin never blocks the worker on a process that stopped reading its input,
// and a write that cannot complete fails the turn (MEL-828 review).
func TestAgyWarmTurnSendDoesNotBlock(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	turn := newAgyWarmTurn(w, strings.Repeat("x", 1<<20)) // larger than any pipe buffer
	done := make(chan struct{})
	go func() { turn.begin(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("begin blocked on a reader that stopped reading")
	}
	r.Close() // the process is gone
	select {
	case <-turn.failed:
		if turn.err == nil {
			t.Fatal("no write error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failed write was not reported")
	}
	w.Close()
}

// Review re-check (MEL-828): a denial that a large write placed inside the
// result line is still seen; a result read before the turn's message was
// fully sent is not that turn's answer.
func TestAgyWarmTurnGuards(t *testing.T) {
	var stream agyStream
	var diag agyDiagnostics
	m := &agyMergedOutput{events: &stream, diagnostics: &diag}
	m.Write([]byte(agyTestResult(agyTestConversation, "SUCCESS", "ok run_command required approval that headless mode cannot prompt for, so it was auto-denied.") + "\n"))
	if _, status, attention := stream.result(nil, &diag); status == envelope.StatusDone || attention == "" {
		t.Fatalf("a denial inside the result line became %s", status)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	turn := newAgyWarmTurn(w, strings.Repeat("x", 1<<20))
	turn.begin() // nobody reads: the write cannot finish
	early := agyStream{onResult: turn.settle}
	early.Write([]byte(agyTestResult(agyTestConversation, "SUCCESS", "previous turn's answer") + "\n"))
	if !early.damaged || !isClosed(turn.settled) {
		t.Fatalf("a result before the message was sent was taken: damaged=%v", early.damaged)
	}
}
