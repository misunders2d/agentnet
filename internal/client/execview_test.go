package client

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A terminal answer the host sent dominates its earlier running status in
// the requester's view at once, and a running status older than
// ExecRunningMaxAge is stale even while the host is connected; the host's
// own timestamps are kept.
func TestExecViewSettlesOnAnswerAndAge(t *testing.T) {
	now := time.Now().Unix()
	req := ConversationMessage{ID: "q1", Dir: "out", Kind: envelope.KindQuestion}
	answer := ConversationMessage{ID: "a1", Dir: "in", ReplyTo: "q1", Status: envelope.StatusDone, SentAt: time.Unix(now-5, 0)}
	status, at := legacyAnswer([]ConversationMessage{req, answer}, "q1")
	if status != envelope.StatusDone || at != now-5 {
		t.Fatalf("answer %q at %d", status, at)
	}
	e := ExecView{State: stateRunning, At: now - 60, Host: "bob/laptop", Detail: "claimed"}
	e.settle(true, status, at, now)
	if e.State != envelope.StatusDone || e.At != now-5 || e.Detail != "" || e.Stale {
		t.Fatalf("answer did not dominate: %+v", e)
	}
	e = ExecView{State: stateRunning, At: now - int64(ExecRunningMaxAge/time.Second) - 1, Host: "bob/laptop"}
	e.settle(true, "", 0, now)
	if !e.Stale || e.State != stateRunning || e.At != now-int64(ExecRunningMaxAge/time.Second)-1 {
		t.Fatalf("old running not stale, or timestamp changed: %+v", e)
	}
	e = ExecView{State: stateRunning, At: now - 10, Host: "bob/laptop"}
	e.settle(true, "", 0, now)
	if e.Stale {
		t.Fatal("fresh running from a connected host marked stale")
	}
	e = ExecView{State: envelope.StatusFailed, At: now - 10}
	e.settle(false, envelope.StatusDone, now, now)
	if e.State != envelope.StatusFailed || !e.Stale {
		t.Fatalf("a terminal state was overwritten, or a disconnected host not stale: %+v", e)
	}
}
