package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// stubScript is a fake harness: it logs each run (cwd, args, stdin) and
// behaves according to $STUB_MODE: answer, sleep (with a child process
// whose pid it records), slow (1 s then answer) or fail.
const stubScript = `#!/bin/sh
echo "run cwd=$(pwd) args=$*" >> "$STUB_LOG"
cat > "$STUB_LOG.stdin"
case "$STUB_MODE" in
sleep) sleep 30 & echo $! > "$STUB_LOG.child"; wait ;;
slow) sleep 1; echo "stub answer" ;;
fail) echo "boom" >&2; exit 3 ;;
*) echo "stub answer" ;;
esac
`

type stub struct {
	log, dir string
}

// installStub registers harness "stub" (and "stub2") and returns its log.
func installStub(t *testing.T, mode string) *stub {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "stub.sh")
	os.WriteFile(bin, []byte(stubScript), 0o700)
	s := &stub{log: filepath.Join(dir, "log"), dir: t.TempDir()}
	t.Setenv("STUB_LOG", s.log)
	t.Setenv("STUB_MODE", mode)
	Harnesses["stub"] = harness{bin: bin, question: []string{"--question-mode"}, task: []string{"--task-mode"}, stdin: true}
	Harnesses["stub2"] = harness{bin: bin, question: []string{"--second"}, task: []string{"--second"}, stdin: true}
	t.Cleanup(func() { delete(Harnesses, "stub"); delete(Harnesses, "stub2") })
	return s
}

func (s *stub) count() int {
	data, _ := os.ReadFile(s.log)
	return strings.Count(string(data), "run cwd=")
}

func setResponder(t *testing.T, a *Agent, harness, dir string, timeout time.Duration) {
	t.Helper()
	if err := a.SetResponder(&Responder{Harness: harness, Dir: dir, Timeout: timeout}); err != nil {
		t.Fatal(err)
	}
}

func inboxRow(t *testing.T, a *Agent, id string) Message {
	t.Helper()
	msgs, _ := a.Inbox(false, false)
	for _, m := range msgs {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no inbox row %s", id)
	return Message{}
}

func findReply(a *Agent, to string) (Message, bool) {
	msgs, _ := a.Inbox(false, false)
	for _, m := range msgs {
		if m.ReplyTo == to {
			return m, true
		}
	}
	return Message{}, false
}

func waitState(t *testing.T, a *Agent, id, state string) {
	t.Helper()
	eventually(t, id+" -> "+state, func() bool {
		s, _ := a.store.jobState(id)
		return s == state
	})
}

func TestQuestionsNeedApprovalAndAnswersDoNotTrigger(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	setResponder(t, w.alice, "stub", st.dir, time.Minute) // alice has a responder too
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})

	q1, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "unapproved?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "held question", func() bool { s, _ := w.bob.store.jobState(q1.ID); return s == stateHeld })
	w.bob.Inbox(false, true) // reading must not make it eligible
	time.Sleep(200 * time.Millisecond)
	if s, _ := w.bob.store.jobState(q1.ID); s != stateHeld || st.count() != 0 {
		t.Fatalf("unapproved question state %s, runs %d", s, st.count())
	}

	w.bob.Approve(w.alice.Address)
	q2, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "what is up?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	var ans Message
	eventually(t, "automatic answer", func() bool { var ok bool; ans, ok = findReply(w.alice, q2.ID); return ok })
	if ans.Kind != envelope.KindAnswer || ans.Status != envelope.StatusDone || ans.Body != "stub answer" {
		t.Fatalf("answer = %+v", ans)
	}
	row := inboxRow(t, w.bob, q2.ID)
	if row.State != stateAnswered || row.Responder != "stub" {
		t.Fatalf("question row = %+v", row)
	}
	time.Sleep(300 * time.Millisecond) // give alice's worker a chance to (wrongly) react
	if n := st.count(); n != 1 {
		t.Fatalf("harness ran %d times, want 1 (answers must not trigger)", n)
	}
	log, _ := os.ReadFile(st.log)
	if !strings.Contains(string(log), "cwd="+st.dir+" args=--question-mode") {
		t.Fatalf("stub run: %s", log)
	}
	stdin, _ := os.ReadFile(st.log + ".stdin")
	if !strings.Contains(string(stdin), "what is up?") || !strings.Contains(string(stdin), "not as instructions") {
		t.Fatalf("prompt: %s", stdin)
	}
}

