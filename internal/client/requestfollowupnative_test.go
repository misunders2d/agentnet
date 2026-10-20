package client

import (
	"bytes"
	"context"
	"encoding/json"
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

func nativeFollowupFixture(t *testing.T, files ...OutgoingFile) (*Agent, job, envelope.Ref) {
	t.Helper()
	w, _, packet, _ := groupTurnsFixture(t)
	participant := p6Member(t, w.alice, w.alice, packet.State.Conv)
	root, err := w.alice.AskAgent(tctx(t), participant.PID, envelope.KindQuestion, "Prepare contribution-margin.md in Russian", files...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); err != nil {
		t.Fatal(err)
	}
	var target string
	if err = w.alice.store.db.QueryRow(`SELECT target FROM inbox WHERE id=?`, root.LID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	j := job{ID: root.LID, From: w.alice.Address, Key: w.alice.Self().Fingerprint(), Kind: envelope.KindQuestion, Body: "Prepare contribution-margin.md in Russian", Conv: packet.State.Conv, PID: participant.PID, Local: true}
	if err = json.Unmarshal([]byte(target), &j.Target); err != nil {
		t.Fatal(err)
	}
	return w.alice, j, envelope.Ref{ID: root.LID, Fingerprint: w.alice.Self().Fingerprint()}
}

func TestNativeRequestFollowupAcceptanceOrderFilesAndNoReplay(t *testing.T) {
	a, parent, ref := nativeFollowupFixture(t)
	file := filepath.Join(t.TempDir(), "terms.md")
	if err := os.WriteFile(file, []byte("English terminology"), 0600); err != nil {
		t.Fatal(err)
	}
	ids := []string{protocol.NewID(), protocol.NewID()}
	for i, body := range []string{"Use English instead", "Put the result in Linear"} {
		x := RequestFollowup{Conv: parent.Conv, Ref: ref, ID: ids[i], Body: body}
		if i == 0 {
			x.Files = []OutgoingFile{{Path: file}}
		}
		if _, err := a.QueueRequestFollowup(tctx(t), x); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(tctx(t))
	defer cancel()
	in, out := io.Pipe()
	defer in.Close()
	var output bytes.Buffer
	ready := make(chan struct{})
	close(ready)
	bridge := &codexRoomBridge{ctx: ctx, input: out, output: &output, thread: "owned", turn: "active", calls: map[string]bool{}, ready: ready}
	defer bridge.close()
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
	for i, id := range ids {
		m := <-messages
		params := m["params"].(map[string]any)
		if params["clientUserMessageId"] != id {
			t.Fatalf("correction order: %v", params)
		}
		text := params["input"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, []string{"Use English instead", "Put the result in Linear"}[i]) {
			t.Fatal(text)
		}
		if i == 0 && !strings.Contains(text, "terms.md") {
			t.Fatal("missing staged correction file", text)
		}
		// A duplicate claim is impossible while acceptance is pending.
		if next, found, err := a.claimNativeFollowup(parent); err != nil || found {
			t.Fatalf("pending correction replayed: %v %v %v", next, found, err)
		}
		raw, _ := json.Marshal(map[string]any{"id": m["id"], "result": map[string]string{"turnId": "active"}})
		bridge.event(raw)
		waitState(t, a, id, stateSteered)
	}
	if state, err := a.store.jobState(parent.ID); err != nil || state != stateRunning {
		t.Fatalf("steer changed original: %s %v", state, err)
	}
	cancel()
	<-done
	if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
		t.Fatalf("accepted correction repeated: %v %v", found, err)
	}
	// Restart recovery retains accepted terminal corrections; it must not turn
	// them back into executable requests.
	if err := a.store.interruptRunning(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if state, err := a.store.jobState(id); err != nil || state != stateSteered {
			t.Fatal(state, err)
		}
	}
	if public, detail, ok := statusOf(stateSteered); !ok || public != "steered" || !strings.Contains(detail, "completion is not proven") {
		t.Fatal(public, detail, ok)
	}
}

// Pi's queue ACK does not prove that a correction reached native work. A
// completed parent must retain the uncertain correction without replaying it;
// an ordinary ask to the same participation remains separate eligible work.
func TestGroupNativeFollowupResultBeforeConsumption(t *testing.T) {
	a, parent, ref := nativeFollowupFixture(t)
	parent.nativeAttempt = 1
	if _, err := a.store.db.Exec(`UPDATE inbox SET attempts=1,last_attempt_at=unixepoch() WHERE id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	correction := RequestFollowup{Conv: parent.Conv, Ref: ref, ID: protocol.NewID(), Attempt: 1, Body: "Include the second requested comparison"}
	if _, err := a.QueueRequestFollowup(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	if marker, err := storedRequestFollowup(a.store.db, "in", correction.ID); err != nil || marker == nil || marker.Ref != ref || marker.Attempt != 1 {
		t.Fatalf("strict correction lost its exact binding: %+v %v", marker, err)
	}
	ordinaryID := protocol.NewID()
	ordinary, err := a.AskAgent(WithQueuedSend(tctx(t), ordinaryID), parent.PID, envelope.KindQuestion, "A separate question for the same agent")
	if err != nil || ordinary.LID != ordinaryID {
		t.Fatalf("ordinary same-participation ask %+v %v", ordinary, err)
	}
	if marker, err := storedRequestFollowup(a.store.db, "in", ordinaryID); err != nil || marker != nil {
		t.Fatalf("ordinary ask acquired steering metadata: %+v %v", marker, err)
	}

	bridge, decoder, output := piBridgeFixture(t)
	ctx, cancel := context.WithCancel(tctx(t))
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); a.watchNativeFollowups(ctx, parent, bridge, nil) }()
	request := make(chan map[string]any, 1)
	go func() {
		var m map[string]any
		_ = decoder.Decode(&m)
		request <- m
	}()
	var native map[string]any
	select {
	case native = <-request:
	case <-time.After(3 * time.Second):
		t.Fatal("strict correction did not reach the owned Pi bridge")
	}
	if native["type"] != "steer" || native["id"] != correction.ID {
		t.Fatalf("wrong native correction: %v", native)
	}
	piEvent(bridge, map[string]any{"type": "response", "id": correction.ID, "command": "steer", "success": true, "data": map[string]string{"disposition": "queued"}})
	if state := jobState(t, a, correction.ID); state != stateRunning {
		t.Fatalf("queue ACK falsely proved consumption: %s", state)
	}
	if state := jobState(t, a, ordinaryID); state != stateAgentWaiting {
		t.Fatalf("ordinary same-participation ask joined native steering: %s", state)
	}
	answer := "The first requested comparison is complete."
	piEvent(bridge, map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": answer}}, "stopReason": "stop"}})
	piEvent(bridge, map[string]any{"type": "agent_end"})
	piEvent(bridge, map[string]any{"type": "agent_settled", "aborted": false})
	bridge.finish()
	if got := strings.TrimSpace(output.String()); got != answer {
		t.Fatalf("parent result changed: %q", got)
	}
	// Use the production atomic result commit, including its correlated reply.
	a.finishAgent(WithQueuedSend(tctx(t), protocol.NewID()), parent, &Responder{Harness: "pi"}, envelope.StatusDone, output.String())
	waitState(t, a, parent.ID, stateAnswered)
	waitState(t, a, correction.ID, stateNeedHuman)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("uncertain correction watcher did not settle")
	}
	var resultID string
	if err = a.store.db.QueryRow(`SELECT coalesce(result_id,'') FROM inbox WHERE id=?`, parent.ID).Scan(&resultID); err != nil || resultID == "" {
		t.Fatalf("parent lacks committed result identity: %q %v", resultID, err)
	}
	var results int
	if err = a.store.db.QueryRow(`SELECT count(DISTINCT lid) FROM outbox WHERE conv=? AND pid=? AND reply_to=? AND kind=? AND body=?`, parent.Conv, parent.PID, parent.ID, envelope.KindAnswer, answer).Scan(&results); err != nil || results != 1 {
		t.Fatalf("parent result duplicated or lost: %d %v", results, err)
	}
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id=? AND conv=? AND pid=? AND reply_to=? AND kind=? AND body=?`, resultID, parent.Conv, parent.PID, parent.ID, envelope.KindAnswer, answer).Scan(&results); err != nil || results != 1 {
		t.Fatalf("parent result_id differs from its committed reply: %d %v", results, err)
	}
	if _, err = a.QueueRequestFollowup(tctx(t), correction); err != nil {
		t.Fatal("exact retry lost its durable correlation", err)
	}
	var corrections int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND lid=?`, correction.ID, correction.ID).Scan(&corrections); err != nil || corrections != 1 {
		t.Fatalf("correction identity changed on retry: %d %v", corrections, err)
	}
	// A late consumption-looking event and restart recovery cannot turn this
	// uncertain handover into an accepted correction or another automatic job.
	piEvent(bridge, map[string]any{"type": "message_start", "message": map[string]string{"role": "user", "content": native["message"].(string)}})
	bridge.mu.Lock()
	_, accepted := bridge.seen[correction.ID]
	bridge.mu.Unlock()
	if accepted {
		t.Fatal("late native event retroactively accepted uncertain correction")
	}
	if err = a.store.interruptRunning(); err != nil {
		t.Fatal(err)
	}
	if state := jobState(t, a, correction.ID); state != stateNeedHuman {
		t.Fatalf("restart replayed uncertain correction: %s", state)
	}
	next, found, _, _, err := a.store.claimAgentPage("pi", a.Address, a.Self().Fingerprint(), 0, agentPage)
	if err != nil || !found || next.ID != ordinaryID {
		t.Fatalf("ordinary ask was lost or correction replayed: %s %v %v", next.ID, found, err)
	}
}

func TestNativeRequestFollowupExactAdmission(t *testing.T) {
	a, parent, ref := nativeFollowupFixture(t)
	id := protocol.NewID()
	if _, err := a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: parent.Conv, Ref: ref, ID: id, Body: "Use English instead"}); err != nil {
		t.Fatal(err)
	}
	originalTarget := targetJSON(parent.Target)
	for _, mutation := range []struct {
		name, query string
		args        []any
	}{
		{"different target", `UPDATE inbox SET target=json_set(target,'$.fingerprint',?) WHERE id=?`, []any{strings.Repeat("f", 64), id}},
		{"different topic", `UPDATE inbox SET topic=? WHERE id=?`, []any{protocol.NewID(), id}},
		{"different kind", `UPDATE inbox SET kind=? WHERE id=?`, []any{envelope.KindTask, id}},
		{"different human", `UPDATE inbox SET verified_by=? WHERE id=?`, []any{strings.Repeat("f", 64), id}},
		{"inert history", `UPDATE inbox SET replica=1 WHERE id=?`, []any{id}},
		{"cancelled original", `UPDATE inbox SET state=? WHERE id=?`, []any{stateCancelReq, parent.ID}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := a.store.db.Exec(mutation.query, mutation.args...); err != nil {
				t.Fatal(err)
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
				t.Fatalf("changed exact admission steered: %v %v", found, err)
			}
			if _, err := a.store.db.Exec(`UPDATE inbox SET topic=NULL,kind=?,verified_by=?,replica=0,target=? WHERE id=?`, envelope.KindQuestion, a.Self().Fingerprint(), originalTarget, id); err != nil {
				t.Fatal(err)
			}
			if _, err := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, parent.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
	correction, found, err := a.claimNativeFollowup(parent)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if !a.nativeFollowupCurrent(parent, correction.job) {
		t.Fatal("valid correction lost current check")
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, id); err != nil {
		t.Fatal(err)
	}
	if a.nativeFollowupCurrent(parent, correction.job) {
		t.Fatal("cancelled correction crossed native boundary")
	}
}

func TestNativeRequestFollowupUnsupportedAndUnknownDoNotReplay(t *testing.T) {
	for _, mode := range []string{"unsupported", "wrong-turn", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			a, parent, ref := nativeFollowupFixture(t)
			id := protocol.NewID()
			file := filepath.Join(t.TempDir(), "terms.md")
			if err := os.WriteFile(file, []byte("English terminology"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: parent.Conv, Ref: ref, ID: id, Body: "Use English instead", Files: []OutgoingFile{{Path: file}}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(tctx(t))
			defer cancel()
			in, out := io.Pipe()
			defer in.Close()
			var output bytes.Buffer
			ready := make(chan struct{})
			close(ready)
			bridge := &codexRoomBridge{ctx: ctx, input: out, output: &output, thread: "owned", turn: "active", calls: map[string]bool{}, ready: ready}
			defer bridge.close()
			messages := make(chan map[string]any, 1)
			go func() { var m map[string]any; _ = json.NewDecoder(in).Decode(&m); messages <- m }()
			done := make(chan struct{})
			processFinished := make(chan struct{})
			defer func() {
				select {
				case <-processFinished:
				default:
					close(processFinished)
				}
			}()
			go func() { defer close(done); a.watchNativeFollowups(ctx, parent, bridge, processFinished) }()
			m := <-messages
			text := m["params"].(map[string]any)["input"].([]any)[0].(map[string]any)["text"].(string)
			entries, err := os.ReadDir(filepath.Join(a.home, "runs", id, "in"))
			if err != nil || len(entries) != 1 {
				t.Fatal("native correction lacks one staged file", err, text)
			}
			stagedPath := filepath.Join(a.home, "runs", id, "in", entries[0].Name())
			if !strings.Contains(text, fmt.Sprintf("%q", stagedPath)) {
				t.Fatal("native input did not contain the readable staged path", text)
			}
			state, err := a.store.jobState(id)
			if err != nil || state != stateRunning {
				t.Fatalf("handover lacked durable claim: %s %v", state, err)
			}
			if mode == "interrupted" {
				cancel()
			} else {
				response := map[string]any{"id": m["id"], "result": map[string]string{"turnId": "foreign"}}
				if mode == "unsupported" {
					response = map[string]any{"id": m["id"], "error": map[string]any{"code": -32601, "message": "unsupported"}}
				}
				raw, _ := json.Marshal(response)
				bridge.event(raw)
			}
			want := stateNeedHuman
			if mode == "unsupported" {
				want = stateAgentWaiting
			}
			waitState(t, a, id, want)
			cancel() // Watcher cancellation must not remove paths still in native use.
			if got, err := os.ReadFile(stagedPath); err != nil || string(got) != "English terminology" {
				t.Fatalf("correction files removed before process ended: %q %v", got, err)
			}
			select {
			case <-done:
				t.Fatal("file cleanup did not wait for native process completion")
			default:
			}
			close(processFinished)
			<-done
			if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
				t.Fatalf("correction files retained after process ended: %v", err)
			}
			if mode != "unsupported" {
				if err := a.store.interruptRunning(); err != nil {
					t.Fatal(err)
				}
				if state, err := a.store.jobState(id); err != nil || state != stateNeedHuman {
					t.Fatal("uncertain correction replayed after recovery", state, err)
				}
			} else if _, found, err := a.claimNativeFollowup(job{ID: parent.ID, Conv: "wrong-conversation", PID: parent.PID}); err != nil || found {
				t.Fatalf("queued correction migrated to another run: %v %v", found, err)
			}
		})
	}
}

// A real worker invocation talks to the re-executed mock app-server. No native
// user session, provider, credentials or model request is involved.
func TestNativeRequestFollowupOwnedWorkerProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable wrapper fixture")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := filepath.Join(bin, "codex")
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$AGENTNET_CODEX_FIXTURE_EXE\" -test.run '^TestCodexRoomFixtureProcess$'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENTNET_CODEX_FIXTURE_EXE", exe)
	t.Setenv("AGENTNET_CODEX_ROOM_FIXTURE", "steer")
	a, parent, ref := nativeFollowupFixture(t)
	id := protocol.NewID()
	if _, err = a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: parent.Conv, Ref: ref, ID: id, Body: "Use English instead"}); err != nil {
		t.Fatal(err)
	}
	a.runJob(tctx(t), parent, &Responder{Harness: "codex", Dir: t.TempDir(), Timeout: 15 * time.Second}, nil)
	waitState(t, a, id, stateSteered)
	waitState(t, a, parent.ID, stateAnswered)
	var count int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND reply_to=? AND kind=? AND body=?`, parent.Conv, parent.ID, envelope.KindAnswer, "OWNED_CORRECTION_ACCEPTED").Scan(&count); err != nil || count == 0 {
		t.Fatalf("owned process did not send the correlated original answer: %d %v", count, err)
	}
	if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
		t.Fatal("native accepted correction repeated", found, err)
	}
}

