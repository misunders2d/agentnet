//go:build !windows

package client

// Process groups are Unix-only (proc_unix.go): Windows records none, so
// killing a warm worker's group mid-turn is tested here.

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Acceptance 4: a warm worker lost during a turn, after the turn's input was
// accepted, is reported and never run again; the next message starts anew.
func TestWarmWorkerCrashReportedNotRerun(t *testing.T) {
	st, w, conv, pid := warmFixture(t, "pi", "0")
	t.Setenv("AGENTNET_NATIVE_STUB_HANG", "HANG")
	pool := warmPoolOf(t, w.bob)
	convAsk(t, w.alice, conv, pid, "first")
	eventually(t, "warm", func() bool { return pool.idleCount() == 1 })
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "HANG here")
	if err != nil {
		t.Fatal(err)
	}
	var job string
	var pgid int
	eventually(t, "the warm turn to accept its input", func() bool {
		row := w.bob.store.db.QueryRow(`SELECT id, coalesce(run_pgid,0) FROM inbox WHERE kind=? AND state=? ORDER BY rowid DESC LIMIT 1`, envelope.KindQuestion, stateRunning)
		if row.Scan(&job, &pgid) != nil || pgid == 0 {
			return false
		}
		return strings.Contains(st.prompt(), "HANG here")
	})
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// An agent's failed turn is reported to its person, not sent to the chat.
	eventually(t, "the lost turn reported failed", func() bool { return jobState(t, w.bob, job) == stateJobFailed })
	var detail string
	if err := w.bob.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, job).Scan(&detail); err != nil || !strings.Contains(detail, "Pi ended before its request settled") {
		t.Fatalf("lost turn's report %q (%v)", detail, err)
	}
	noReply(t, w.alice, conv, q.ID)
	state := jobState(t, w.bob, job)
	time.Sleep(500 * time.Millisecond)
	if runs := st.runs(); len(runs) != 1 || jobState(t, w.bob, job) != state {
		t.Fatalf("the lost turn ran again: %v, state %s -> %s", runs, state, jobState(t, w.bob, job))
	}
	t.Setenv("AGENTNET_NATIVE_STUB_HANG", "")
	convAsk(t, w.alice, conv, pid, "after the crash")
	if n := len(st.runs()); n != 2 {
		t.Fatalf("the next message after a crash: %d runs", n)
	}
}
