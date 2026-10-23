package client

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// sessionStub exercises Claude session flags and Codex's native app-server.
func sessionStub(t *testing.T) *stub {
	st := installStub(t, "answer")
	x := installCodexSessionStub(t, st.log)
	t.Setenv("STUB_THREAD", "thread-1")
	t.Setenv("STUB_NOISE", "0")
	t.Setenv("STUB_TURN", "")
	t.Setenv("STUB_NO_AGENT", "")
	Harnesses["cstyle"] = harness{bin: Harnesses["stub"].bin, question: []string{"--question-mode", sessionOneShot}, task: []string{"--task-mode", sessionOneShot}, stdin: true, sessions: claudeSessions}
	Harnesses["cstyle2"] = harness{bin: Harnesses["stub"].bin, question: []string{"--other-flags", sessionOneShot}, task: []string{"--task-mode", sessionOneShot}, stdin: true, sessions: claudeSessions}
	Harnesses["xstyle"] = harness{bin: x, question: []string{"app-server"}, task: []string{"app-server"}, stdin: true, sessions: codexSessions}
	t.Cleanup(func() { delete(Harnesses, "cstyle"); delete(Harnesses, "cstyle2"); delete(Harnesses, "xstyle") })
	return st
}

func (s *stub) runs() []string {
	data, _ := os.ReadFile(s.log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(l, "run ") {
			out = append(out, l)
		}
	}
	return out
}

