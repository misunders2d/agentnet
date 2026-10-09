package client

import (
	"bytes"
	"context"
	"encoding/json"
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

func nativeFollowupFixture(t *testing.T) (*Agent, job, envelope.Ref) {
	t.Helper()
	w, _, packet, _ := groupTurnsFixture(t)
	participant := p6Member(t, w.alice, w.alice, packet.State.Conv)
	root, err := w.alice.AskAgent(tctx(t), participant.PID, envelope.KindQuestion, "Prepare contribution-margin.md in Russian")
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
			var stagedPath string
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, `- "terms.md"`) {
					if err := json.Unmarshal([]byte(line[strings.LastIndex(line, ": ")+2:]), &stagedPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			if stagedPath == "" {
				t.Fatal("native correction lacks readable staged path", text)
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
