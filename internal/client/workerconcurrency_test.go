package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Each actual subprocess records its request ID before waiting for its own
// release. No model, heartbeat, or second manually invoked scheduler is used.
func concurrencyHarness(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell barrier harness")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "barrier.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
cat >/dev/null
request=${AGENTNET_REQUEST_ID:-$AGENTNET_ROOM_REQUEST}
printf '%s\n' "$request" >> "$CONCURRENCY_LOG"
touch "$CONCURRENCY_LOG.$request.started"
while [ ! -f "$CONCURRENCY_LOG.$request.release" ]; do sleep 0.01; done
printf 'barrier answer\n'
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCURRENCY_LOG", filepath.Join(dir, "starts"))
	Harnesses["concurrencybarrier"] = harness{bin: script, stdin: true}
	t.Cleanup(func() { delete(Harnesses, "concurrencybarrier") })
	return dir
}

func barrierStarted(id string) bool {
	_, err := os.Stat(os.Getenv("CONCURRENCY_LOG") + "." + id + ".started")
	return err == nil
}
func releaseBarrier(t *testing.T, id string) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("CONCURRENCY_LOG")+"."+id+".release", nil, 0600); err != nil {
		t.Fatal(err)
	}
}
func requireBarrier(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !barrierStarted(id) {
		if time.Now().After(deadline) {
			t.Fatalf("executor did not enter harness within 2s: %s", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkerDistinctExecutorsConcurrent(t *testing.T) {
	dir := concurrencyHarness(t)
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	a, err := w.bob.CreateLocalAgent("same label", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.bob.CreateLocalAgent("same label", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	first := namedExecutorRequest(t, w, a.ID, envelope.KindQuestion, "")
	second := namedExecutorRequest(t, w, b.ID, envelope.KindTask, "")
	// Task permission is explicit, even in the synthetic admitted fixture.
	if err = w.bob.Accept(second); err != nil {
		t.Fatal(err)
	}
	runWith(t, w, w.bob, RunOptions{})
	requireBarrier(t, first)
	requireBarrier(t, second)
	if jobState(t, w.bob, first) != stateRunning || jobState(t, w.bob, second) != stateRunning {
		t.Fatal("both distinct executors must be running before release")
	}
	releaseBarrier(t, first)
	releaseBarrier(t, second)
	waitState(t, w.bob, first, stateAnswered)
	waitState(t, w.bob, second, stateAnswered)
}

func TestWorkerExecutorLaneWakeCancelAndUpdate(t *testing.T) {
	dir := concurrencyHarness(t)
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	a, err := w.bob.CreateLocalAgent("A", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.bob.CreateLocalAgent("B", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	first := namedExecutorRequest(t, w, a.ID, envelope.KindQuestion, "")
	runWith(t, w, w.bob, RunOptions{})
	requireBarrier(t, first)
	same := namedExecutorRequest(t, w, a.ID, envelope.KindQuestion, "")
	unaccepted := namedExecutorRequest(t, w, b.ID, envelope.KindTask, "")
	time.Sleep(100 * time.Millisecond)
	if barrierStarted(same) || barrierStarted(unaccepted) {
		t.Fatal("busy same executor or unaccepted task started")
	}
	if err = w.bob.Accept(unaccepted); err != nil {
		t.Fatal(err)
	}
	requireBarrier(t, unaccepted) // only local acceptance/change; first is still blocked
	if resume, err := w.bob.PauseForAppUpdate(); err == nil {
		resume()
		t.Fatal("update accepted while two executors active")
	}
	if err = w.bob.Cancel(first); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, first, stateCancelled)
	requireBarrier(t, same) // released lane wakes without unrelated B completion
	if jobState(t, w.bob, unaccepted) != stateRunning {
		t.Fatal("canceling A stopped B")
	}
	releaseBarrier(t, same)
	waitState(t, w.bob, same, stateAnswered)
	if resume, err := w.bob.PauseForAppUpdate(); err == nil {
		resume()
		t.Fatal("update accepted while remaining executor active")
	}
	releaseBarrier(t, unaccepted)
	waitState(t, w.bob, unaccepted, stateAnswered)
	var resume func()
	eventually(t, "all executor cleanup releases update fence", func() bool { resume, err = w.bob.PauseForAppUpdate(); return err == nil })
	fenced := namedExecutorRequest(t, w, a.ID, envelope.KindQuestion, "")
	time.Sleep(100 * time.Millisecond)
	if barrierStarted(fenced) {
		resume()
		t.Fatal("job crossed update idle fence")
	}
	resume()
	requireBarrier(t, fenced)
	releaseBarrier(t, fenced)
	waitState(t, w.bob, fenced, stateAnswered)
	for _, id := range []string{first, same, unaccepted, fenced} {
		var attempts int
		if err = w.bob.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, id).Scan(&attempts); err != nil || attempts != 1 {
			t.Fatalf("claim attempts %s: %d %v", id, attempts, err)
		}
	}
}

func TestWorkerGroupDistinctExecutorsConcurrent(t *testing.T) {
	dir := concurrencyHarness(t)
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	var requests []string
	for range 2 {
		agent, err := w.alice.CreateLocalAgent("same label", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
			t.Fatal(err)
		}
		part, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, agent.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "own named executor participation active", func() bool { return stateAt(t, w.alice, part.PID).Claimable() })
		request, err := w.alice.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "synthetic concurrent group request")
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request.ID)
		requireBarrier(t, request.ID)
	}
	for _, id := range requests {
		if jobState(t, w.alice, id) != stateRunning {
			t.Fatal("group request completed before both executors started")
		}
	}
	for _, id := range requests {
		releaseBarrier(t, id)
	}
	for _, id := range requests {
		waitState(t, w.alice, id, stateAnswered)
	}
}

func TestWorkerConcurrentStopRestartDoesNotReplay(t *testing.T) {
	dir := concurrencyHarness(t)
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	var requests []string
	for range 2 {
		agent, err := w.bob.CreateLocalAgent("same label", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, namedExecutorRequest(t, w, agent.ID, envelope.KindQuestion, ""))
	}
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	for _, id := range requests {
		requireBarrier(t, id)
	}
	stop() // return proves every active harness and cleanup was joined
	if !w.bob.executorsIdle() {
		t.Fatal("stopped daemon left executor reservations")
	}
	for _, id := range requests {
		waitState(t, w.bob, id, stateInterrupt)
	}
	runWith(t, w, w.bob, RunOptions{})
	time.Sleep(100 * time.Millisecond)
	starts, err := os.ReadFile(os.Getenv("CONCURRENCY_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(starts), "\n") != 2 {
		t.Fatalf("restart replayed harness: %s", starts)
	}
	for _, id := range requests {
		var attempts int
		if err = w.bob.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, id).Scan(&attempts); err != nil || attempts != 1 || jobState(t, w.bob, id) != stateInterrupt {
			t.Fatalf("restart changed interrupted claim: %s attempts=%d err=%v", id, attempts, err)
		}
	}
}

func TestWorkerConcurrentRealNamedRequests(t *testing.T) {
	dir := concurrencyHarness(t)
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		rec, err := w.bob.CreateLocalAgent("same label", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.ID)
	}
	runWith(t, w, w.alice, RunOptions{})
	runWith(t, w, w.bob, RunOptions{})
	eventually(t, "named execution advertised", func() bool { return w.alice.requireAgentIdentity(tctx(t), w.bob.Self()) == nil })
	var requests []string
	for i, id := range ids {
		kind := envelope.KindQuestion
		if i == 1 {
			kind = envelope.KindTask
		}
		sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: kind, Body: "synthetic signed concurrent request", Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: id}})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, sent.ID)
		if i == 1 {
			waitState(t, w.bob, sent.ID, stateAwaiting)
			if barrierStarted(sent.ID) {
				t.Fatal("real task ran before acceptance")
			}
			if err = w.bob.Accept(sent.ID); err != nil {
				t.Fatal(err)
			}
		}
		requireBarrier(t, sent.ID)
	}
	for _, id := range requests {
		if jobState(t, w.bob, id) != stateRunning {
			t.Fatal("real requests did not overlap")
		}
		releaseBarrier(t, id)
	}
	for i, id := range requests {
		waitState(t, w.bob, id, stateAnswered)
		eventually(t, "independent signed result with exact named author", func() bool { reply, ok := findReply(w.alice, id); return ok && reply.AgentID == ids[i] })
	}
}

