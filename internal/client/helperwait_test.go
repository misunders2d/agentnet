package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// gatedHarness registers a test harness that logs each prompt to dir/name.log
// and answers once dir/<gate> exists (created by the test), with the shell
// snippet answer.
func gatedHarness(t *testing.T, dir, name, gate, answer string) {
	t.Helper()
	bin := filepath.Join(dir, name+".sh")
	script := "#!/bin/sh\nd=" + dir + "\ncat > $d/" + name + ".last\n{ echo '=== run'; cat $d/" + name + ".last; } >> $d/" + name + ".log\n" +
		"while [ ! -e $d/" + gate + " ]; do sleep 0.05; done\n" + answer + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	Harnesses[name] = harness{bin: bin, question: []string{"--question-mode"}, task: []string{"--task-mode"}, stdin: true}
	t.Cleanup(func() { delete(Harnesses, name) })
}

func gateLog(t *testing.T, dir, name string) (int, string) {
	data, _ := os.ReadFile(filepath.Join(dir, name+".log"))
	last, _ := os.ReadFile(filepath.Join(dir, name+".last"))
	return strings.Count(string(data), "=== run"), string(last)
}

// MEL-540 (owner, October 29, 2026): a coordinating agent's run hands tasks
// to other agents and ends; its request waits for them and runs again with
// their results once all have finished. A helper that needs it asks it and
// is answered at once. A stopped request is not woken by late results.
func TestParkedRequestRunsAgainWithHelperResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	stub := installAgentStub(t)
	Harnesses["roomasker"] = Harnesses["agentstub"]
	t.Cleanup(func() { delete(Harnesses, "roomasker") })
	dir := t.TempDir()
	gatedHarness(t, dir, "bossstub", "bossgo",
		`if grep -q "Results of the agents you asked" $d/bossstub.last; then printf 'final report\nemotion: calm\n'; else printf 'asked the helpers\nemotion: calm\n'; fi`)
	gatedHarness(t, dir, "help1stub", "h1go", `printf 'part one done\nemotion: calm\n'`)
	gatedHarness(t, dir, "help2stub", "h2go", `printf 'part two done\nemotion: calm\n'`)
	gate := func(name string, open bool) {
		p := filepath.Join(dir, name)
		if open {
			os.WriteFile(p, nil, 0o600)
		} else {
			os.Remove(p)
		}
	}
	w := newWorld(t, "")
	persons(t, w.alice)
	a := w.alice
	runAgent(t, a)
	phone := linked(t, a)
	publishGroupFixtureCaps(t, a, true)
	publishGroupFixtureCaps(t, phone, true)
	asker, err := a.CreateLocalAgent("asker", Responder{Harness: "roomasker", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	boss, err := a.CreateLocalAgent("boss", Responder{Harness: "bossstub", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	h1, err := phone.CreateLocalAgent("one", Responder{Harness: "help1stub", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := phone.CreateLocalAgent("two", Responder{Harness: "help2stub", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*Agent{a, phone} {
		if err = d.PublishAgentCatalog(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	from, err := a.SessionAgent("", asker.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobAt := func(d *Agent, room, lid string) string {
		var id string
		eventually(t, "request stored at its host", func() bool {
			return d.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, room, lid).Scan(&id) == nil
		})
		return id
	}
	stateOf := func(id string) string {
		s, _ := a.store.jobState(id)
		return s
	}

	// The person's request to the coordinating agent; its run asks two helpers.
	req, err := a.SendAgentRoomAsk(tctx(t), AgentRoomAsk{From: from, SessionKey: "agentstub/report", Host: a.Address, AgentID: boss.ID, Kind: envelope.KindTask, Body: "make the monthly fee report"})
	if err != nil {
		t.Fatal(err)
	}
	cause := jobAt(a, req.Conv, req.LID)
	waitState(t, a, cause, stateRunning)
	var asks []AgentRoomSent
	for _, h := range []protocol.AgentRecord{h1, h2} {
		s, err := a.RunAgentRoomAsk(tctx(t), cause, AgentRoomAsk{Host: phone.Address, AgentID: h.ID, Kind: envelope.KindTask, Body: "your part of the report"})
		if err != nil {
			t.Fatal(err)
		}
		if err = a.WaitForHelper(cause, s.ID); err != nil {
			t.Fatal(err)
		}
		asks = append(asks, s)
	}
	gate("bossgo", true)
	waitState(t, a, cause, stateHelpersWaiting)
	if r, err := a.AwaitReply(tctx(t), req.ID, 200*time.Millisecond, nil); err != nil || r.Answer != nil {
		t.Fatalf("a waiting request answered before its helpers finished: %+v %v", r.Answer, err)
	}

	// A helper that needs the coordinator asks it: that runs now, as its own job.
	h2job := jobAt(phone, req.Conv, asks[1].LID)
	waitState(t, phone, h2job, stateRunning)
	back, err := phone.RunAgentRoomAsk(tctx(t), h2job, AgentRoomAsk{Host: a.Address, AgentID: boss.ID, Kind: envelope.KindQuestion, Body: "which month?"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, a, jobAt(a, req.Conv, back.LID), stateAnswered)
	if s := stateOf(cause); s != stateHelpersWaiting {
		t.Fatalf("a helper's question woke the waiting request: %s", s)
	}

	// One helper's result alone wakes nothing.
	gate("h1go", true)
	eventually(t, "first helper's result here", func() bool { _, done, _ := a.helperOutcome(asks[0].ID); return done })
	a.wakeWorker()
	time.Sleep(300 * time.Millisecond)
	if n, _ := gateLog(t, dir, "bossstub"); n != 2 || stateOf(cause) != stateHelpersWaiting {
		t.Fatalf("woke before every helper finished: %d run(s), %s", n, stateOf(cause))
	}

	// The last result runs the request again, with both results and its note.
	gate("h2go", true)
	waitState(t, a, cause, stateAnswered)
	n, prompt := gateLog(t, dir, "bossstub")
	if n != 3 {
		t.Fatalf("coordinator runs: %d, want ask, the helper's question, and one continuation", n)
	}
	for _, want := range []string{"Results of the agents you asked", "part one done", "part two done", "asked the helpers"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("continuation prompt lacks %q:\n%s", want, prompt)
		}
	}
	if r, err := a.AwaitReply(tctx(t), req.ID, 15*time.Second, nil); err != nil || r.Answer == nil || !strings.Contains(r.Answer.Body, "final report") {
		t.Fatalf("the requester did not get the final answer: %+v %v", r, err)
	}

	// Stopped while waiting: a late result wakes nothing.
	gate("bossgo", false)
	gate("h1go", false)
	req2, err := a.SendAgentRoomAsk(tctx(t), AgentRoomAsk{From: from, SessionKey: "agentstub/report", Host: a.Address, AgentID: boss.ID, Kind: envelope.KindTask, Body: "next month too"})
	if err != nil {
		t.Fatal(err)
	}
	cause2 := jobAt(a, req2.Conv, req2.LID)
	waitState(t, a, cause2, stateRunning)
	late, err := a.RunAgentRoomAsk(tctx(t), cause2, AgentRoomAsk{Host: phone.Address, AgentID: h1.ID, Kind: envelope.KindTask, Body: "your part again"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.WaitForHelper(cause2, late.ID); err != nil {
		t.Fatal(err)
	}
	gate("bossgo", true)
	waitState(t, a, cause2, stateHelpersWaiting)
	if err = a.Cancel(cause2); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, cause2, stateCancelled)
	runs, _ := gateLog(t, dir, "bossstub")
	gate("h1go", true)
	eventually(t, "late result here", func() bool { _, done, _ := a.helperOutcome(late.ID); return done })
	a.wakeWorker()
	time.Sleep(300 * time.Millisecond)
	if again, _ := gateLog(t, dir, "bossstub"); again != runs || stateOf(cause2) != stateCancelled {
		t.Fatalf("a late result woke a stopped request: %d run(s), %s", again, stateOf(cause2))
	}
}

// MEL-627: a helper on the same device runs under the coordinator's run. That
// run ending (here it parks, MEL-540) no longer kills the helper as "the
// daemon stopped": the helper finishes, and its result wakes the coordinator.
func TestLocalHelperOutlivesItsCoordinatorsRun(t *testing.T) {
	localHelperRun(t, `printf 'local part done\nemotion: calm\n'`, stateAnswered, "local part done", true)
}

// A helper that fails is finished too (owner, October 29, 2026: a failure
// counts): the coordinator runs again with that outcome.
func TestFailedLocalHelperWakesItsCoordinator(t *testing.T) {
	localHelperRun(t, `echo boom >&2; exit 3`, stateJobFailed, "ended without an answer: failed", false)
}

func localHelperRun(t *testing.T, helperAnswer, helperEnd, inPrompt string, askBack bool) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	stub := installAgentStub(t)
	Harnesses["roomasker"] = Harnesses["agentstub"]
	t.Cleanup(func() { delete(Harnesses, "roomasker") })
	dir := t.TempDir()
	gatedHarness(t, dir, "bossstub", "bossgo",
		`if grep -q "Results of the agents you asked" $d/bossstub.last; then printf 'final report\nemotion: calm\n'; else printf 'asked the helper\nemotion: calm\n'; fi`)
	gatedHarness(t, dir, "help1stub", "h1go", helperAnswer)
	w := newWorld(t, "")
	persons(t, w.alice)
	a := w.alice
	runAgent(t, a)
	phone := linked(t, a)
	publishGroupFixtureCaps(t, a, true)
	publishGroupFixtureCaps(t, phone, true)
	asker, err := a.CreateLocalAgent("asker", Responder{Harness: "roomasker", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	boss, err := a.CreateLocalAgent("boss", Responder{Harness: "bossstub", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := a.CreateLocalAgent("helper", Responder{Harness: "help1stub", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	from, err := a.SessionAgent("", asker.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobAt := func(room, lid string) string {
		var id string
		eventually(t, "request stored here", func() bool {
			return a.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, room, lid).Scan(&id) == nil
		})
		return id
	}
	req, err := a.SendAgentRoomAsk(tctx(t), AgentRoomAsk{From: from, SessionKey: "agentstub/report", Host: a.Address, AgentID: boss.ID, Kind: envelope.KindTask, Body: "make the monthly fee report"})
	if err != nil {
		t.Fatal(err)
	}
	cause := jobAt(req.Conv, req.LID)
	waitState(t, a, cause, stateRunning)
	ask, err := a.RunAgentRoomAsk(tctx(t), cause, AgentRoomAsk{Host: a.Address, AgentID: helper.ID, Kind: envelope.KindTask, Body: "your part of the report"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.WaitForHelper(cause, ask.ID); err != nil {
		t.Fatal(err)
	}
	child := jobAt(req.Conv, ask.LID)
	waitState(t, a, child, stateRunning)

	gate := func(name string) { os.WriteFile(filepath.Join(dir, name), nil, 0o600) }
	gate("bossgo")
	waitState(t, a, cause, stateHelpersWaiting)
	time.Sleep(300 * time.Millisecond)
	if s, _ := a.store.jobState(child); s != stateRunning {
		t.Fatalf("the coordinator's run ending stopped its local helper: %s", s)
	}
	runs := 2
	if askBack {
		// The helper needs the parked coordinator: answered now, on this device too.
		back, err := a.RunAgentRoomAsk(tctx(t), child, AgentRoomAsk{Host: a.Address, AgentID: boss.ID, Kind: envelope.KindQuestion, Body: "which month?"})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, a, jobAt(req.Conv, back.LID), stateAnswered)
		if s, _ := a.store.jobState(cause); s != stateHelpersWaiting {
			t.Fatalf("the helper's question woke the waiting request: %s", s)
		}
		runs = 3
	}
	gate("h1go")
	waitState(t, a, child, helperEnd)
	waitState(t, a, cause, stateAnswered)
	if n, prompt := gateLog(t, dir, "bossstub"); n != runs || !strings.Contains(prompt, inPrompt) {
		t.Fatalf("coordinator runs %d; continuation prompt:\n%s", n, prompt)
	}
}
