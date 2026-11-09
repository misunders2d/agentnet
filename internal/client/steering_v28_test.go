package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An owned protocol subprocess, with a distinct native thread per process.
// This proves session/input/process isolation without claiming a real model.
func TestV28CodexBarrierProcess(t *testing.T) {
	if os.Getenv("AGENTNET_V28_BARRIER") != "1" {
		t.Skip("owned subprocess fixture")
	}
	thread := fmt.Sprintf("owned-thread-%d", os.Getpid())
	if os.Getenv("AGENTNET_V28_SAME_THREAD") == "1" {
		thread = "shared-native-thread"
	}
	request := os.Getenv(AgentRequestEnv)
	if request == "" {
		request = os.Getenv(ProgressRequestEnv)
	}
	log := os.Getenv("CONCURRENCY_LOG")
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var message struct {
			ID     json.RawMessage
			Method string
			Params json.RawMessage
		}
		if dec.Decode(&message) != nil {
			os.Exit(0)
		}
		switch message.Method {
		case "initialize":
			_ = enc.Encode(map[string]any{"id": message.ID, "result": map[string]any{}})
		case "thread/start":
			var params map[string]any
			// MEL-746: a conversation agent's thread is saved for its next turn.
			if json.Unmarshal(message.Params, &params) != nil || params["ephemeral"] != false {
				os.Exit(2)
			}
			_ = os.WriteFile(log+"."+request+".thread", []byte(thread), 0600)
			_ = enc.Encode(map[string]any{"id": message.ID, "result": map[string]any{"thread": map[string]string{"id": thread}}})
		case "thread/resume":
			os.Exit(3)
		case "turn/start":
			var params map[string]any
			if json.Unmarshal(message.Params, &params) != nil || params["threadId"] != thread {
				os.Exit(4)
			}
			_ = enc.Encode(map[string]any{"id": message.ID, "result": map[string]any{"turn": map[string]string{"id": "active-turn"}}})
			proof, _ := json.Marshal(map[string]any{"thread": thread, "pid": os.Getpid(), "request": request})
			if os.WriteFile(log+"."+request+".started", proof, 0600) != nil {
				os.Exit(5)
			}
			go func() {
				for {
					if _, err := os.Stat(log + "." + request + ".release"); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				_ = enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": thread, "turnId": "active-turn", "item": map[string]string{"type": "agentMessage", "text": "owned barrier answer"}}})
				_ = enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]string{"id": "active-turn", "status": "completed"}}})
				os.Exit(0)
			}()
		}
	}
}

func freshCodexBarrier(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix owned executable wrapper")
	}
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "owned-codex")
	if err = os.WriteFile(script, []byte("#!/bin/sh\nexec \"$AGENTNET_V28_EXE\" -test.run '^TestV28CodexBarrierProcess$'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_V28_BARRIER", "1")
	t.Setenv("AGENTNET_V28_EXE", exe)
	t.Setenv("CONCURRENCY_LOG", filepath.Join(dir, "starts"))
	Harnesses["v28native"] = harness{bin: script, stdin: true, sessions: codexSessions}
	t.Cleanup(func() { delete(Harnesses, "v28native") })
	return dir
}

func TestAgentHandoverIncludesLateAuthorizedEvidence(t *testing.T) {
	a, parent, _ := nativeFollowupFixture(t)
	if _, err := a.SendConv(tctx(t), parent.Conv, ConvOutgoing{Body: "CANONICAL_BASELINE_POSTED_AFTER_REQUEST"}); err != nil {
		t.Fatal(err)
	}
	parent.run = a.newRun(parent, false)
	defer parent.run.remove()
	prompt, err := a.agentPrompt(parent, &Responder{Dir: t.TempDir()}, "", tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "CANONICAL_BASELINE_POSTED_AFTER_REQUEST") {
		t.Fatal("late-start handover excluded authorized canonical evidence already held in the group")
	}
}

// One ordinary host daemon must start independent conversations addressed to
// the same registered agent while its first real subprocess is still waiting.
func TestWorkerSameAgentIndependentConversations(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared-thread-%t", same), func(t *testing.T) { testSameAgentIndependentConversations(t, same) })
	}
}