func TestNativeRequestFollowupDisabledNamedExecutorRefused(t *testing.T) {
	w := newWorld(t, "")
	agent, err := w.alice.CreateLocalAgent("same name", Responder{Harness: "codex", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	parent := job{Target: &envelope.Target{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint(), AgentID: agent.ID}}
	if !w.alice.nativeFollowupExecutor(w.alice.store.db, parent) {
		t.Fatal("current exact agent refused")
	}
	if err = w.alice.SetLocalAgentResponder(agent.ID, nil); err != nil {
		t.Fatal(err)
	}
	if w.alice.nativeFollowupExecutor(w.alice.store.db, parent) {
		t.Fatal("disabled identity received new native corrections")
	}
}

// Direct requests retain SQL NULL conversation/PID bindings and use the same
// signed carrier as real senders. No native model is involved in this fixture.
func directNativeCorrection(t *testing.T, named bool, kind string) (*Agent, *Agent, job, string) {
	t.Helper()
	w := newWorld(t, "")
	a, sender := w.bob, w.alice
	a.Logf = t.Logf
	if err := a.Approve(sender.Address); err != nil {
		t.Fatal(err)
	}
	var target *envelope.Target
	var executor *ExecutorStamp
	if named {
		local, err := a.CreateLocalAgent("same display name", Responder{Harness: "codex", Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		target = &envelope.Target{Address: a.Address, Fingerprint: a.Self().Fingerprint(), AgentID: local.ID}
		executor, err = a.ResolveExecutorIn(a.store.db, local.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	root := sealTo(t, sender, a, envelope.Inner{Kind: kind, Body: "Prepare the original report", Target: target})
	inner, err := envelope.Open(root, a.id, a.Address, sender.Self())
	if err != nil {
		t.Fatal(err)
	}
	if err = sender.store.addOutbox(root, inner, "", nil, boundOutgoing{fingerprint: a.Self().Fingerprint()}); err != nil {
		t.Fatal(err)
	}
	// sealTo already received relay custody; the saved author copy must not
	// pretend it still needs another network send when the reader starts.
	if _, err = sender.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, protocol.StateCustody, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.verifyAndStore(tctx(t), root); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,last_attempt_at=unixepoch() WHERE id=?`, stateRunning, root.ID); err != nil {
		t.Fatal(err)
	}
	parent := job{ID: root.ID, From: sender.Address, Key: sender.Self().Fingerprint(), Kind: kind, Body: "Prepare the original report", Target: target, Executor: executor, nativeAttempt: 1}
	if target != nil {
		parent.AgentID = target.AgentID
	}
	ref := &envelope.FollowupRef{Ref: envelope.Ref{ID: root.ID, Fingerprint: parent.Key}, Attempt: 1}
	correction := sealTo(t, sender, a, envelope.Inner{Kind: kind, Body: "Use English instead", ReplyTo: root.ID, Target: target, Followup: ref})
	if err := a.verifyAndStore(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	return a, sender, parent, correction.ID
}

func TestDirectNativeFollowupExactAdmission(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint("named=", named), func(t *testing.T) {
			a, sender, parent, id := directNativeCorrection(t, named, envelope.KindQuestion)
			original, err := sender.followupOriginal(sender.store.db, "", envelope.Ref{ID: parent.ID, Fingerprint: parent.Key})
			if err != nil {
				t.Fatal("sender exact original lookup failed", err)
			}
			if original.h.ID != parent.ID || original.h.FromKey != parent.Key || original.h.Kind != parent.Kind || original.h.Body != parent.Body || original.to != a.Address || original.key != a.Self().Fingerprint() {
				t.Fatal("sender original lost exact human request or recipient key")
			}
			if named {
				if original.h.Target == nil || *original.h.Target != *parent.Target {
					t.Fatal("sender original lost exact named target")
				}
			} else if original.h.Target != nil {
				t.Fatal("sender default original acquired a named target")
			}
			claim, found, err := a.claimNativeFollowup(parent)
			if err != nil || !found || claim.ID != id {
				t.Fatalf("direct correction not claimed: found=%t id=%s err=%v", found, claim.ID, err)
			}
			if !a.nativeFollowupCurrent(parent, claim.job) {
				t.Fatal("direct correction not current before handover")
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
				t.Fatal("claimed twice", found, err)
			}
		})
	}
}

func TestDirectNativeFollowupGuards(t *testing.T) {
	for _, mode := range []string{"ended", "cancelled", "attempt", "host-key", "kind", "topic", "receiver-route", "receiver-input", "revoked", "pending-key", "disabled", "unapproved", "author", "replica"} {
		t.Run(mode, func(t *testing.T) {
			a, sender, parent, id := directNativeCorrection(t, mode == "disabled", envelope.KindQuestion)
			var err error
			switch mode {
			case "ended":
				_, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, parent.ID)
			case "cancelled":
				_, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, parent.ID)
			case "attempt":
				_, err = a.store.db.Exec(`UPDATE inbox SET attempts=2 WHERE id=?`, parent.ID)
			case "host-key":
				_, err = a.store.db.Exec(`UPDATE inbox SET target=? WHERE id=?`, targetJSON(&envelope.Target{Address: a.Address, Fingerprint: sender.Self().Fingerprint(), AgentID: protocol.NewID()}), id)
			case "kind":
				_, err = a.store.db.Exec(`UPDATE inbox SET kind='task' WHERE id=?`, id)
			case "topic":
				_, err = a.store.db.Exec(`UPDATE inbox SET topic=? WHERE id=?`, protocol.NewID(), id)
			case "receiver-route":
				_, err = a.store.db.Exec(`UPDATE inbox SET receiver_route='{"op":"request"}' WHERE id=?`, id)
			case "receiver-input":
				binding := protocol.NewID()
				if _, err = a.store.db.Exec(`INSERT INTO reply_receivers(id,conv,request_ref,receiver,created_at) VALUES(?,'','{}','{}',1)`, binding); err == nil {
					_, err = a.store.db.Exec(`INSERT INTO reply_receiver_inputs(binding,inbox_id) VALUES(?,?)`, binding, id)
				}
			case "author":
				_, err = a.store.db.Exec(`UPDATE inbox SET verified_by=? WHERE id=?`, a.Self().Fingerprint(), id)
			case "replica":
				_, err = a.store.db.Exec(`UPDATE inbox SET replica=1 WHERE id=?`, id)
			case "revoked":
				_, err = a.store.db.Exec(`INSERT OR REPLACE INTO config(k,v) VALUES(?,?)`, revokedDevicePrefix+sender.Address+"/"+parent.Key, sender.Address)
			case "pending-key":
				_, err = a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, sender.Address)
			case "disabled":
				err = a.SetLocalAgentResponder(parent.AgentID, nil)
			case "unapproved":
				_, err = a.store.db.Exec(`DELETE FROM approvals WHERE address=?`, sender.Address)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
				t.Fatalf("%s admitted: %t %v", mode, found, err)
			}
		})
	}
}

func TestDirectNativeFollowupTaskConsentAndPrewrite(t *testing.T) {
	a, sender, parent, id := directNativeCorrection(t, false, envelope.KindTask)
	// The already-admitted parent's one-time acceptance is not a task grant.
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, statePending, id); err != nil {
		t.Fatal(err)
	}
	if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
		t.Fatal("parent acceptance authorized separate task", found, err)
	}
	if _, err := a.GrantTasks(sender.Address); err != nil {
		t.Fatal(err)
	}
	claim, found, err := a.claimNativeFollowup(parent)
	if err != nil || !found {
		t.Fatal("standing grant did not authorize correction", found, err)
	}
	if !a.nativeFollowupCurrent(parent, claim.job) {
		t.Fatal("admitted task unavailable")
	}
	if _, err := a.RevokeTasks(sender.Address); err != nil {
		t.Fatal(err)
	}
	if a.nativeFollowupCurrent(parent, claim.job) {
		t.Fatal("revoked task grant survived prewrite")
	}
	// Explicit acceptance of this correction is independently sufficient.
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAccepted, id); err != nil {
		t.Fatal(err)
	}
	claim, found, err = a.claimNativeFollowup(parent)
	if err != nil || !found {
		t.Fatal("exact correction acceptance refused", found, err)
	}
	if !a.nativeFollowupCurrent(parent, claim.job, true) {
		t.Fatal("one-time accepted correction refused")
	}
}

func TestDirectNativeFollowupPrewriteFences(t *testing.T) {
	for _, mode := range []string{"grant", "key", "attempt", "cancel", "disabled", "route"} {
		t.Run(mode, func(t *testing.T) {
			a, sender, parent, _ := directNativeCorrection(t, mode == "disabled", envelope.KindQuestion)
			claim, found, err := a.claimNativeFollowup(parent)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			switch mode {
			case "route":
				_, err = a.store.db.Exec(`UPDATE inbox SET receiver_route='{"op":"request"}' WHERE id=?`, claim.ID)
			case "grant":
				_, err = a.store.db.Exec(`DELETE FROM approvals WHERE address=?`, sender.Address)
			case "key":
				_, err = a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, sender.Address)
			case "attempt":
				_, err = a.store.db.Exec(`UPDATE inbox SET attempts=2 WHERE id=?`, parent.ID)
			case "cancel":
				_, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, parent.ID)
			case "disabled":
				err = a.SetLocalAgentResponder(parent.AgentID, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.nativeFollowupCurrent(parent, claim.job) {
				t.Fatal("prewrite fence failed", mode)
			}
		})
	}
}

func TestDirectStrictFollowupNeverLaunchesFallback(t *testing.T) {
	for _, mode := range []string{"ended", "cancelled", "changed", "unsupported", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			st := installStub(t, "answer")
			a, _, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
			if mode == "unsupported" || mode == "unknown" {
				ready := make(chan struct{})
				close(ready)
				outcome := steerUnsupported
				if mode == "unknown" {
					outcome = steerUnknown
				}
				a.watchNativeFollowups(t.Context(), parent, refusedFollowup{ready, outcome}, nil)
				waitState(t, a, id, stateNeedHuman)
			}
			state := stateAnswered
			if mode == "cancelled" {
				state = stateCancelled
			}
			if _, err := a.store.db.Exec(`UPDATE inbox SET state=?,attempts=2 WHERE id=?`, state, parent.ID); err != nil {
				t.Fatal(err)
			}
			child, found, err := a.store.claimJob("stub")
			if err != nil {
				t.Fatal(err)
			}
			if found {
				a.runJob(tctx(t), child, &Responder{Harness: "stub", Dir: st.dir, Timeout: time.Second}, nil)
			}
			if st.count() != 0 {
				t.Fatal("strict correction launched fresh process", mode, st.count())
			}
			waitState(t, a, id, stateNeedHuman)
		})
	}
}

func TestDirectNativeFollowupOwnedWorkerProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable wrapper fixture")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "codex")
	processLog := filepath.Join(t.TempDir(), "processes")
	script := "#!/bin/sh\necho started >> \"$AGENTNET_DIRECT_PROCESS_LOG\"\nexec \"$AGENTNET_CODEX_FIXTURE_EXE\" -test.run '^TestCodexRoomFixtureProcess$'\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(wrapper)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENTNET_CODEX_FIXTURE_EXE", exe)
	t.Setenv("AGENTNET_CODEX_ROOM_FIXTURE", "steer")
	t.Setenv("AGENTNET_DIRECT_PROCESS_LOG", processLog)
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint("named=", named), func(t *testing.T) {
			a, sender, parent, id := directNativeCorrection(t, named, envelope.KindQuestion)
			stop := runAgent(t, sender)
			defer stop()
			eventually(t, "recipient named capability advertised", func() bool { return a.requireAgentIdentity(tctx(t), sender.Self()) == nil })
			a.runJob(tctx(t), parent, &Responder{Harness: "codex", Dir: t.TempDir(), Timeout: 10 * time.Second}, nil)
			waitState(t, a, id, stateSteered)
			var parentState, parentDetail string
			a.store.db.QueryRow(`SELECT state,coalesce(detail,'') FROM inbox WHERE id=?`, parent.ID).Scan(&parentState, &parentDetail)
			t.Logf("owned direct result state=%s detail=%s", parentState, parentDetail)
			if parentState != stateAnswered {
				t.Fatalf("original did not finish: %s %s", parentState, parentDetail)
			}
			var raw []byte
			if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=(SELECT result_id FROM inbox WHERE id=?) AND body=?`, parent.ID, "OWNED_CORRECTION_ACCEPTED").Scan(&raw); err != nil {
				t.Fatal("missing correlated corrected answer", err)
			}
			var env envelope.Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			if err := sender.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal("signed corrected answer rejected", err)
			}
			if state, err := sender.store.jobState(env.ID); err != nil || state == "quarantined" {
				t.Fatal("verified answer unavailable", state, err)
			}
			var resultID string
			if err := a.store.db.QueryRow(`SELECT result_id FROM inbox WHERE id=?`, parent.ID).Scan(&resultID); err != nil || resultID != env.ID || env.Kind != envelope.KindAnswer {
				t.Fatal("correlated signed result is not the exact answer", err)
			}
			t.Log("correlated result_id and signed answer kind verified")
			var count, attempts int
			var answerID, result string
			if err := a.store.db.QueryRow(`SELECT count(DISTINCT coalesce(lid,id)),coalesce(max(id),''),coalesce(max(body),'') FROM outbox WHERE reply_to=? AND json_extract(envelope,'$.kind')=?`, parent.ID, envelope.KindAnswer).Scan(&count, &answerID, &result); err != nil || count != 1 || answerID != resultID || strings.TrimSpace(result) != "OWNED_CORRECTION_ACCEPTED" {
				t.Fatalf("direct answer proof failed: count=%d correlated=%t matched=%t err=%v", count, answerID == resultID, strings.TrimSpace(result) == "OWNED_CORRECTION_ACCEPTED", err)
			}
			if err := a.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, parent.ID).Scan(&attempts); err != nil || attempts != 1 {
				t.Fatal("original repeated", attempts, err)
			}
			var author, agentID, body string
			if err := sender.store.db.QueryRow(`SELECT verified_by,coalesce(agent_id,''),body FROM inbox WHERE id=? AND kind=? AND state!='quarantined'`, answerID, envelope.KindAnswer).Scan(&author, &agentID, &body); err != nil || author != a.Self().Fingerprint() || agentID != parent.AgentID || strings.TrimSpace(body) != "OWNED_CORRECTION_ACCEPTED" {
				t.Fatal("sender did not admit exact signed corrected answer", err)
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
				t.Fatal("finished correction replayed", found, err)
			}
		})
	}
	data, err := os.ReadFile(processLog)
	if err != nil || strings.Count(string(data), "started") != 2 {
		t.Fatal("expected one owned process per original", string(data), err)
	}
}

