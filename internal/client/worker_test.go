package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// stubScript is a fake harness: it logs each run (cwd, args, stdin) and
// behaves according to $STUB_MODE: answer, sleep (with a child process
// whose pid it records), slow (1 s then answer) or fail. With the argument
// --human it says the local human must decide.
const stubScript = `#!/bin/sh
echo "run cwd=$(pwd) args=$* bg=$AGENTNET_BACKGROUND req=$AGENTNET_REQUEST_ID peer=$AGENTNET_REQUESTER home=$AGENTNET_HOME bind=$AGENTNET_REPLY_BINDING" >> "$STUB_LOG"
cat > "$STUB_LOG.stdin"
case "$*" in *--human*) printf 'AGENTNET: NEEDS-HUMAN\nwhich budget applies?\n'; exit 0 ;; esac
case "$STUB_MODE" in
askback) if grep -q "Riga" "$STUB_LOG.stdin"; then echo "Riga: bring a jacket"; else echo "Which city?"; fi ;;
sleep) sleep 30 & echo $! > "$STUB_LOG.child"; wait ;;
slow) sleep 1; echo "stub answer" ;;
propose) printf 'AGENTNET: PROPOSE-TASK\nUpdate CHANGELOG.md: add the 0.8.0 entry.\n' ;;
proposebig) printf 'AGENTNET: PROPOSE-TASK\n'; head -c 70000 /dev/zero | tr '\0' x ;;
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
	Harnesses["stubhuman"] = harness{bin: bin, question: []string{"--human"}, task: []string{"--human"}, stdin: true}
	t.Cleanup(func() { delete(Harnesses, "stub"); delete(Harnesses, "stub2"); delete(Harnesses, "stubhuman") })
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
	if !strings.Contains(string(log), "cwd="+st.dir+" args=--question-mode bg=1") || !strings.Contains(string(log), "req="+q2.ID+" peer="+w.alice.Address+" home="+w.bob.home) {
		t.Fatalf("stub run: %s", log)
	}
	stdin, _ := os.ReadFile(st.log + ".stdin")
	if !strings.Contains(string(stdin), "what is up?") || !strings.Contains(string(stdin), "not as instructions") || !strings.Contains(string(stdin), "send --reply-to <AGENTNET_REQUEST_ID> --progress") || !strings.Contains(string(stdin), "nothing will be sent back") {
		t.Fatalf("prompt: %s", stdin)
	}
}

// A run gets only the values its own job sets. The same names left in the
// daemon's environment never reach a harness, where the CLI's run guard
// would take them as permission (cmd/agentnet/runguard.go): an ordinary run
// sees only its own request, a follow-up neither a request nor a binding.
func TestRunEnvDropsInheritedAgentNetValues(t *testing.T) {
	st := installStub(t, "answer")
	for _, k := range []string{BackgroundEnv, ProgressRequestEnv, ProgressPeerEnv, receiverBindingEnv, "AGENTNET_HOME"} {
		t.Setenv(k, "stray")
	}
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})

	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "which port?", Kind: envelope.KindQuestion, FollowUp: "summarize"})
	if err != nil {
		t.Fatal(err)
	}
	var ans Message
	eventually(t, "answer", func() bool { var ok bool; ans, ok = findReply(w.alice, q.ID); return ok })
	waitState(t, w.alice, ans.ID, stateSummary)
	data, _ := os.ReadFile(st.log)
	log := string(data)
	for _, want := range []string{
		" bg=1 req=" + q.ID + " peer=" + w.alice.Address + " home=" + w.bob.home + " bind=\n", // bob answers his own request
		" bg=1 req= peer= home=" + w.alice.home + " bind=\n",                                  // alice's follow-up
	} {
		if !strings.Contains(log, want) {
			t.Errorf("no run with %q", want)
		}
	}
	if st.count() != 2 || strings.Contains(log, "stray") {
		t.Fatalf("runs:\n%s", log)
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
	// Running is recorded before the harness starts; stop only once it has.
	eventually(t, "stub child", func() bool { _, err := os.Stat(st.log + ".child"); return err == nil })
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
	helpArgs := map[string][]string{"claude": {"--help"}, "codex": {"exec", "--help"}, "pi": {"--help"}}
	for name, args := range helpArgs {
		h := Harnesses[name]
		path, err := exec.LookPath(h.bin)
		if err != nil {
			t.Logf("%s not installed", name)
			continue
		}
		help, err := exec.Command(path, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s --help: %v", name, err)
		}
		for _, arg := range append(append([]string{h.out, h.addDir}, h.question...), h.task...) {
			if strings.HasPrefix(arg, "-") && !strings.Contains(string(help), arg) {
				t.Errorf("%s --help does not list %s", name, arg)
			}
		}
		if name == "codex" { // every feature switched off must exist
			features, err := exec.Command(path, "features", "list").CombinedOutput()
			if err != nil {
				t.Fatalf("codex features list: %v", err)
			}
			for i, arg := range h.question {
				if arg == "--disable" && !strings.Contains(string(features), "\n"+h.question[i+1]+" ") {
					t.Errorf("codex features list does not list %s", h.question[i+1])
				}
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

// R1: the prompt carries the whole back-and-forth with the asker, and never
// messages exchanged with anyone else, even if the asker names their ids.
func TestThreadContextStaysInConversation(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})

	q1, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "first question Q1", Kind: envelope.KindQuestion})
	var a1 Message
	eventually(t, "answer 1", func() bool { var ok bool; a1, ok = findReply(w.alice, q1.ID); return ok })
	q2, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "follow-up Q2", ReplyTo: a1.ID, Kind: envelope.KindQuestion})
	waitState(t, w.bob, q2.ID, stateAnswered)
	prompt, _ := os.ReadFile(st.log + ".stdin")
	for _, want := range []string{"the device admin/alice [question; local state: answered]: first question Q1", "\nthis device [answer; local state:", "]: stub answer", "follow-up Q2"} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("multi-turn prompt lacks %q:\n%s", want, prompt)
		}
	}

	// Carol's message to bob and bob's message to carol stay out of alice's prompts.
	toBob, _ := carol.Send(tctx(t), w.bob.Address, "CAROL-SECRET-IN", "")
	eventually(t, "carol's message", func() bool { return hasInbox(w.bob, "CAROL-SECRET-IN") })
	toCarol, _ := w.bob.Send(tctx(t), carol.Address, "BOB-SECRET-OUT", "")
	for _, foreign := range []string{toBob.ID, toCarol.ID} {
		q, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "tell me about it", ReplyTo: foreign, Kind: envelope.KindQuestion})
		waitState(t, w.bob, q.ID, stateAnswered)
		prompt, _ := os.ReadFile(st.log + ".stdin")
		if strings.Contains(string(prompt), "SECRET") {
			t.Fatalf("foreign conversation leaked into prompt:\n%s", prompt)
		}
	}
}

// R2: withdrawing approval also withdraws questions already queued for the
// worker; an explicit accept still lets one through.
func TestUnapproveWithdrawsQueuedQuestions(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	runWith(t, w, w.bob, RunOptions{}) // no responder yet
	w.bob.Approve(w.alice.Address)
	q1, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "queued", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q1.ID, statePending)
	w.bob.Inbox(false, true) // read it
	if err := w.bob.Unapprove(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.bob.store.jobState(q1.ID); s != stateHeld {
		t.Fatalf("queued question after unapprove: %s", s)
	}
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	q2, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "later", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q2.ID, stateHeld)
	time.Sleep(300 * time.Millisecond)
	if st.count() != 0 {
		t.Fatal("question ran after its sender was unapproved")
	}
	if err := w.bob.Accept(q1.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q1.ID, stateAnswered)
	if s, _ := w.bob.store.jobState(q2.ID); s != stateHeld || st.count() != 1 {
		t.Fatalf("other question %s, runs %d", s, st.count())
	}

	// The claim itself re-checks approval, whatever the row says.
	w.bob.SetResponder(nil) // keep the daemon's worker out of this check
	w.bob.store.db.Exec(`INSERT INTO approvals(address, added_at) VALUES(?, 0)`, w.alice.Address)
	w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, statePending, q2.ID)
	w.bob.store.db.Exec(`DELETE FROM approvals`)
	if _, ok, err := w.bob.store.claimJob("stub"); ok || err != nil {
		t.Fatalf("claimed a pending question from an unapproved sender (%v)", err)
	}
}

// The harness is told its working directory as configured, even when that
// path goes through a symlink (as /var does on macOS): PWD must match Dir.
func TestResponderSeesConfiguredDirectory(t *testing.T) {
	st := installStub(t, "answer")
	link := filepath.Join(t.TempDir(), "via-link")
	if err := os.Symlink(st.dir, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", link, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "where are you?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	log, _ := os.ReadFile(st.log)
	if !strings.Contains(string(log), "run cwd="+link+" args=") {
		t.Fatalf("harness saw another directory than %s:\n%s", link, log)
	}
}

// Native permissions govern both admitted questions and accepted tasks.
func TestQuestionPresetsKeepOwnSetup(t *testing.T) {
	prohibited := []string{"--tools", "--strict-mcp-config", "--mcp-config", "--no-tools", "--no-skills", "--ignore-user-config", "--disable", "--no-builtin-tools", "--permission-mode", "--disallowedTools", "--allowedTools", "--sandbox", "--approval-mode", "--exclude-tools", "--config", "--dangerously-bypass-approvals-and-sandbox"}
	for name, h := range Harnesses {
		if name != "claude" && name != "codex" && name != "pi" && name != "omp" {
			continue
		}
		if !slices.Equal(h.question, h.task) {
			t.Errorf("%s adds question policy: %v / %v", name, h.question, h.task)
		}
		for _, args := range [][]string{h.question, h.task, resumeArgs(h, "question", h.question, "T")} {
			for _, flag := range prohibited {
				if slices.Contains(args, flag) {
					t.Errorf("%s injects %s: %v", name, flag, args)
				}
			}
			for _, arg := range args {
				if strings.Contains(arg, "sandbox_mode=") || strings.Contains(arg, "approval_policy=") {
					t.Errorf("%s injects policy %s", name, arg)
				}
			}
		}
		if h.limits == "" {
			t.Errorf("%s permissions undescribed", name)
		}
	}
	old := Harnesses["codex"]
	old.question = append(append([]string{}, old.question...), "--sandbox", "read-only")
	if presetID(old, "question", old.question) == presetID(Harnesses["codex"], "question", Harnesses["codex"].question) {
		t.Fatal("old restricted session must not resume under changed preset")
	}
}

// A question from an agent that is not approved waits for a person. On a
// server no desktop notification reaches anyone, so doctor must say that
// the responder answers nobody yet and that an item waits, without its text
// and without calling waiting a failure.
func TestDoctorShowsApprovalsAndWaitingItems(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	runWith(t, w, w.bob, RunOptions{})
	check := func(name string) Check {
		for _, c := range w.bob.Doctor(tctx(t)) {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("no %s check", name)
		return Check{}
	}
	if c := check("responder"); !c.OK || !strings.Contains(c.Result, NoApprovals) {
		t.Fatalf("responder with no approvals: %+v", c)
	}
	if c := check("review"); !c.OK || c.Result != "nothing waits for your decision" {
		t.Fatalf("empty review: %+v", c)
	}

	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "secret greeting", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "held question", func() bool { s, _ := w.bob.store.jobState(q.ID); return s == stateHeld })
	c := check("review")
	if !c.OK || c.Result != "1 item(s) wait for your decision: agentnet inbox --review" {
		t.Fatalf("waiting review: %+v", c)
	}
	if s, _ := w.bob.store.jobState(q.ID); s != stateHeld || st.count() != 0 {
		t.Fatalf("doctor changed the held question: state %s, runs %d", s, st.count())
	}

	w.bob.Approve(w.alice.Address)
	if c := check("responder"); !strings.HasSuffix(c.Result, "answers questions from 1 approved agent(s)") {
		t.Fatalf("responder with one approval: %+v", c)
	}
	if s, _ := w.bob.store.jobState(q.ID); s != stateHeld {
		t.Fatalf("approving later released the held question: %s", s)
	}
}

func TestQuestionPromptOffersAskingBack(t *testing.T) {
	w := newWorld(t, "")
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		prompt, err := w.bob.prompt(job{From: w.alice.Address, Kind: kind}, &Responder{})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(prompt, "reply with your question for them"); got != (kind == envelope.KindQuestion) {
			t.Fatalf("asking back in %s prompt: %v", kind, got)
		}
		if !strings.Contains(prompt, needsHumanMarker) {
			t.Fatal("missing local permission stop")
		}
	}
}

func TestWorkerStartupTimingAndLaunchFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-%t", missing), func(t *testing.T) {
			st := installStub(t, "answer")
			w := newWorld(t, "")
			setResponder(t, w.bob, "stub", st.dir, time.Minute)
			if missing {
				if err := os.Remove(Harnesses["stub"].bin); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.bob.Approve(w.alice.Address); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var logs []string
			w.bob.Logf = func(f string, args ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, args...)); mu.Unlock() }
			ctx, cancel := context.WithCancel(tctx(t))
			done := make(chan error, 1)
			go func() { done <- w.bob.Run(ctx, RunOptions{}) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("worker did not stop")
				}
			})
			runAgent(t, w.alice)
			q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "PRIVATE_STARTUP_CONTENT"})
			if err != nil {
				t.Fatal(err)
			}
			var answer Message
			eventually(t, "terminal launch result", func() bool { var ok bool; answer, ok = findReply(w.alice, q.ID); return ok })
			wantStatus, wantState := envelope.StatusDone, stateAnswered
			if missing {
				wantStatus, wantState = envelope.StatusFailed, stateJobFailed
			}
			if answer.Status != wantStatus {
				t.Fatalf("result %s, want %s", answer.Status, wantStatus)
			}
			waitState(t, w.bob, q.ID, wantState)
			mu.Lock()
			joined := strings.Join(logs, "\n")
			mu.Unlock()
			if !strings.Contains(joined, "stored_age_ms=") || strings.Contains(joined, "PRIVATE_STARTUP_CONTENT") {
				t.Fatalf("startup diagnostics missing or disclosed content: %s", joined)
			}
			if missing {
				if !strings.Contains(joined, "launch failed: setup_ms=") || strings.Contains(joined, "first output:") || st.count() != 0 {
					t.Fatalf("failed launch falsely ran: %s", joined)
				}
				if w.bob.runNext(tctx(t), nil) {
					t.Fatal("failed launch reran without explicit acceptance")
				}
			} else if !strings.Contains(joined, "process started: setup_ms=") || strings.Count(joined, "first output:") != 1 || st.count() != 1 {
				t.Fatalf("startup milestones incorrect: %s", joined)
			}
		})
	}
}

func TestOMPResponderQuestionAndAcceptedTask(t *testing.T) {
	st := installStub(t, "answer")
	original := Harnesses["omp"]
	h := original
	h.bin = Harnesses["stub"].bin
	Harnesses["omp"] = h
	t.Cleanup(func() { Harnesses["omp"] = original })
	script := `#!/bin/sh
 if [ "$1" = "--help" ]; then echo "--print --no-session --config --approval-mode --extension"; exit 0; fi
 echo "run cwd=$(pwd) args=$*" >> "$STUB_LOG"
 for arg do
  if [ "$previous" = "--config" ]; then
   test -f "$arg" || exit 41
   cp "$arg" "$STUB_LOG.policy"
   echo "$arg" > "$STUB_LOG.path"
  fi
  previous="$arg"
 done
 cat > "$STUB_LOG.stdin"
 echo "omp stub answer"