func TestWorkerConcurrentPersonRemovalStopsEveryLane(t *testing.T) {
	dir := concurrencyHarness(t)
	w, roster := p7Person(t)
	roster, _ = p7Added(t, w, roster, "alice/phone", true)
	if _, err := w.bob.GrantTasks(roster.Person); err != nil {
		t.Fatal(err)
	}
	var requests []string
	for range 2 {
		agent, err := w.bob.CreateLocalAgent("same label", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, namedExecutorRequest(t, w, agent.ID, envelope.KindTask, ""))
	}
	runWith(t, w, w.bob, RunOptions{})
	for _, id := range requests {
		requireBarrier(t, id)
	}
	removed := protocol.PersonRoster{Person: roster.Person, Label: roster.Label, Seq: roster.Seq + 1, Prev: roster.Hash(), Devices: []identity.Public{roster.Devices[1]}, HumanKeys: []string{roster.Devices[1].Fingerprint()}, By: w.alice.Self().Fingerprint()}
	removed.Sign(w.alice.id.Sign)
	p7PinStep(t, w.bob, removed)
	for _, id := range requests {
		waitState(t, w.bob, id, stateNotDelivered)
	}
	eventually(t, "removal joined all active lane cleanup", w.bob.executorsIdle)
}

func TestWorkerConcurrentKickBroadcastCancelsEveryLane(t *testing.T) {
	dir := concurrencyHarness(t)
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	var requests []string
	for range 2 {
		agent, err := w.bob.CreateLocalAgent("same label", Responder{Harness: "concurrencybarrier", Dir: dir, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, namedExecutorRequest(t, w, agent.ID, envelope.KindQuestion, ""))
	}
	runWith(t, w, w.bob, RunOptions{})
	for _, id := range requests {
		requireBarrier(t, id)
	}
	// Simulate another local process's commit. No store Changed callback runs;
	// one coalesced wake must reach every watcher, never be consumed by one run.
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id IN (?,?)`, stateCancelReq, requests[0], requests[1]); err != nil {
		t.Fatal(err)
	}
	w.bob.wakeWorker()
	for _, id := range requests {
		waitState(t, w.bob, id, stateCancelled)
	}
	eventually(t, "kick canceled every executor and joined cleanup", w.bob.executorsIdle)
}

func TestWorkerExecutorAncestorLanes(t *testing.T) {
	w := newWorld(t, "")
	stamp := &ExecutorStamp{AgentID: "same"}
	w.bob.workerLanes.Lock()
	defer w.bob.workerLanes.Unlock()
	w.bob.reserveExecutor(job{ID: "root", Executor: stamp}, "")
	check := func(parent string, want bool) {
		t.Helper()
		got, err := w.bob.executorAvailable(w.bob.store.db, stamp, parent)
		if err != nil || got != want {
			t.Fatalf("parent=%q available=%v want=%v err=%v", parent, got, want, err)
		}
	}
	check("", false)
	check("unrelated", false)
	check("root", true)
	w.bob.reserveExecutor(job{ID: "child", Executor: stamp}, "root")
	check("root", false) // an active sibling cannot borrow the parent's busy child
	check("child", true) // exact grandchild may borrow its full active ancestry
	w.bob.reserveExecutor(job{ID: "other", Executor: stamp}, "")
	check("child", false) // unrelated active lane never grants authority
}

// Managed continuations own native sessions. A busy continuation must leave
// the next input pending without hiding an eligible different executor.
func TestWorkerReceiverLaneSkipsBusyWithoutClaim(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	var inputs []string
	var agents []*ExecutorStamp
	for range 2 {
		local, err := w.alice.CreateLocalAgent("same label", Responder{Harness: "stub", Dir: st.dir})
		if err != nil {
			t.Fatal(err)
		}
		stamp, err := w.alice.ResolveExecutorIn(w.alice.store.db, local.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, stamp)
		selected := &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "original authorized work", Mode: envelope.KindTask}
		sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "request", ReplyReceiver: selected})
		if err != nil {
			t.Fatal(err)
		}
		input := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Body: "correlated reply", ReplyTo: sent.ID})
		if err = w.alice.verifyAndStore(tctx(t), input); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input.ID)
	}
	w.alice.workerLanes.Lock()
	defer w.alice.workerLanes.Unlock()
	w.alice.reserveExecutor(job{ID: "already-running", Executor: agents[0]}, "")
	j, ok, err := w.alice.claimReplyReceiverJob(func(q dbq, stamp *ExecutorStamp) (bool, error) { return w.alice.executorAvailable(q, stamp, "") })
	if err != nil || !ok || j.ID != inputs[1] || j.Executor.AgentID != agents[1].AgentID {
		t.Fatalf("busy receiver hid independent executor: job=%+v ok=%v err=%v", j, ok, err)
	}
	var attempts int
	if err = w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, inputs[0]).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("busy native session input claimed: attempts=%d err=%v", attempts, err)
	}
}