func TestDirectNativeFollowupLinkedHuman(t *testing.T) {
	w, first, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	stop := runAgent(t, a)
	second, awaited, _ := linkPhone(t, a, "second-correction-phone")
	request := pendingLink(t, a)
	stop()
	if err := a.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	original := sealTo(t, first, a, envelope.Inner{Kind: envelope.KindTask, Body: "Prepare original report"})
	if err := a.verifyAndStore(tctx(t), original); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,last_attempt_at=unixepoch() WHERE id=?`, stateRunning, original.ID); err != nil {
		t.Fatal(err)
	}
	parent := job{ID: original.ID, From: first.Address, Key: first.Self().Fingerprint(), Kind: envelope.KindTask, nativeAttempt: 1, OwnHumanRequest: true}
	correction := sealTo(t, second, a, envelope.Inner{Kind: envelope.KindTask, Body: "Use English instead", ReplyTo: original.ID, Followup: &envelope.FollowupRef{Ref: envelope.Ref{ID: original.ID, Fingerprint: first.Self().Fingerprint()}, Attempt: 1}})
	if err := a.verifyAndStore(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	claim, found, err := a.claimNativeFollowup(parent)
	if err != nil || !found || claim.ID != correction.ID {
		t.Fatal("current linked human correction refused", found, err)
	}
	if !a.nativeFollowupCurrent(parent, claim.job) {
		t.Fatal("current linked human unavailable before handover")
	}
	if _, err := a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, second.Address); err != nil {
		t.Fatal(err)
	}
	if a.nativeFollowupCurrent(parent, claim.job) {
		t.Fatal("linked changed key survived prewrite")
	}
}

func TestDirectNativeFollowupOrderFilesAndRetry(t *testing.T) {
	a, sender, parent, first := directNativeCorrection(t, false, envelope.KindQuestion)
	session := protocol.NewID()
	record := protocol.CapsRecord{Address: a.Address, Session: session, Caps: []string{protocol.CapRequestSteer}, TS: time.Now().Unix()}
	record.Sign(a.id.Sign)
	raw, _ := json.Marshal(record)
	sender.hub.http.Transport = &invitationCapsTransport{base: sender.hub.http.Transport, profile: protocol.Profile{Live: true, Sessions: []string{session}, Caps: []json.RawMessage{raw}}}
	file := filepath.Join(t.TempDir(), "terms.md")
	if err := os.WriteFile(file, []byte("English terminology"), 0600); err != nil {
		t.Fatal(err)
	}
	followup := &envelope.FollowupRef{Ref: envelope.Ref{ID: parent.ID, Fingerprint: parent.Key}, Attempt: 1}
	sent, err := sender.SendMessage(tctx(t), Outgoing{To: a.Address, Kind: envelope.KindQuestion, Body: "Use English and retain this file", ReplyTo: parent.ID, RequestFollowup: followup, Named: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := sender.store.outboxEnvelope(sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if err = a.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal("duplicate delivery", err)
	}
	ctx, cancel := context.WithCancel(tctx(t))
	defer cancel()
	in, out := io.Pipe()
	defer in.Close()
	ready := make(chan struct{})
	close(ready)
	var output bytes.Buffer
	bridge := &codexRoomBridge{ctx: ctx, input: out, output: &output, thread: "owned", turn: "active", calls: map[string]bool{}, ready: ready}
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
	for i, id := range []string{first, sent.ID} {
		var m map[string]any
		select {
		case m = <-messages:
		case <-time.After(3 * time.Second):
			t.Fatal("ordered direct correction unavailable", i)
		}
		params := m["params"].(map[string]any)
		if params["clientUserMessageId"] != id {
			t.Fatal("direct correction reordered", params)
		}
		text := params["input"].([]any)[0].(map[string]any)["text"].(string)
		if i == 1 && !strings.Contains(text, "terms.md") {
			t.Fatal("direct file missing", text)
		}
		if i == 1 {
			entries, e := os.ReadDir(filepath.Join(a.home, "runs", id, "in"))
			if e != nil || len(entries) != 1 {
				t.Fatal("staged direct bytes unavailable", e)
			}
			data, e := os.ReadFile(filepath.Join(a.home, "runs", id, "in", entries[0].Name()))
			if e != nil || string(data) != "English terminology" {
				t.Fatal("staged direct bytes changed", string(data), e)
			}
		}
		ack, _ := json.Marshal(map[string]any{"id": m["id"], "result": map[string]string{"turnId": "active"}})
		bridge.event(ack)
		waitState(t, a, id, stateSteered)
	}
	cancel()
	<-done
	if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
		t.Fatal("duplicate correction replayed", found, err)
	}
}