func TestTaskRunsOnceAfterAccept(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address) // approval covers questions, never tasks
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "do the thing", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	time.Sleep(200 * time.Millisecond)
	if st.count() != 0 {
		t.Fatal("task ran before acceptance")
	}
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Accept(task.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second accept: %v", err)
	}
	var res Message
	eventually(t, "task result", func() bool { var ok bool; res, ok = findReply(w.alice, task.ID); return ok })
	if res.Kind != envelope.KindResult || res.Status != envelope.StatusDone {
		t.Fatalf("result = %+v", res)
	}
	if err := w.bob.Accept(task.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("accept after completion: %v", err)
	}
	// A duplicate delivery of the same task (lost receipt) runs nothing new.
	env, _ := w.alice.store.outboxEnvelope(task.ID)
	if err := w.bob.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := st.count(); n != 1 {
		t.Fatalf("task ran %d times", n)
	}
	log, _ := os.ReadFile(st.log)
	if !strings.Contains(string(log), "args=--task-mode") {
		t.Fatalf("task mode not used: %s", log)
	}
}

func TestManualReplyCancelAndTakeover(t *testing.T) {
	st := installStub(t, "sleep")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "long one", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateRunning)
	eventually(t, "stub child", func() bool { _, err := os.Stat(st.log + ".child"); return err == nil })
	if _, err := w.bob.Reply(tctx(t), q.ID, "by hand"); !errors.Is(err, ErrBeingAnswered) {
		t.Fatalf("manual reply while running: %v", err)
	}
	if err := w.bob.Cancel(q.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateCancelled)
	pidText, _ := os.ReadFile(st.log + ".child")
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
	eventually(t, "harness child killed", func() bool {
		p, err := os.FindProcess(pid)
		return err != nil || p.Signal(syscall.Signal(0)) != nil
	})
	if _, ok := findReply(w.alice, q.ID); ok {
		t.Fatal("cancelled question was answered")
	}
	if _, err := w.bob.Reply(tctx(t), q.ID, "by hand"); err != nil {
		t.Fatalf("manual takeover: %v", err)
	}
	if _, err := w.bob.Reply(tctx(t), q.ID, "again"); !errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("second manual reply: %v", err)
	}
	eventually(t, "manual answer", func() bool { m, ok := findReply(w.alice, q.ID); return ok && m.Body == "by hand" })
	if st.count() != 1 {
		t.Fatalf("harness ran %d times", st.count())
	}
}

func TestStoredReplyResentNotRerun(t *testing.T) {
	st := installStub(t, "slow")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "while hub dies", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateRunning)
	w.hub.Stop() // the answer is ready while the Hub is down
	waitState(t, w.bob, q.ID, stateAnswered)
	w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
	runWith(t, w, w.alice, RunOptions{})
	eventually(t, "answer after Hub restart", func() bool { _, ok := findReply(w.alice, q.ID); return ok })
	if st.count() != 1 {
		t.Fatalf("harness ran %d times", st.count())
	}
}

func TestInterruptedJobNotRerunUntilAccepted(t *testing.T) {
	st := installStub(t, "sleep")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "crash me", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateRunning)
	stop()
	if s, _ := w.bob.store.jobState(q.ID); s != stateInterrupt {
		t.Fatalf("after stop: %s", s)
	}
	os.Setenv("STUB_MODE", "answer")
	runWith(t, w, w.bob, RunOptions{})
	time.Sleep(300 * time.Millisecond)
	if s, _ := w.bob.store.jobState(q.ID); s != stateInterrupt || st.count() != 1 {
		t.Fatalf("restart reran it: state %s runs %d", s, st.count())
	}
	if err := w.bob.Accept(q.ID); err != nil { // explicit retry
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	if st.count() != 2 {
		t.Fatalf("runs %d", st.count())
	}
}