func testSameAgentIndependentConversations(t *testing.T, same bool) {
	dir := freshCodexBarrier(t)
	if same {
		t.Setenv("AGENTNET_V28_SAME_THREAD", "1")
	}
	w, _, packet, _ := groupTurnsFixture(t)
	agent, err := w.alice.CreateLocalAgent("same registered agent", Responder{Harness: "v28native", Dir: dir, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	dm := newDM(t, w.alice, w.bob)
	var first string
	for _, conv := range []string{packet.State.Conv, dm} {
		part, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, agent.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "own named agent active", func() bool { return stateAt(t, w.alice, part.PID).Claimable() })
		request, err := w.alice.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "independent conversation")
		if err != nil {
			t.Fatal(err)
		}
		if first != "" && same {
			waitState(t, w.alice, request.ID, stateNeedHuman)
			if barrierStarted(request.ID) {
				t.Fatal("duplicate actual native thread received turn input")
			}
			if jobState(t, w.alice, first) != stateRunning {
				t.Fatal("duplicate session rejection stopped the other conversation")
			}
			if err := w.alice.Cancel(first); err != nil {
				t.Fatal(err)
			}
			waitState(t, w.alice, first, stateCancelled)
			return
		}
		requireBarrier(t, request.ID)
		if first == "" {
			first = request.ID
			continue
		}
		if jobState(t, w.alice, first) != stateRunning {
			t.Fatal("first subprocess ended before the independent conversation started")
		}
		var firstProof, secondProof struct {
			Thread string
			PID    int
		}
		one, _ := os.ReadFile(os.Getenv("CONCURRENCY_LOG") + "." + first + ".started")
		two, _ := os.ReadFile(os.Getenv("CONCURRENCY_LOG") + "." + request.ID + ".started")
		if json.Unmarshal(one, &firstProof) != nil || json.Unmarshal(two, &secondProof) != nil || firstProof.Thread == secondProof.Thread || firstProof.PID == secondProof.PID {
			t.Fatal("independent native threads/processes were not proven", string(one), string(two))
		}
		if err = w.alice.Cancel(first); err != nil {
			t.Fatal(err)
		}
		waitState(t, w.alice, first, stateCancelled)
		if jobState(t, w.alice, request.ID) != stateRunning {
			t.Fatal("canceling the group stopped the independent DM")
		}
		releaseBarrier(t, request.ID)
		waitState(t, w.alice, request.ID, stateAnswered)
	}
}