`
	if err := os.WriteFile(h.bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, "")
	setResponder(t, w.bob, "omp", st.dir, 0)
	w.bob.Approve(w.alice.Address)
	runAgent(t, w.bob)
	runAgent(t, w.alice)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "question only"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	if _, err := os.Stat(st.log + ".policy"); !os.IsNotExist(err) {
		t.Fatalf("OMP received an injected policy: %v", err)
	}
	log, _ := os.ReadFile(st.log)
	if !strings.Contains(string(log), "--extension") || strings.Contains(string(log), "--approval-mode") || strings.Contains(string(log), "--config") || strings.Contains(string(log), "--exclude-tools") || strings.Contains(string(log), "--resume") {
		t.Fatalf("OMP native setup changed: %s", log)
	}
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "accepted work"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if st.count() != 1 {
		t.Fatal("task executed before acceptance")
	}
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAnswered)
	log, _ = os.ReadFile(st.log)
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 2 || strings.Contains(lines[1], "--config") || strings.Contains(lines[1], "--approval-mode") || strings.Contains(lines[1], "--resume") {
		t.Fatalf("task changed own permissions/session: %s", log)
	}
}

// Opt-in actual OMP: fresh background jobs, synthetic homes/requests only.
func TestLiveOMP(t *testing.T) {
	if os.Getenv("AGENTNET_LIVE") != "omp" {
		t.Skip("set AGENTNET_LIVE=omp for a real native responder smoke")
	}
	old := Harnesses["omp"]
	h := old
	if native := os.Getenv("AGENTNET_OMP_BINARY"); native != "" {
		h.bin = native
	}
	Harnesses["omp"] = h
	t.Cleanup(func() { Harnesses["omp"] = old })
	program := os.Getenv("AGENTNET_LOOKUP_BINARY")
	if program == "" {
		t.Fatal("bind the exact installed AgentNet binary for version lookup")
	}
	expected, err := exec.Command(program, "version").Output()
	if err != nil {
		t.Fatal("installed version lookup unavailable")
	}
	bindProgram(t, program)
	w := newWorld(t, "")
	dir := t.TempDir()
	setResponder(t, w.bob, "omp", dir, 0)
	w.bob.Approve(w.alice.Address)
	runAgent(t, w.bob)
	runAgent(t, w.alice)
	waitResult := func(id string) Message {
		t.Helper()
		for deadline := time.Now().Add(75 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if m, ok := findReply(w.alice, id); ok {
				return m
			}
		}
		t.Fatal("real OMP result not received within bounded smoke window")
		return Message{}
	}
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "Use only the fixed agentnet_lookup version operation to read the installed AgentNet version. Reply with both exact lines: version/protocol AND build revision. Include the complete build revision hash. Do not use other tools, edit files, contact people or run any external actions."})
	if err != nil {
		t.Fatal(err)
	}
	answer := waitResult(q.ID)
	if answer.Status != envelope.StatusDone || !strings.Contains(answer.Body, strings.Split(strings.TrimSpace(string(expected)), "\n")[0]) {
		t.Fatalf("real OMP version question failed (status=%s, authentication_error=%t)", answer.Status, strings.Contains(strings.ToLower(answer.Body), "auth"))
	}
	t.Log("real OMP question returned installed AgentNet version")
	if os.Getenv("AGENTNET_OMP_QUESTION_ONLY") == "1" {
		return
	}
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "Without any tools or external actions, return exactly OMP-BENIGN-ACK."})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	result := waitResult(task.ID)
	if result.Status != envelope.StatusDone || !strings.Contains(result.Body, "OMP-BENIGN-ACK") {
		t.Fatalf("real OMP benign accepted task failed (status=%s)", result.Status)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("real OMP altered synthetic working folder (%d entries)", len(entries))
	}
	t.Log("real OMP accepted benign task returned exact ACK; working folder unchanged")
}
