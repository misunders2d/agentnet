package client

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Antigravity's native stream-json contract, checked against agy 1.2.10:
// https://antigravity.google/docs/cli/headless/
// A cold worker sends one user event and closes stdin. A warm worker (warm.go)
// keeps the process and its input open and writes the next turn's user event
// into it: agy answers each event with its own terminal result on the same
// conversation (checked against agy 1.5.18). Only the terminal result is an
// answer; tool output and streamed commentary never become replies.
func agyInput(prompt string) string {
	raw, _ := json.Marshal(struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}{Event: "user", Message: struct {
		Content string `json:"content"`
	}{Content: prompt}})
	return string(raw) + "\n"
}

type agyStream struct {
	onActivity               func(nativeActivity)
	onResult                 func() bool // a warm turn's terminal result was read; false: not this turn's
	line                     []byte
	skipping, damaged        bool
	conversation, expected   string
	complete                 bool
	status, response, detail string
}

func (s *agyStream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if !s.skipping {
			if len(s.line)+len(chunk) > codexLineMax {
				s.skipping, s.line = true, nil
			} else {
				s.line = append(s.line, chunk...)
			}
		}
		if i < 0 {
			break
		}
		s.flush()
		p = p[i+1:]
	}
	return n, nil
}

func (s *agyStream) flush() {
	defer func() { s.line, s.skipping = s.line[:0], false }()
	if s.skipping {
		s.damaged = true
		return
	}
	if len(bytes.TrimSpace(s.line)) == 0 {
		return
	}
	var ev struct {
		Event        string          `json:"event"`
		Conversation string          `json:"conversation_id"`
		StepUpdate   json.RawMessage `json:"step_update"`
		Result       *struct {
			Conversation string `json:"conversation_id"`
			Status       string `json:"status"`
			Response     string `json:"response"`
			Error        string `json:"error"`
		} `json:"result"`
	}
	if json.Unmarshal(s.line, &ev) != nil || ev.Event == "" {
		s.damaged = true
		return
	}
	switch ev.Event {
	case "init":
		s.setConversation(ev.Conversation)
	case "step_update":
		if s.onActivity != nil && !s.complete && !s.damaged && s.conversation != "" && (ev.Conversation == "" || ev.Conversation == s.conversation) {
			var step struct {
				Type string `json:"step_type"`
			}
			if json.Unmarshal(ev.StepUpdate, &step) == nil && step.Type == "agent_response" {
				s.onActivity(activityAnswer)
			}
		}
	case "result":
		if s.complete || ev.Result == nil {
			s.damaged = true
			return
		}
		s.complete = true
		s.setConversation(ev.Result.Conversation)
		s.status, s.response, s.detail = ev.Result.Status, ev.Result.Response, ev.Result.Error
		if s.onResult != nil && !s.onResult() {
			s.damaged = true // a result before this turn's message was sent is not its answer
		}
	}
}

// agyWarmTurn is one turn of a warm agy process: begin writes its user event
// into the running process; settled closes once that turn's terminal result
// is read. The process input stays open for the next turn.
type agyWarmTurn struct {
	in      io.WriteCloser
	prompt  string
	settled chan struct{}
	sent    chan struct{} // the whole event was written
	failed  chan struct{} // the event could not be written; err says why
	err     error
	once    sync.Once
}

func newAgyWarmTurn(in io.WriteCloser, prompt string) *agyWarmTurn {
	return &agyWarmTurn{in: in, prompt: prompt, settled: make(chan struct{}), sent: make(chan struct{}), failed: make(chan struct{})}
}

// begin sends the event without blocking the worker: a process that stops
// reading cannot hold off a stop or timeout, which end the process and so
// the write. A failed or partial write fails the turn; nothing is resent.
func (t *agyWarmTurn) begin() {
	go func() {
		if _, err := io.WriteString(t.in, agyInput(t.prompt)); err != nil {
			t.err = err
			close(t.failed)
			return
		}
		close(t.sent)
	}()
}

// settle ends the turn on its terminal result. It reports false for a result
// read before the turn's whole message was written: agy answers an event
// only after reading it, so that result is not this turn's (the turn fails).
func (t *agyWarmTurn) settle() bool {
	t.once.Do(func() { close(t.settled) })
	return isClosed(t.sent)
}

func (s *agyStream) setConversation(id string) {
	compact := strings.ReplaceAll(id, "-", "")
	_, err := hex.DecodeString(compact)
	if len(id) != 36 || len(compact) != 32 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || err != nil ||
		s.conversation != "" && s.conversation != id || s.expected != "" && s.expected != id {
		s.damaged = true
		return
	}
	s.conversation = id
}