func TestNativeFollowupFreshEvidenceFilesAndCursor(t *testing.T) {
	a, parent, ref := nativeFollowupFixture(t)
	if _, err := a.store.db.Exec(`UPDATE inbox SET attempts=1 WHERE id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	parent.run = a.newRun(parent, false)
	defer parent.run.remove()
	if _, err := a.agentPrompt(parent, &Responder{Dir: t.TempDir()}, "", tctx(t)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "canonical-baseline.md")
	if err := os.WriteFile(file, []byte("CANONICAL_VERIFIED_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendConv(tctx(t), parent.Conv, ConvOutgoing{Body: "CANONICAL_EVIDENCE_POSTED_DURING_WORK", Files: []OutgoingFile{{Path: file}}}); err != nil {
		t.Fatal(err)
	}
	other, err := a.SendConv(tctx(t), parent.Conv, ConvOutgoing{Topic: "new", Body: "OTHER_TOPIC_PRIVATE_EVIDENCE", Files: []OutgoingFile{{Path: file, Name: "private-other-topic.md"}}})
	if err != nil {
		t.Fatal(err)
	}
	erased, err := a.SendConv(tctx(t), parent.Conv, ConvOutgoing{Body: "ERASED_CONTEXT", Files: []OutgoingFile{{Path: file, Name: "erased.md"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.db.Exec(`INSERT INTO conv_erased(conv,key,lid,deletion) VALUES(?,?,?,?)`, parent.Conv, a.Self().Fingerprint(), erased.LID, protocol.NewID()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(tctx(t))
	defer cancel()
	in, out := io.Pipe()
	defer in.Close()
	ready := make(chan struct{})
	close(ready)
	var output bytes.Buffer
	bridge := &codexRoomBridge{ctx: ctx, input: out, output: &output, thread: "owned", turn: "active", ready: ready}
	defer bridge.close()
	messages := make(chan map[string]any, 2)
	go func() {
		dec := json.NewDecoder(in)
		for {
			var m map[string]any
			if dec.Decode(&m) != nil {
				return
			}
			messages <- m
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); a.watchNativeFollowups(ctx, parent, bridge, nil) }()
	defer func() { cancel(); <-done }()
	for n := 0; n < 2; n++ {
		id := protocol.NewID()
		if _, err = a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: parent.Conv, Ref: ref, ID: id, Body: "Use English instead"}); err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		select {
		case message = <-messages:
		case <-time.After(3 * time.Second):
			t.Fatal("fresh evidence did not reach exact steering input")
		}
		text := message["params"].(map[string]any)["input"].([]any)[0].(map[string]any)["text"].(string)
		if strings.Contains(text, "OTHER_TOPIC_PRIVATE_EVIDENCE") || strings.Contains(text, other.LID) || strings.Contains(text, "private-other-topic.md") || strings.Contains(text, "ERASED_CONTEXT") || strings.Contains(text, "erased.md") {
			t.Fatal("unrelated/erased evidence was disclosed", text)
		}
		if n == 0 && (!strings.Contains(text, "CANONICAL_EVIDENCE_POSTED_DURING_WORK") || !strings.Contains(text, "CANONICAL_VERIFIED_BYTES") || !strings.Contains(text, "canonical-baseline.md")) {
			t.Fatal("fresh canonical evidence/file bytes missing", text)
		}
		if n == 1 && (strings.Contains(text, "CANONICAL_EVIDENCE_POSTED_DURING_WORK") || strings.Contains(text, "CANONICAL_VERIFIED_BYTES") || strings.Contains(text, "canonical-baseline.md")) {
			t.Fatal("already supplied evidence or file bytes were copied again", text)
		}
		ack, _ := json.Marshal(map[string]any{"id": message["id"], "result": map[string]string{"turnId": "active"}})
		bridge.event(ack)
		waitState(t, a, id, stateSteered)
	}
}

// Uses the owned active native bridge, so assertions inspect exactly the
// turn/steer input and advance only after its explicit native acknowledgment.
func cursorHandover(t *testing.T, a *Agent, parent job, ref envelope.Ref) func(bool) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	in, out := io.Pipe()
	ready := make(chan struct{})
	close(ready)
	var output bytes.Buffer
	bridge := &codexRoomBridge{ctx: ctx, input: out, output: &output, thread: "owned", turn: "active", ready: ready}
	messages := make(chan map[string]any, 4)
	go func() {
		dec := json.NewDecoder(in)
		for {
			var m map[string]any
			if dec.Decode(&m) != nil {
				return
			}
			messages <- m
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); a.watchNativeFollowups(ctx, parent, bridge, nil) }()
	t.Cleanup(func() { cancel(); <-done; bridge.close(); in.Close() })
	return func(accepted bool) string {
		t.Helper()
		id := protocol.NewID()
		if _, err := a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: parent.Conv, Ref: ref, ID: id, Body: "Review the current authorized evidence"}); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		select {
		case m = <-messages:
		case <-time.After(3 * time.Second):
			state, err := a.store.jobState(id)
			t.Fatalf("no owned native handover: context=%v correction=%s read=%v", ctx.Err(), state, err)
		}
		params := m["params"].(map[string]any)
		if params["clientUserMessageId"] != id {
			t.Fatal("replayed a different correction", params)
		}
		text := params["input"].([]any)[0].(map[string]any)["text"].(string)
		turn, state := "active", stateSteered
		if !accepted {
			turn, state = "wrong-turn", stateNeedHuman
		}
		ack, _ := json.Marshal(map[string]any{"id": m["id"], "result": map[string]string{"turnId": turn}})
		bridge.event(ack)
		waitState(t, a, id, state)
		if !accepted {
			<-done
			a.store.changed()
			select {
			case unexpected := <-messages:
				t.Fatal("unknown handover replayed", unexpected)
			default:
			}
		}
		return text
	}
}

func TestNativeCursorReceivesVerifiedEditAndDeletion(t *testing.T) {
	a, parent, ref := nativeFollowupFixture(t)
	parent.run = a.newRun(parent, false)
	defer parent.run.remove()
	if _, err := a.agentPrompt(parent, &Responder{Dir: t.TempDir()}, "", tctx(t)); err != nil {
		t.Fatal(err)
	}
	baseline, err := a.SendConv(tctx(t), parent.Conv, ConvOutgoing{Body: "LIVE_BASELINE_OLD"})
	if err != nil {
		t.Fatal(err)
	}
	handover := cursorHandover(t, a, parent, ref)
	if text := handover(true); !strings.Contains(text, "LIVE_BASELINE_OLD") {
		t.Fatal("initial live baseline missing", text)
	}
	author, err := a.RefOf(parent.Conv, baseline.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Revise(tctx(t), author, "LIVE_BASELINE_VERIFIED_REVISION"); err != nil {
		t.Fatal(err)
	}
	if text := handover(true); !strings.Contains(text, "LIVE_BASELINE_VERIFIED_REVISION") || strings.Contains(text, "LIVE_BASELINE_OLD") {
		t.Fatal("verified author edit hidden by cursor", text)
	}
	if _, err = a.Retract(tctx(t), author, "superseded"); err != nil {
		t.Fatal(err)
	}
	if text := handover(true); !strings.Contains(text, baseline.LID) || !strings.Contains(text, "a message its author deleted") || strings.Contains(text, "LIVE_BASELINE_VERIFIED_REVISION") {
		t.Fatal("verified deletion hidden or deleted plaintext restored", text)
	}
	if text := handover(true); strings.Contains(text, baseline.LID) {
		t.Fatal("unchanged deletion repeated", text)
	}
}

func TestNativeCursorRetriesUnavailableFile(t *testing.T) {
	for _, mode := range []struct{ startup, unknown bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		startup, unknown := mode.startup, mode.unknown
		t.Run(fmt.Sprintf("startup-%t-unknown-%t", startup, unknown), func(t *testing.T) {
			a, parent, ref := nativeFollowupFixture(t)
			parent.run = a.newRun(parent, false)
			defer parent.run.remove()
			if !startup {
				if _, err := a.agentPrompt(parent, &Responder{Dir: t.TempDir()}, "", tctx(t)); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "ready-later.md")
			if err := os.WriteFile(path, []byte("LATER_READY_EXACT_BYTES"), 0600); err != nil {
				t.Fatal(err)
			}
			baseline, err := a.SendConv(tctx(t), parent.Conv, ConvOutgoing{Body: "LIVE_FILE_BASELINE", Files: []OutgoingFile{{Path: path}}})
			if err != nil {
				t.Fatal(err)
			}
			files, err := a.store.sentAttachments(baseline.ID)
			if err != nil || len(files) != 1 {
				t.Fatal(err, files)
			}
			kept := a.keptPath(files[0].SHA256)
			if err = os.Rename(kept, kept+".held"); err != nil {
				t.Fatal(err)
			}
			if startup {
				text, err := a.agentPrompt(parent, &Responder{Dir: t.TempDir()}, "", tctx(t))
				if err != nil || !strings.Contains(text, "LIVE_FILE_BASELINE") || !strings.Contains(text, "bytes unavailable here") {
					t.Fatal("startup fixture did not supply text while bytes unavailable", err, text)
				}
			}
			handover := cursorHandover(t, a, parent, ref)
			initial := handover(true)
			if !startup && (!strings.Contains(initial, "LIVE_FILE_BASELINE") || !strings.Contains(initial, "bytes unavailable here")) {
				t.Fatal("initial native text/file availability missing", initial)
			}
			if err = os.Rename(kept+".held", kept); err != nil {
				t.Fatal(err)
			}
			stages := 0
			old := afterRunFileStaged
			afterRunFileStaged = func(string) { stages++ }
			t.Cleanup(func() { afterRunFileStaged = old })
			before, _ := json.Marshal(parent.run.context)
			text := handover(!unknown)
			if !strings.Contains(text, "LATER_READY_EXACT_BYTES") || strings.Contains(text, "LIVE_FILE_BASELINE") {
				t.Fatal("ready bytes lost or old text repeated", text)
			}
			if unknown {
				after, _ := json.Marshal(parent.run.context)
				if !bytes.Equal(before, after) {
					t.Fatal("unknown acknowledgment advanced delivery", string(before), string(after))
				}
			} else if text := handover(true); strings.Contains(text, "ready-later.md") || strings.Contains(text, "LATER_READY_EXACT_BYTES") || strings.Contains(text, "LIVE_FILE_BASELINE") {
				t.Fatal("successful bytes or unchanged text repeated", text)
			}
			if stages != 1 {
				t.Fatalf("successful bytes restaged: %d", stages)
			}
		})
	}
}

func TestNativeCursorSuppliesDeferredParentAttachments(t *testing.T) {
	for _, mode := range []struct{ omitted, unknown bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("omitted-%t-unknown-%t", mode.omitted, mode.unknown), func(t *testing.T) {
			if mode.omitted {
				old := maxRunFiles
				maxRunFiles = 1 // real existing staging limit; first parent file wins
				t.Cleanup(func() { maxRunFiles = old })
			}
			dir := t.TempDir()
			first, second := filepath.Join(dir, "already-parent.md"), filepath.Join(dir, "deferred-parent.md")
			if err := os.WriteFile(first, []byte("ALREADY_SUPPLIED_PARENT_BYTES"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(second, []byte("DEFERRED_PARENT_EXACT_BYTES"), 0600); err != nil {
				t.Fatal(err)
			}
			a, parent, ref := nativeFollowupFixture(t, OutgoingFile{Path: first}, OutgoingFile{Path: second})
			parent.run = a.newRun(parent, false)
			defer parent.run.remove()
			files, err := a.store.sentAttachments(parent.ID)
			if err != nil || len(files) != 2 {
				t.Fatal(err, files)
			}
			kept := a.keptPath(files[1].SHA256)
			if !mode.omitted {
				if err = os.Rename(kept, kept+".held"); err != nil {
					t.Fatal(err)
				}
			}
			initial, err := a.agentPrompt(parent, &Responder{Dir: t.TempDir()}, "", tctx(t))
			why := "bytes unavailable here"
			if mode.omitted {
				why = "not given to this run"
			}
			if err != nil || !strings.Contains(initial, parent.Body) || !strings.Contains(initial, "ALREADY_SUPPLIED_PARENT_BYTES") || !strings.Contains(initial, why) || strings.Contains(initial, "DEFERRED_PARENT_EXACT_BYTES") {
				t.Fatal("parent startup fixture", err, initial)
			}
			if !mode.omitted {
				if err = os.Rename(kept+".held", kept); err != nil {
					t.Fatal(err)
				}
			}
			stages := 0
			old := afterRunFileStaged
			afterRunFileStaged = func(string) { stages++ }
			t.Cleanup(func() { afterRunFileStaged = old })
			before, _ := json.Marshal(parent.run.context)
			handover := cursorHandover(t, a, parent, ref)
			text := handover(!mode.unknown)
			if !strings.Contains(text, "DEFERRED_PARENT_EXACT_BYTES") || !strings.Contains(text, "deferred-parent.md") || strings.Contains(text, parent.Body) || strings.Contains(text, "ALREADY_SUPPLIED_PARENT_BYTES") || strings.Contains(text, "already-parent.md") || strings.Contains(text, parent.run.path) {
				t.Fatal("parent attachment missing, old bytes/text repeated or staged in original run", text)
			}
			if mode.unknown {
				after, _ := json.Marshal(parent.run.context)
				if !bytes.Equal(before, after) {
					t.Fatal("unknown acknowledgment advanced parent receipt", string(before), string(after))
				}
			} else if text := handover(true); strings.Contains(text, "deferred-parent.md") || strings.Contains(text, "DEFERRED_PARENT_EXACT_BYTES") || strings.Contains(text, parent.Body) {
				t.Fatal("accepted parent bytes/text repeated", text)
			}
			if stages != 1 {
				t.Fatalf("parent bytes restaged %d times", stages)
			}
		})
	}
}

func TestWorkerIndependentRootCapDoesNotQueueControls(t *testing.T) {
	a, parent, ref := nativeFollowupFixture(t)
	stamp := &ExecutorStamp{AgentID: "new", Responder: Responder{Harness: "codex"}}
	a.workerLanes.Lock()
	if a.workerLanes.jobs == nil {
		a.workerLanes.jobs = map[string]executionLane{}
	}
	for n := 0; n < protocol.MaxAgentCatalog+1; n++ {
		a.workerLanes.jobs[fmt.Sprint(n)] = executionLane{executor: fmt.Sprint(n), conv: "other", fresh: true}
	}
	a.workerLanes.Unlock()
	candidate := job{ID: protocol.NewID(), AgentID: "new", PID: protocol.NewID(), Conv: parent.Conv, Kind: envelope.KindQuestion}
	a.workerLanes.Lock()
	available, err := a.executorAvailable(a.store.db, stamp, "", candidate)
	if available || !errors.Is(err, errIndependentLimit) {
		t.Fatalf("root limit bypassed: %v %v", available, err)
	}
	available, err = a.executorAvailable(a.store.db, stamp, "0", candidate)
	a.workerLanes.Unlock()
	if !available || err != nil {
		t.Fatalf("exact descendant was queued behind root cap: %v %v", available, err)
	}
	id := protocol.NewID()
	if _, err = a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: parent.Conv, Ref: ref, ID: id, Body: "Use English instead"}); err != nil {
		t.Fatal(err)
	}
	if correction, found, err := a.claimNativeFollowup(parent); err != nil || !found || correction.ID != id {
		t.Fatalf("control waited in the root queue: %v %v %v", correction, found, err)
	}
	if err = a.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	if state := jobState(t, a, parent.ID); state != stateCancelReq {
		t.Fatal("Stop waited in the root queue", state)
	}
}

func TestWorkerPeerWaitVisibilityRetainsActualReason(t *testing.T) {
	a, parent, _ := nativeFollowupFixture(t)
	if _, err := a.store.db.Exec(`UPDATE inbox SET last_attempt_at=unixepoch() WHERE id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	a.workerLanes.Lock()
	a.reserveExecutor(parent, "")
	a.workerLanes.Unlock()
	a.noteRoomWait(parent.ID, 1)
	a.noteRoomWait(parent.ID, 1)
	if changed, err := a.store.markRunVisibility(parent.ID, true); err != nil || changed {
		t.Fatal("known peer dependency became unexplained silence", changed, err)
	}
	a.noteRoomWait(parent.ID, -1)
	var detail string
	if err := a.store.db.QueryRow(`SELECT detail FROM inbox WHERE id=?`, parent.ID).Scan(&detail); err != nil || detail != roomWaitDetail {
		t.Fatal("one peer reply hid another active wait", detail, err)
	}
	a.noteRoomWait(parent.ID, -1)
	if err := a.store.db.QueryRow(`SELECT detail FROM inbox WHERE id=?`, parent.ID).Scan(&detail); err != nil || !strings.HasPrefix(detail, "Running since ") {
		t.Fatal("finished dependency still shows peer wait", detail, err)
	}
}

func TestNativeSessionReservationRefusesCanceledRequest(t *testing.T) {
	a, parent, _ := nativeFollowupFixture(t)
	if err := a.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	if a.bindNativeThread(parent, sessionRef{Harness: "codex", Mode: "question", ID: "unstarted-native-thread"}) {
		t.Fatal("stopped request obtained a live native session reservation")
	}
}

func TestNativeFreshReservationRefusesKnownSavedThread(t *testing.T) {
	a, parent, _ := nativeFollowupFixture(t)
	ref := sessionRef{Harness: "codex", Mode: "question", ID: "previous-owned-thread"}
	if err := a.store.setConfig(map[string]string{headKey(ref): "completed-other-conversation"}); err != nil {
		t.Fatal(err)
	}
	if a.bindNativeThread(parent, ref, true) {
		t.Fatal("fresh launch substituted a saved native session")
	}
}

func TestIndependentSessionRejectsCustomNonprotocolLauncher(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := Harnesses["pi"]
	custom := old
	custom.bin = exe
	Harnesses["pi"] = custom
	t.Cleanup(func() { Harnesses["pi"] = old })
	j := job{Kind: envelope.KindQuestion, PID: protocol.NewID(), Conv: "conversation", AgentID: protocol.NewID()}
	if independentSession(j, &ExecutorStamp{AgentID: j.AgentID, Responder: Responder{Harness: "pi"}}) {
		t.Fatal("harness name/fresh flags qualified an unknown custom executable")
	}
}
