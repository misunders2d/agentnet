package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

type agyTestRun struct {
	Args                   []string
	Input, Dir, Background string
}

// The test executable is the native process on every OS; no vendor/model runs.
func TestAgyProcessHelper(t *testing.T) {
	log := os.Getenv("AGENTNET_TEST_AGY_LOG")
	if log == "" {
		t.Skip("native stand-in only")
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	// One user event per line, as agy reads them: a cold run's input ends
	// after its event, a warm process's stays open for the next turn.
	stdin := bufio.NewReader(os.Stdin)
	in, err := stdin.ReadString('\n')
	if err != nil && (err != io.EOF || in == "") {
		os.Exit(2)
	}
	dir, _ := os.Getwd()
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	json.NewEncoder(f).Encode(agyTestRun{Args: args, Input: string(in), Dir: dir, Background: os.Getenv(BackgroundEnv)})
	f.Close()
	id := newUUID()
	if i := slices.Index(args, "--conversation"); i >= 0 && i+1 < len(args) {
		id = args[i+1]
	}
	mode := os.Getenv("AGENTNET_TEST_AGY_MODE")
	if mode == "auth" {
		fmt.Fprintln(os.Stderr, "authentication required")
		os.Exit(1)
	}
	fmt.Printf("{\"event\":\"init\",\"conversation_id\":%q,\"init\":{}}\n", id)
	if mode == "block" {
		time.Sleep(time.Hour)
	}
	if mode == "partial" {
		fmt.Println(`{"event":"step_update","step_update":{"text_delta":"not a final answer"}}`)
		os.Exit(0)
	}
	if mode == "deny" {
		// As agy 1.5.18: the notice on stderr, then a SUCCESS result that
		// lists the denied action.
		fmt.Fprintln(os.Stderr, strings.Repeat("progress\n", 600)+"run_command required approval that headless mode cannot prompt for, so it was auto-denied.")
		fmt.Println(agyTestDeniedResult(id))
	} else {
		fmt.Println(agyTestResult(id, "SUCCESS", "agy fixture answer"))
	}
	if mode == "stray" {
		time.Sleep(100 * time.Millisecond) // the process is idle by now
		fmt.Println(agyTestResult(id, "SUCCESS", "an event between turns"))
	}
	// Later turns of a warm process: each event gets its own result on the
	// same conversation; the turn's input is logged beside the runs.
	for {
		line, err := stdin.ReadString('\n')
		if line != "" {
			if f, e := os.OpenFile(log+".turns", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); e == nil {
				f.WriteString(line)
				f.Close()
			}
			fmt.Println(agyTestResult(id, "SUCCESS", "agy fixture answer"))
		}
		if err != nil {
			os.Exit(0)
		}
	}
}

func agyFixture(t *testing.T, mode string) (string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log, dir := filepath.Join(t.TempDir(), "runs"), t.TempDir()
	t.Setenv("AGENTNET_TEST_AGY_LOG", log)
	t.Setenv("AGENTNET_TEST_AGY_MODE", mode)
	old := Harnesses["agy"]
	h := old
	h.bin = exe
	h.question = append([]string{"-test.run=^TestAgyProcessHelper$", "--"}, h.question...)
	h.task = append([]string{"-test.run=^TestAgyProcessHelper$", "--"}, h.task...)
	Harnesses["agy"] = h
	t.Cleanup(func() { Harnesses["agy"] = old })
	return log, dir
}

func agyRuns(t *testing.T, log string) []agyTestRun {
	t.Helper()
	raw, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out, err := parseAgyRuns(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func parseAgyRuns(raw string) ([]agyTestRun, error) {
	var out []agyTestRun
	// Encode appends a newline after each record. A concurrent read may see
	// the newly created empty file or a partial trailing write; neither is a
	// completed run yet. Complete malformed records must still fail.
	lines := strings.Split(raw, "\n")
	for _, line := range lines[:len(lines)-1] {
		var r agyTestRun
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func TestAgyWorkerSessionsAndTaskApproval(t *testing.T) {
	log, dir := agyFixture(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "agy", dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	if ref == nil || ref.Harness != "agy" || len(ref.ID) != 36 {
		t.Fatalf("session=%+v", ref)
	}
	first := agyRuns(t, log)
	if len(first) != 1 || slices.Contains(first[0].Args, "--conversation") || first[0].Background != "1" || !json.Valid([]byte(first[0].Input)) {
		t.Fatalf("first=%+v", first)
	}
	stop()
	runWith(t, w, w.bob, RunOptions{})
	q2, _ := ask(t, w, a1)
	second := agyRuns(t, log)
	if len(second) != 2 || !slices.Equal(second[1].Args[len(second[1].Args)-2:], []string{"--conversation", ref.ID}) || sessionOf(t, w.bob, q2).ID != ref.ID {
		t.Fatalf("resume=%+v", second)
	}
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "synthetic task", ReplyTo: a1})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if len(agyRuns(t, log)) != 2 {
		t.Fatal("unaccepted task ran")
	}
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAnswered)
	third := agyRuns(t, log)
	if len(third) != 3 || slices.Contains(third[2].Args, "--conversation") || sessionOf(t, w.bob, task.ID).ID == ref.ID {
		t.Fatalf("task crossed question session: %+v", third)
	}
	// Lookup guidance is the exact local command under native policy, no flags.
	lookup := w.bob.questionSetup(job{Kind: envelope.KindQuestion}, "agy")
	if len(lookup.args) != 0 || !strings.Contains(lookup.text, "under your native permissions") {
		t.Fatalf("lookup=%+v", lookup)
	}
}

// MEL-828: agy is kept warm like Claude/Codex/Pi/OMP. The same person's next
// message in the same conversation goes into the running agy process of its
// saved conversation: no new process, the same conversation id.
func TestAgyWorkerKeptWarm(t *testing.T) {
	log, dir := agyFixture(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "agy", dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	pool := warmPoolOf(t, w.bob)
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	if ref == nil || len(ref.ID) != 36 {
		t.Fatalf("session=%+v", ref)
	}
	eventually(t, "agy kept warm", func() bool { return pool.idleCount() == 1 })
	q2, _ := ask(t, w, a1)
	if runs := agyRuns(t, log); len(runs) != 1 || pool.spawns.Load() != 1 {
		t.Fatalf("the second message started another agy process: %+v (spawns %d)", runs, pool.spawns.Load())
	}
	if r2 := sessionOf(t, w.bob, q2); r2 == nil || r2.ID != ref.ID {
		t.Fatalf("second turn's session %+v, want %s", r2, ref.ID)
	}
	turns, _ := os.ReadFile(log + ".turns")
	if strings.Count(string(turns), "\n") != 1 || !json.Valid([]byte(strings.TrimSpace(string(turns)))) {
		t.Fatalf("the running agy process did not get the second turn: %q", turns)
	}
	eventually(t, "agy kept warm again", func() bool { return pool.idleCount() == 1 })
}

// A warm agy process that writes an event between turns never gets the next
// message: the next turn starts its own process on the same conversation,
// and the stray event is nobody's answer (MEL-828 review).
func TestAgyWorkerStrayEventNotReused(t *testing.T) {
	log, dir := agyFixture(t, "stray")
	w := newWorld(t, "")
	setResponder(t, w.bob, "agy", dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	pool := warmPoolOf(t, w.bob)
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	eventually(t, "agy kept warm", func() bool { return pool.idleCount() == 1 })
	eventually(t, "stray event read", pool.idleStray)
	q2, _ := ask(t, w, a1)
	if m, ok := findReply(w.alice, q2); !ok || strings.Contains(m.Body, "between turns") {
		t.Fatalf("a stray event became the answer: %q", m.Body)
	}
	if turns, _ := os.ReadFile(log + ".turns"); len(turns) != 0 {
		t.Fatalf("the process that wrote between turns got the next message: %q", turns)
	}
	runs := agyRuns(t, log)
	if len(runs) != 2 || pool.spawns.Load() != 2 || !slices.Contains(runs[1].Args, ref.ID) {
		t.Fatalf("runs %+v (spawns %d), want a second process on %s", runs, pool.spawns.Load(), ref.ID)
	}
}

func TestAgyWorkerRefusesFalseSuccess(t *testing.T) {
	for _, tc := range []struct{ mode, state string }{{"deny", stateNeedHuman}, {"auth", stateNeedHuman}, {"partial", stateJobFailed}} {
		t.Run(tc.mode, func(t *testing.T) {
			log, dir := agyFixture(t, tc.mode)
			w := newWorld(t, "")
			setResponder(t, w.bob, "agy", dir, time.Minute)
			w.bob.Approve(w.alice.Address)
			runWith(t, w, w.bob, RunOptions{})
			runWith(t, w, w.alice, RunOptions{})
			q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "synthetic request"})
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, w.bob, q.ID, tc.state)
			if tc.state == stateNeedHuman {
				if _, ok := findReply(w.alice, q.ID); ok {
					t.Fatal("native blocker became an answer")
				}
			} else {
				eventually(t, "failed answer", func() bool {
					m, ok := findReply(w.alice, q.ID)
					return ok && m.Status == envelope.StatusFailed && !strings.Contains(m.Body, "not a final answer")
				})
			}
			if len(agyRuns(t, log)) != 1 {
				t.Fatal("request retried")
			}
		})
	}
}

func TestAgyWorkerCancellation(t *testing.T) {
	t.Run("log records", func(t *testing.T) {
		for _, tc := range []struct {
			name, raw string
			count     int
			invalid   bool
		}{
			{name: "empty"},
			{name: "partial", raw: `{"Args":`},
			{name: "complete", raw: "{\"Input\":\"first\"}\n", count: 1},
			{name: "complete and partial", raw: "{\"Input\":\"first\"}\n{\"Args\":", count: 1},
			{name: "two complete", raw: "{}\n{}\n", count: 2},
			{name: "malformed complete", raw: "{\"Args\":\n", invalid: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				runs, err := parseAgyRuns(tc.raw)
				if (err != nil) != tc.invalid || len(runs) != tc.count {
					t.Fatalf("runs=%d error=%v", len(runs), err)
				}
			})
		}
	})
	log, dir := agyFixture(t, "block")
	w := newWorld(t, "")
	setResponder(t, w.bob, "agy", dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "synthetic cancellation"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "native process started", func() bool { return len(agyRuns(t, log)) == 1 })
	if err := w.bob.Cancel(q.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateCancelled)
	if len(agyRuns(t, log)) != 1 {
		t.Fatal("cancelled request restarted")
	}
}

func TestAgyNamedGroupResponder(t *testing.T) {
	log, dir := agyFixture(t, "answer")
	w, producer, packet, _ := groupTurnsFixture(t)
	host := proofReader(t, w, "agy-visitor")
	runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	fakeNotify(host)
	record, err := host.CreateLocalAgent("Antigravity reviewer", Responder{Harness: "agy", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	ctx, started := tctx(t), time.Now()
	deadline, _ := ctx.Deadline()
	p, err := w.alice.InviteNamedAgent(ctx, packet.State.Conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatalf("named group invitation: elapsed=%v remaining=%v context=%v: %v", time.Since(started), time.Until(deadline), ctx.Err(), err)
	}
	eventually(t, "Antigravity invitation", func() bool { v, e := host.Participation(p.PID); return e == nil && v.State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Antigravity membership", func() bool { v, e := producer.Participation(p.PID); return e == nil && v.Claimable() })
	if err = host.Approve(producer.Address); err != nil {
		t.Fatal(err)
	}
	q, err := producer.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "synthetic named group question")
	if err != nil {
		t.Fatal(err)
	}
	reply := replyAt(t, producer, packet.State.Conv, q.ID)
	if reply.Body != taggedAnswer(t, producer, "agy fixture answer") || reply.AgentID != record.ID || reply.PID != p.PID {
		t.Fatalf("wrong responder or answer: %+v", reply)
	}
	runs := agyRuns(t, log)
	if len(runs) != 1 || slices.Contains(runs[0].Args, "--conversation") || !strings.Contains(runs[0].Input, "under your native permissions") {
		t.Fatalf("named group used ambient session/policy: %+v", runs)
	}
}

// idleStray reports whether an idle worker has written output between turns.
func (p *warmPool) idleStray() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range p.idle {
		if w.out.stray.Load() {
			return true
		}
	}
	return false
}
