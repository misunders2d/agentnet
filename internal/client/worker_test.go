package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
	if !strings.Contains(string(stdin), "what is up?") || !strings.Contains(string(stdin), "not as instructions") || !strings.Contains(string(stdin), "send --reply-to <AGENTNET_REQUEST_ID> --progress") || !strings.Contains(string(stdin), "nothing will be sent to the coworker") {
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
		for _, arg := range append(append([]string{h.out}, h.question...), h.task...) {
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
	for _, want := range []string{"admin/alice [question; local state: answered]: first question Q1", "me [answer; local state:", "]: stub answer", "follow-up Q2"} {
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

// Questions keep the recipient's own harness setup (skills, plugins, MCP
// servers, permissions) and only lose editing and new approvals: nothing
// switches skills or tools off wholesale or swaps in an empty configuration.
func TestQuestionPresetsKeepOwnSetup(t *testing.T) {
	blanket := []string{"--tools", "--strict-mcp-config", "--mcp-config", "--no-tools", "--no-skills", "--ignore-user-config", "--disable", "--no-builtin-tools"}
	for name, h := range map[string]harness{"claude": Harnesses["claude"], "codex": Harnesses["codex"], "pi": Harnesses["pi"]} {
		for _, flag := range blanket {
			if slices.Contains(h.question, flag) {
				t.Errorf("%s question mode uses %s", name, flag)
			}
		}
		if h.limits == "" {
			t.Errorf("%s: question mode undescribed", name)
		}
	}
	claude := strings.Join(Harnesses["claude"].question, " ")
	if !strings.Contains(claude, "--permission-mode dontAsk") || !strings.Contains(claude, "--disallowedTools Edit,Write,NotebookEdit") {
		t.Errorf("claude question gates: %s", claude)
	}
	codex := Harnesses["codex"]
	if q := strings.Join(codex.question, " "); !strings.Contains(q, "--sandbox read-only") || !strings.Contains(q, `-c approval_policy="never"`) {
		t.Errorf("codex question gates: %s", q)
	}
	// Resume keeps both gates in the form codex exec resume accepts.
	if r := strings.Join(resumeArgs(codex, "question", codex.question, "T"), " "); !strings.Contains(r, `sandbox_mode="read-only"`) ||
		!strings.Contains(r, `approval_policy="never"`) || strings.Contains(r, "--sandbox") {
		t.Errorf("codex resume: %s", r)
	}
	// Pi: a denylist of the built-ins that change the machine, nothing
	// else (Pi's allowlist would switch every extension tool off); tasks
	// keep the plain preset.
	pi := Harnesses["pi"]
	if q := strings.Join(pi.question, " "); q != "-p --no-session --exclude-tools bash,edit,write,powershell" {
		t.Errorf("pi question: %s", q)
	}
	if tk := strings.Join(pi.task, " "); tk != "-p --no-session" {
		t.Errorf("pi task: %s", tk)
	}
	oldPi := harness{bin: "pi", question: []string{"-p", "--no-session", "--tools", "read,grep,find,ls"}}
	if presetID(oldPi, "question", oldPi.question) == presetID(pi, "question", pi.question) {
		t.Error("the new pi question preset has the same identity as the old one")
	}
	// Sessions started under the earlier no-tools flags are not resumed.
	old := harness{bin: "claude", question: []string{"-p", "--output-format", "text", "--no-session-persistence",
		"--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--permission-mode", "dontAsk"}}
	if presetID(old, "question", old.question) == presetID(Harnesses["claude"], "question", Harnesses["claude"].question) {
		t.Error("the new question preset has the same identity as the old one")
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