func sessionOf(t *testing.T, a *Agent, id string) *sessionRef {
	t.Helper()
	var raw sql.NullString
	if err := a.store.db.QueryRow(`SELECT session_ref FROM inbox WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !raw.Valid {
		return nil
	}
	var ref sessionRef
	json.Unmarshal([]byte(raw.String), &ref)
	return &ref
}

// ask sends a question from alice (optionally in reply to replyTo) and
// waits for bob's answer.
func ask(t *testing.T, w *world, replyTo string) (q, answer string) {
	t.Helper()
	r, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion, ReplyTo: replyTo})
	if err != nil {
		t.Fatal(err)
	}
	var ans Message
	eventually(t, "answer to "+r.ID, func() bool { var ok bool; ans, ok = findReply(w.alice, r.ID); return ok })
	return r.ID, ans.ID
}

// A conversation's next question resumes the session its first question
// started, also after the daemon restarts; a duplicate delivery runs nothing.
func TestSessionCreateAndResumeAcrossRestart(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "cstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	if ref == nil || len(ref.ID) != 36 || ref.Mode != "question" || ref.Harness != "cstyle" {
		t.Fatalf("recorded session %+v", ref)
	}
	if run := st.runs()[0]; !strings.Contains(run, "--session-id "+ref.ID) || strings.Contains(run, sessionOneShot) {
		t.Fatalf("first run: %s", run)
	}
	m, _ := w.bob.store.inboxMessage(q1)
	w.bob.store.addInbox(envelope.Inner{ID: q1, From: w.alice.Address, To: w.bob.Address, TS: m.SentAt.Unix(), Kind: envelope.KindQuestion, Body: "q"}, "")

	stop()
	runWith(t, w, w.bob, RunOptions{})
	q2, _ := ask(t, w, a1)
	runs := st.runs()
	if len(runs) != 2 || !strings.Contains(runs[1], "--resume "+ref.ID) || strings.Contains(runs[1], "--session-id") {
		t.Fatalf("runs after restart: %v", runs)
	}
	if r2 := sessionOf(t, w.bob, q2); r2 == nil || r2.ID != ref.ID {
		t.Fatalf("second job's session %+v", r2)
	}
}

// Another peer, another mode, directory or preset each get a new session;
// a task in the conversation still waits for acceptance.
func TestSessionIsolation(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	setResponder(t, w.bob, "cstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	w.bob.Approve(carol.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, a1 := ask(t, w, "")
	first := sessionOf(t, w.bob, q1).ID
	fresh := func(id, what string) {
		t.Helper()
		if ref := sessionOf(t, w.bob, id); ref == nil || ref.ID == first {
			t.Fatalf("%s reused the first session: %+v", what, ref)
		}
		runs := st.runs()
		if last := runs[len(runs)-1]; strings.Contains(last, "--resume") {
			t.Fatalf("%s resumed: %s", what, last)
		}
	}

	// Another peer replying to alice's question id starts its own session.
	cq, err := carol.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "mine", Kind: envelope.KindQuestion, ReplyTo: q1})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, cq.ID, stateAnswered)
	fresh(cq.ID, "another peer")

	// A task in the conversation waits for acceptance, then runs in a new
	// task-mode session.
	runs := len(st.runs())
	task, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "do it", Kind: envelope.KindTask, ReplyTo: a1})
	waitState(t, w.bob, task.ID, stateAwaiting)
	time.Sleep(200 * time.Millisecond)
	if len(st.runs()) != runs {
		t.Fatal("a task ran before it was accepted")
	}
	w.bob.Accept(task.ID)
	waitState(t, w.bob, task.ID, stateAnswered)
	fresh(task.ID, "task mode")
	if ref := sessionOf(t, w.bob, task.ID); ref.Mode != "task" {
		t.Fatalf("task session %+v", ref)
	}

	// Another harness, a changed preset (flags or program), another directory.
	setResponder(t, w.bob, "cstyle2", st.dir, time.Minute)
	q3, _ := ask(t, w, a1)
	fresh(q3, "another harness")
	setResponder(t, w.bob, "cstyle", st.dir, time.Minute)
	ref := sessionOf(t, w.bob, q1)
	ref.Preset = "from-an-older-program"
	data, _ := json.Marshal(ref)
	w.bob.store.db.Exec(`UPDATE inbox SET session_ref = ? WHERE id = ?`, string(data), q1)
	q4, _ := ask(t, w, a1)
	fresh(q4, "another preset")
	setResponder(t, w.bob, "cstyle", t.TempDir(), time.Minute)
	q5, _ := ask(t, w, a1)
	fresh(q5, "another directory")
}

// The preset changes with the flags and with the harness program.
func TestPresetIdentity(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "harness")
	if runtime.GOOS == "windows" {
		bin += ".exe" // LookPath finds only PATHEXT names there, as with real harnesses
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Fatalf("fixture harness not resolvable: %v", err)
	}
	h := harness{bin: bin}
	a := presetID(h, "question", []string{"--x"})
	if a != presetID(h, "question", []string{"--x"}) || a == presetID(h, "question", []string{"--y"}) || a == presetID(h, "task", []string{"--x"}) {
		t.Fatal("preset does not follow the flags and mode")
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n# upgraded\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if a == presetID(h, "question", []string{"--x"}) {
		t.Fatal("preset does not follow the program")
	}
}

// An interrupted job's session is not resumed, and the search does not
// fall back to an older clean session; a rerun starts a new session too.
func TestSessionAfterInterruption(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "cstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, a1 := ask(t, w, "")
	clean := sessionOf(t, w.bob, q1).ID

	os.Setenv("STUB_MODE", "sleep")
	q2, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "slow", Kind: envelope.KindQuestion, ReplyTo: a1})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q2.ID, stateRunning)
	eventually(t, "stub child", func() bool { _, err := os.Stat(st.log + ".child"); return err == nil })
	stop()
	if s, _ := w.bob.store.jobState(q2.ID); s != stateInterrupt || sessionOf(t, w.bob, q2.ID).ID != clean {
		t.Fatalf("q2 state %s", s)
	}
	os.Setenv("STUB_MODE", "answer")
	runWith(t, w, w.bob, RunOptions{})

	// Its first recorded ancestor is the interrupted q2: new session, even
	// though q1's session further up ended cleanly.
	q3, _ := ask(t, w, q2.ID)
	if ref := sessionOf(t, w.bob, q3); ref == nil || ref.ID == clean {
		t.Fatalf("q3 fell back to the older session: %+v", ref)
	}
	// Running q2 again explicitly starts a new session as well.
	w.bob.Accept(q2.ID)
	waitState(t, w.bob, q2.ID, stateAnswered)
	if ref := sessionOf(t, w.bob, q2.ID); ref.ID == clean {
		t.Fatal("the rerun resumed its interrupted session")
	}
	if runs := st.runs(); strings.Contains(runs[len(runs)-1], "--resume") {
		t.Fatalf("rerun resumed: %s", runs[len(runs)-1])
	}
}

// Codex saves the native thread ID, then resumes only the exact owned thread.
func TestCodexStyleSession(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "xstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, a1 := ask(t, w, "")
	if ref := sessionOf(t, w.bob, q1); ref == nil || ref.ID != "thread-1" {
		t.Fatalf("captured %+v", ref)
	}
	q2, a2 := ask(t, w, a1)
	runs := st.runs()
	if len(runs) != 2 || runs[0] != "run app-server" || runs[1] != "run app-server" {
		t.Fatalf("native launch %v", runs)
	}
	raw, _ := os.ReadFile(st.log + ".rpc")
	if !strings.Contains(string(raw), "thread/start") || !strings.Contains(string(raw), "thread/resume thread-1") {
		t.Fatalf("missing owned resume: %s", raw)
	}
	if m, _ := w.alice.store.inboxMessage(a2); m == nil || m.Body != "codex native answer" || sessionOf(t, w.bob, q2).ID != "thread-1" {
		t.Fatalf("resumed answer %+v", m)
	}
	// Missing thread identity cannot be treated as a completed anonymous run.
	t.Setenv("STUB_THREAD", "")
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "missing identity", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateNeedHuman)
	if ref := sessionOf(t, w.bob, q.ID); ref != nil {
		t.Fatalf("recorded without ID: %+v", ref)
	}
}

// If the session cannot be recorded, the result is not used: with codex
// (known only after the run) the answer is not sent; with claude (known
// before) nothing runs at all.
func TestSessionRecordFailure(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	w.bob.Approve(w.alice.Address)
	if _, err := w.bob.store.db.Exec(`CREATE TRIGGER no_session BEFORE UPDATE OF session_ref ON inbox BEGIN SELECT RAISE(ABORT, 'test: disk full'); END`); err != nil {
		t.Fatal(err)
	}
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	for _, h := range []string{"xstyle", "cstyle"} {
		setResponder(t, w.bob, h, st.dir, time.Minute)
		before := len(st.runs())
		q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: h, Kind: envelope.KindQuestion})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, w.bob, q.ID, stateJobFailed)
		m, _ := w.bob.store.inboxMessage(q.ID)
		time.Sleep(200 * time.Millisecond)
		if _, ok := findReply(w.alice, q.ID); ok {
			t.Fatalf("%s: answer sent although its session was not recorded", h)
		}
		ran := len(st.runs()) - before
		if h == "xstyle" && (ran != 1 || !strings.Contains(m.Detail, "result was not used")) {
			t.Fatalf("%s: runs %d, detail %q", h, ran, m.Detail)
		}
		if h == "cstyle" && (ran != 0 || !strings.Contains(m.Detail, "nothing was run")) {
			t.Fatalf("%s: runs %d, detail %q", h, ran, m.Detail)
		}
	}
}

// Only the latest job in a session can lead to its resume: a branch from an
// older message of the conversation starts fresh, whether the later job
// failed, was cancelled or succeeded; and without a recorded head (sessions
// from before heads were kept) nothing is resumed.
func TestSessionOnlyLatestJobResumes(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "cstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	resumed := func() bool { runs := st.runs(); return strings.Contains(runs[len(runs)-1], "--resume") }

	q1, a1 := ask(t, w, "")
	s := sessionOf(t, w.bob, q1).ID
	q2, a2 := ask(t, w, a1) // continues the session: q2 is its head now
	if !resumed() || sessionOf(t, w.bob, q2).ID != s {
		t.Fatal("q2 did not resume")
	}
	q3, _ := ask(t, w, a1) // a branch from q1, after the successful q2
	if resumed() || sessionOf(t, w.bob, q3).ID == s {
		t.Fatal("a branch from an older message resumed the session")
	}
	if _, _ = ask(t, w, a2); !resumed() {
		t.Fatal("continuing from the head q2 did not resume")
	}

	// A failed and a cancelled job each leave their own session unresumable.
	for _, mode := range []string{"fail", "sleep"} {
		p, pa := ask(t, w, "")
		ps := sessionOf(t, w.bob, p).ID
		os.Setenv("STUB_MODE", mode)
		os.Remove(st.log + ".child")
		bad, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: mode, Kind: envelope.KindQuestion, ReplyTo: pa})
		if err != nil {
			t.Fatal(err)
		}
		if mode == "sleep" {
			waitState(t, w.bob, bad.ID, stateRunning)
			eventually(t, "stub child", func() bool { _, err := os.Stat(st.log + ".child"); return err == nil })
			w.bob.Cancel(bad.ID)
			waitState(t, w.bob, bad.ID, stateCancelled)
		} else {
			waitState(t, w.bob, bad.ID, stateJobFailed)
		}
		os.Setenv("STUB_MODE", "answer")
		r, _ := ask(t, w, pa) // back to p, whose session a later job used
		if ref := sessionOf(t, w.bob, r); resumed() || ref.ID == ps {
			t.Fatalf("after a %s job the older message resumed its session", mode)
		}
	}

	// A session recorded without a head is not resumed.
	h, ha := ask(t, w, "")
	w.bob.store.db.Exec(`DELETE FROM config WHERE k = ?`, headKey(*sessionOf(t, w.bob, h)))
	if _, _ = ask(t, w, ha); resumed() {
		t.Fatal("resumed a session without a recorded head")
	}
}

// Execution order, not arrival order, decides the head: of two held
// questions branching from the same answer, the one accepted first resumes
// the session and the other, run later, starts fresh.
func TestSessionHeadFollowsExecutionOrder(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "cstyle", st.dir, time.Minute)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "first", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q1.ID, stateHeld)
	w.bob.Accept(q1.ID)
	var a1 Message
	eventually(t, "answer", func() bool { var ok bool; a1, ok = findReply(w.alice, q1.ID); return ok })
	s := sessionOf(t, w.bob, q1.ID).ID
	early, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "arrives first", Kind: envelope.KindQuestion, ReplyTo: a1.ID})
	late, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "arrives second", Kind: envelope.KindQuestion, ReplyTo: a1.ID})
	waitState(t, w.bob, early.ID, stateHeld)
	waitState(t, w.bob, late.ID, stateHeld)
	w.bob.Accept(late.ID)
	waitState(t, w.bob, late.ID, stateAnswered)
	w.bob.Accept(early.ID)
	waitState(t, w.bob, early.ID, stateAnswered)
	if sessionOf(t, w.bob, late.ID).ID != s || sessionOf(t, w.bob, early.ID).ID == s {
		t.Fatalf("heads: late %+v early %+v (session %s)", sessionOf(t, w.bob, late.ID), sessionOf(t, w.bob, early.ID), s)
	}
}

// A reply link must name a message with the same agent.
func TestCheckReplyTo(t *testing.T) {
	w := newWorld(t, "")
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	runWith(t, w, w.bob, RunOptions{})
	m, _ := w.alice.Send(tctx(t), w.bob.Address, "hi", "")
	if err := w.alice.CheckReplyTo(m.ID, w.bob.Address); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.CheckReplyTo(m.ID, w.bob.Address+"#"+strings.Repeat("a", 32)); err != nil {
		t.Fatalf("session-addressed: %v", err)
	}
	if err := w.alice.CheckReplyTo(m.ID, carol.Address); err == nil || !strings.Contains(err.Error(), "not "+carol.Address) {
		t.Fatalf("wrong peer: %v", err)
	}
	if err := w.alice.CheckReplyTo("nope", w.bob.Address); err == nil || !strings.Contains(err.Error(), "no message nope") {
		t.Fatalf("unknown id: %v", err)
	}
}

// Tool/reasoning events never become the answer, and an incomplete native turn
// cannot publish a partial answer or silently replay.
func TestCodexAnswerFromEvents(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "xstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	t.Setenv("STUB_NOISE", "200000")
	q, a := ask(t, w, "")
	m, _ := w.alice.store.inboxMessage(a)
	if m.Body != "codex native answer" || sessionOf(t, w.bob, q).ID != "thread-1" {
		t.Fatalf("answer %+v", m)
	}
	// Each technical end fails the request with its exact reason and is never
	// presented as a human decision (oct23 T2).
	for mode, want := range map[string]string{
		"fail":     `xstyle failed: Codex ended the turn as "failed": fixture provider error`,
		"cut":      "xstyle failed: Codex ended before the turn completed",
		"empty":    "xstyle failed: Codex completed the turn without an answer",
		"approval": "xstyle failed: Codex sent an unreadable native request",
	} {
		t.Setenv("STUB_TURN", mode)
		// Each unrelated request starts a genuinely new native thread. Reusing
		// thread-1 would test the saved-thread fence before reaching this mode.
		t.Setenv("STUB_THREAD", "thread-"+mode)
		sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: mode, Kind: envelope.KindQuestion})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, w.bob, sent.ID, stateJobFailed)
		var reply Message
		eventually(t, mode+" failure reply", func() bool { var ok bool; reply, ok = findReply(w.alice, sent.ID); return ok })
		if reply.Status != envelope.StatusFailed || !strings.HasPrefix(reply.Body, want) || strings.Contains(reply.Body, "partial commentary") || strings.Contains(reply.Body, needsHumanMarker) {
			t.Fatalf("%s: %s %q", mode, reply.Status, reply.Body)
		}
		row, _ := w.bob.store.inboxMessage(sent.ID)
		if strings.Contains(row.Detail, "partial commentary") {
			t.Fatal("partial answer mistaken for completion")
		}
		if mode == "approval" {
			// This legacy stub omits itemId: it is a malformed approval,
			// not a supported callback. Its private Reason must not leak.
			if strings.Contains(row.Detail+reply.Body, "fixture native permission") {
				t.Fatalf("malformed native approval leaked its reason: %s", row.Detail)
			}
			var callbacks int
			if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM native_requests WHERE request=?`, sent.ID).Scan(&callbacks); err != nil || callbacks != 0 {
				t.Fatalf("malformed approval became actionable: callbacks=%d err=%v", callbacks, err)
			}
		}
	}
}