// Diagnostics are read on stderr's own goroutine, independently of stdout.
// Keep scanning after the bounded diagnostic buffer fills: a late soft denial
// must not become success merely because native progress used its first 4 KiB.
//
// A warm agy process writes its stdout and stderr into one pipe
// (agyMergedOutput), so a diagnostic it wrote before its result is always
// read before it; the lock covers the reads after the turn.
type agyDiagnostics struct {
	mu                       sync.Mutex
	tail                     string
	permission, auth, failed bool
}

func (d *agyDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	text := d.tail + strings.ToLower(string(p))
	d.permission = d.permission || strings.Contains(text, "headless mode cannot prompt for") && strings.Contains(text, "auto-denied")
	d.auth = d.auth || strings.Contains(text, "authentication required") || strings.Contains(text, "unauthenticated")
	d.failed = d.failed || strings.Contains(text, "agy_error:")
	d.tail = text[max(0, len(text)-512):]
	return len(p), nil
}

// result returns either a terminal answer/status or a local needs-human reason,
// which is AgentNet's own text and says so.
// Vendor approval advice and tool diagnostics stay out of the peer's answer.
func (s *agyStream) result(runErr error, d *agyDiagnostics) (body, status, attention string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	status = envelope.StatusFailed
	detail := strings.ToLower(s.detail)
	switch {
	case d.permission:
		return "", status, nativeStatusPrefix + "Antigravity could not obtain a native tool approval. Review the request in your native setup; AgentNet did not change permissions or retry it."
	case d.auth || strings.Contains(detail, "authentication required") || strings.Contains(detail, "unauthenticated"):
		return "", status, nativeStatusPrefix + "Antigravity requires sign-in. Open agy in its configured directory and sign in, then explicitly retry this request."
	case s.status == "WAITING" && s.complete && !s.damaged:
		return "", status, nativeStatusPrefix + "Antigravity is waiting for native input. Open agy in its configured directory to review what it needs; this request was not retried."
	case s.damaged:
		return "Antigravity's result or conversation identity could not be verified; no partial answer was used.", status, ""
	case s.complete && s.status == "CANCELED":
		return "Antigravity cancelled the run.", envelope.StatusCancelled, ""
	case runErr != nil:
		return fmt.Sprintf("Antigravity failed: %v. Review its native diagnostics in the configured directory; this request was not retried.", runErr), status, ""
	case !s.complete:
		return "Antigravity did not report a completed turn; no partial answer was used.", status, ""
	case s.status != "SUCCESS" || d.failed:
		return "Antigravity did not complete the run (" + s.status + "); review its native diagnostics. This request was not retried.", status, ""
	case strings.TrimSpace(s.response) == "":
		return "Antigravity completed without an answer.", status, ""
	default:
		return s.response, envelope.StatusDone, ""
	}
}

// agyMergedOutput splits a warm agy process's single output pipe (stdout and
// stderr, in the order agy wrote them) into its stream-json events and its
// diagnostics. A line that is not a stream-json event is a diagnostic; an
// event line broken by interleaved text fails to parse and fails the turn
// closed (agyStream marks it damaged), never as success.
type agyMergedOutput struct {
	events, diagnostics io.Writer
	line                []byte
	overflow            bool // an over-long line goes to the events decoder, which refuses it
}

func (m *agyMergedOutput) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i+1]
		}
		p = p[len(chunk):]
		if m.overflow {
			_, _ = m.events.Write(chunk)
			m.overflow = i < 0
			continue
		}
		m.line = append(m.line, chunk...)
		if i < 0 {
			if len(m.line) > codexLineMax {
				_, _ = m.events.Write(m.line)
				m.line, m.overflow = m.line[:0], true
			}
			continue
		}
		var ev struct {
			Event string `json:"event"`
		}
		trimmed := bytes.TrimSpace(m.line)
		switch {
		case len(trimmed) > 0 && trimmed[0] == '{':
			// An event, or one broken by interleaved text (refused by the
			// decoder, never a success). Diagnostic text that a write larger
			// than the pipe's atomic size placed inside it is still seen: a
			// false match only asks the person, never answers.
			if json.Unmarshal(trimmed, &ev) != nil || ev.Event == "" || agyDiagnosticText(trimmed) {
				_, _ = m.diagnostics.Write(m.line)
			}
			_, _ = m.events.Write(m.line)
		default:
			_, _ = m.diagnostics.Write(m.line)
		}
		m.line = m.line[:0]
	}
	return n, nil
}

// agyDiagnosticText reports whether line carries agy's soft-denial notice,
// the diagnostic that would otherwise let a denied turn pass as a success.
func agyDiagnosticText(line []byte) bool {
	l := bytes.ToLower(line)
	return bytes.Contains(l, []byte("headless mode cannot prompt for")) && bytes.Contains(l, []byte("auto-denied"))
}
