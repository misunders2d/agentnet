package client

import (
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
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
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
		fmt.Fprintln(os.Stderr, strings.Repeat("progress\n", 600)+"run_command required approval that headless mode cannot prompt for, so it was auto-denied.")
	}
	fmt.Println(agyTestResult(id, "SUCCESS", "agy fixture answer"))
	os.Exit(0)
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
	var out []agyTestRun
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r agyTestRun
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
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
	p, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
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
	if reply.Body != "agy fixture answer" || reply.AgentID != record.ID || reply.PID != p.PID {
		t.Fatalf("wrong responder or answer: %+v", reply)
	}
	runs := agyRuns(t, log)
	if len(runs) != 1 || slices.Contains(runs[0].Args, "--conversation") || !strings.Contains(runs[0].Input, "under your native permissions") {
		t.Fatalf("named group used ambient session/policy: %+v", runs)
	}
}
