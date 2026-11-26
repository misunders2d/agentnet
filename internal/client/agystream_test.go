package client

import (
	"encoding/json"
	"errors"
	"io"
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

// agyTestDeniedResult is agy 1.5.18's result for a turn whose tool it
// auto-denied: SUCCESS, with what was denied (captured live, November 26).
func agyTestDeniedResult(id string) string {
	raw, _ := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"conversation_id": id, "status": "SUCCESS", "response": "", "denied_actions": []map[string]string{{"action": "command", "display_name": "Bash"}}}})
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

// Review re-checks (MEL-828, Codex and agy): a result listing auto-denied
// actions needs the person, whatever stderr's timing; a result that merely
// quotes the denial notice is an answer; a broken event ends a warm turn at
// once instead of holding it until timeout; a result read before the turn's
// message began to be written is not its answer, one read while the write is
// still finishing is (the worker then waits for the write to finish).
func TestAgyWarmTurnGuards(t *testing.T) {
	notice := "run_command required approval that headless mode cannot prompt for, so it was auto-denied."
	var stream agyStream
	stream.Write([]byte(agyTestDeniedResult(agyTestConversation) + "\n"))
	if _, status, attention := stream.result(nil, &agyDiagnostics{}); status == envelope.StatusDone || !strings.Contains(attention, "native tool approval") {
		t.Fatalf("a result listing denied actions became %s %q", status, attention)
	}
	for _, none := range []string{`null`, `[]`, `{}`} {
		var s agyStream
		s.Write([]byte(`{"event":"result","result":{"conversation_id":"` + agyTestConversation + `","status":"SUCCESS","response":"ok","denied_actions":` + none + `}}` + "\n"))
		if _, status, _ := s.result(nil, &agyDiagnostics{}); status != envelope.StatusDone {
			t.Fatalf("denied_actions %s became %s", none, status)
		}
	}
	stream = agyStream{}
	stream.Write([]byte(agyTestResult(agyTestConversation, "SUCCESS", "the code checks for: "+notice) + "\n"))
	if body, status, _ := stream.result(nil, &agyDiagnostics{}); status != envelope.StatusDone || !strings.Contains(body, "auto-denied") {
		t.Fatalf("an answer quoting the notice became %s %q", status, body)
	}
	full := agyTestResult(agyTestConversation, "SUCCESS", "ok")
	broken := newAgyWarmTurn(nil, "")
	broken.state.Store(agyTurnWriting)
	bs := agyStream{onResult: broken.settle}
	bs.Write([]byte(full[:len(full)/2] + "\n"))
	if !bs.damaged || !isClosed(broken.settled) {
		t.Fatalf("a broken event did not end the warm turn: damaged=%v", bs.damaged)
	}
	early := agyStream{onResult: newAgyWarmTurn(nil, "").settle}
	early.Write([]byte(full + "\n"))
	if !early.damaged {
		t.Fatal("a result before the message was written was taken")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	turn := newAgyWarmTurn(w, strings.Repeat("x", 1<<20))
	turn.begin() // nobody reads: the write stays unfinished
	eventually(t, "write begun", turn.begun)
	during := agyStream{onResult: turn.settle}
	during.Write([]byte(full + "\n"))
	if during.damaged || !isClosed(turn.settled) || isClosed(turn.sent) {
		t.Fatal("a result read while the write was finishing was refused")
	}
	// A reused process that speaks before its message refuses the turn, and
	// the message is never written; output after the write starts is its own.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	strict := newAgyWarmTurn(pw, "never sent")
	strict.strict = true
	ss := agyStream{onResult: strict.settle, onOutput: strict.early}
	ss.Write([]byte(`{"event":"step_update","step_update":{}}`))
	strict.begin()
	if !ss.damaged || !isClosed(strict.settled) || !isClosed(strict.failed) || strict.err != errAgyEarlyOutput || strict.begun() {
		t.Fatalf("early output did not refuse the turn: damaged=%v err=%v", ss.damaged, strict.err)
	}
	pw.Close()
	if b, _ := io.ReadAll(pr); len(b) != 0 {
		t.Fatalf("a refused turn's message was written: %q", b)
	}
	dirty := newAgyWarmTurn(nil, "")
	dirty.strict, dirty.clean = true, func() bool { return false }
	dirty.begin()
	if !isClosed(dirty.failed) || dirty.begun() {
		t.Fatal("output between turns did not refuse the turn")
	}
	// A reused process announced its conversation once, on its first turn:
	// later turns still report the answer as it starts.
	answering := 0
	reused := agyStream{conversation: agyTestConversation, expected: agyTestConversation, onActivity: func(nativeActivity) { answering++ }}
	reused.Write([]byte(`{"event":"step_update","step_update":{"conversation_id":"` + agyTestConversation + `","step_type":"agent_response"}}` + "\n"))
	if answering != 1 {
		t.Fatalf("warm turn activity %d", answering)
	}
	// A reused agy's result counts only after agy reported reading this
	// turn's message (its user_input step): a late result read while the
	// message was being written must not become this turn's answer.
	for _, tc := range []struct {
		name, input string // input: the conversation of a user_input step, "" for none
		want        string
	}{{"stale result", "", envelope.StatusFailed}, {"another conversation's input", "00000000-0000-4000-8000-000000000000", envelope.StatusFailed}, {"answer after input", agyTestConversation, envelope.StatusDone}} {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pr.Close()
			defer pw.Close()
			go io.Copy(io.Discard, pr)
			turn := newAgyWarmTurn(pw, "next question")
			turn.strict, turn.clean = true, func() bool { return true }
			s := agyStream{conversation: agyTestConversation, expected: agyTestConversation, onResult: turn.settle, onOutput: turn.early, onInput: turn.begun}
			turn.begin()
			if !turn.begun() {
				t.Fatal("the turn's message was not begun")
			}
			if tc.input != "" {
				s.Write([]byte(`{"event":"step_update","step_update":{"conversation_id":"` + tc.input + `","state":"DONE","step_type":"user_input"}}` + "\n"))
			}
			s.Write([]byte(agyTestResult(agyTestConversation, "SUCCESS", "an answer") + "\n"))
			if !isClosed(turn.settled) {
				t.Fatal("the result did not settle the turn")
			}
			if _, status, _ := s.result(nil, &agyDiagnostics{}); status != tc.want {
				t.Fatalf("status %q, want %q (damaged=%v)", status, tc.want, s.damaged)
			}
		})
	}
}

// Output a warm agy process writes between turns makes it unfit for the
// next one: an event then could be taken for that turn's answer.
func TestAgyWarmStrayOutputUnfit(t *testing.T) {
	p := &warmPool{limits: warmLimits{lifetime: time.Hour}}
	w := &warmProc{pool: p, harness: "agy", fp: "f", born: time.Now(), exited: make(chan struct{})}
	if ok, why := p.fits(w, "f", time.Minute); !ok {
		t.Fatalf("clean idle agy unfit: %s", why)
	}
	w.out.Write([]byte(`{"event":"result"}` + "\n"))
	if ok, _ := p.fits(w, "f", time.Minute); ok {
		t.Fatal("agy that wrote between turns was reused")
	}
}