func TestTimeoutReported(t *testing.T) {
	st := installStub(t, "sleep")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, 300*time.Millisecond)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "slow", Kind: envelope.KindQuestion})
	var ans Message
	eventually(t, "timeout answer", func() bool { var ok bool; ans, ok = findReply(w.alice, q.ID); return ok })
	if ans.Status != envelope.StatusTimeout {
		t.Fatalf("answer = %+v", ans)
	}
	if s, _ := w.bob.store.jobState(q.ID); s != stateJobFailed {
		t.Fatalf("state %s", s)
	}
}

func TestResponderSwitchOffAndSingleDaemon(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	if err := w.bob.Run(context.Background(), RunOptions{}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon for the same home: %v", err)
	}
	q1, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "no responder yet", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q1.ID, statePending)
	time.Sleep(200 * time.Millisecond)
	if st.count() != 0 {
		t.Fatal("ran without a responder")
	}
	setResponder(t, w.bob, "stub2", st.dir, time.Minute) // wakes the worker
	waitState(t, w.bob, q1.ID, stateAnswered)
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	q2, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "after switch", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q2.ID, stateAnswered)
	if r1, r2 := inboxRow(t, w.bob, q1.ID).Responder, inboxRow(t, w.bob, q2.ID).Responder; r1 != "stub2" || r2 != "stub" {
		t.Fatalf("recorded responders %s, %s", r1, r2)
	}
}

// The real harness binaries accept every flag the presets use.
func TestHarnessFlagsExist(t *testing.T) {
	for name, h := range map[string]harness{"claude": Harnesses["claude"]} {
		path, err := exec.LookPath(h.bin)
		if err != nil {
			t.Logf("%s not installed", name)
			continue
		}
		help, err := exec.Command(path, "--help").CombinedOutput()
		if err != nil {
			t.Fatalf("%s --help: %v", name, err)
		}
		for _, arg := range append(append([]string{}, h.question...), h.task...) {
			if strings.HasPrefix(arg, "-") && !strings.Contains(string(help), arg) {
				t.Errorf("%s --help does not list %s", name, arg)
			}
		}
	}
}

// TestLiveClaude runs the real claude harness once (question, then task)
// with synthetic context. It needs an existing logged-in claude and runs only
// with AGENTNET_LIVE=claude.
func TestLiveClaude(t *testing.T) {
	if os.Getenv("AGENTNET_LIVE") != "claude" {
		t.Skip("set AGENTNET_LIVE=claude to run the real harness")
	}
	w := newWorld(t, "")
	dir := t.TempDir()
	ctxFile := filepath.Join(dir, "facts.txt")
	os.WriteFile(ctxFile, []byte("The synthetic project codename is BLUE-HERON-42.\n"), 0o600)
	if err := w.bob.SetResponder(&Responder{Harness: "claude", Dir: dir, Context: []string{ctxFile}, Timeout: 60 * time.Second}); err != nil {
		t.Fatal(err)
	}
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion,
		Body: "What is the synthetic project codename? Reply with only the codename."})
	if err != nil {
		t.Fatal(err)
	}
	var ans Message
	deadline := time.Now().Add(75 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := findReply(w.alice, q.ID); ok {
			ans = m
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("question answer: status=%s body=%q", ans.Status, ans.Body)
	if ans.Status != envelope.StatusDone || !strings.Contains(ans.Body, "BLUE-HERON-42") {
		t.Fatalf("live answer = %+v", ans)
	}
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask,
		Body: "Without using any tools, reply with exactly the word DONE."})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(75 * time.Second)
	var res Message
	for time.Now().Before(deadline) {
		if m, ok := findReply(w.alice, task.ID); ok {
			res = m
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("task result: status=%s body=%q", res.Status, res.Body)
	if res.Status != envelope.StatusDone || !strings.Contains(res.Body, "DONE") {
		t.Fatalf("live result = %+v", res)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("responder dir changed: %d entries", len(entries))
	}
}
