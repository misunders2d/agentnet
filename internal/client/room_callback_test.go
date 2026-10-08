package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Every edge is issued by an actual harness through the exact-run CLI binding.
// There is one daemon per device, with no manually claimed or revived inbox row.
func TestRoomBusyAncestorCallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell callback harness")
	}
	bin := filepath.Join(t.TempDir(), "agentnet")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/agentnet")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, out)
	}
	t.Setenv("CALLBACK_CLI", bin)
	for _, mode := range []string{"simple", "named_hop", "cancel"} {
		t.Run(mode, func(t *testing.T) { testRoomBusyAncestorCallback(t, mode) })
	}
}

func testRoomBusyAncestorCallback(t *testing.T, mode string) {
	dir := t.TempDir()
	t.Setenv("CALLBACK_DIR", dir)
	scripts := map[string]string{
		"callbacka": `#!/bin/sh
set -e
cat >/dev/null
printf '%s\n' "$AGENTNET_ROOM_REQUEST" >> "$CALLBACK_DIR/runs"
if mkdir "$CALLBACK_DIR/root" 2>/dev/null; then
  printf '%s' "$AGENTNET_ROOM_REQUEST" > "$CALLBACK_DIR/a0"
  while [ ! -f "$CALLBACK_DIR/go" ]; do sleep 0.02; done
  "$CALLBACK_CLI" --home "$AGENTNET_HOME" room ask --pid "$CALLBACK_B" B0
else
  printf '%s' "$AGENTNET_ROOM_REQUEST" > "$CALLBACK_DIR/a1"
  while [ ! -f "$CALLBACK_DIR/release" ]; do sleep 0.02; done
  printf 'CALLBACK_COMPLETE\n'
fi
`,
		"callbackb": `#!/bin/sh
set -e
cat >/dev/null
printf '%s\n' "$AGENTNET_ROOM_REQUEST" >> "$CALLBACK_DIR/runs"
if mkdir "$CALLBACK_DIR/remote" 2>/dev/null; then
  printf '%s' "$AGENTNET_ROOM_REQUEST" > "$CALLBACK_DIR/b0"
  "$CALLBACK_CLI" --home "$AGENTNET_HOME" room ask --pid "$CALLBACK_FIRST" CALLBACK
else
  printf '%s' "$AGENTNET_ROOM_REQUEST" > "$CALLBACK_DIR/b1"
  "$CALLBACK_CLI" --home "$AGENTNET_HOME" room ask --pid "$CALLBACK_A" A1
fi
`,
		"callbackc": `#!/bin/sh
set -e
cat >/dev/null
printf '%s\n' "$AGENTNET_ROOM_REQUEST" >> "$CALLBACK_DIR/runs"
printf '%s' "$AGENTNET_ROOM_REQUEST" > "$CALLBACK_DIR/c0"
"$CALLBACK_CLI" --home "$AGENTNET_HOME" room ask --pid "$CALLBACK_B" B1
`,
	}
	for name, script := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		Harnesses[name] = harness{bin: path, stdin: true}
		t.Cleanup(func() { delete(Harnesses, name) })
	}
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	a := p6Member(t, w.alice, w.alice, conv)
	b := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "source agent known at remote host", func() bool { return stateAt(t, w.bob, a.PID).Claimable() })
	t.Setenv("CALLBACK_A", a.PID)
	t.Setenv("CALLBACK_B", b.PID)
	firstPID := a.PID
	if mode == "named_hop" {
		named, err := w.alice.CreateLocalAgent("intermediate", Responder{Harness: "callbackc", Dir: dir, Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.alice.PublishAgentCatalog(tctx(t)); err != nil {
			t.Fatal(err)
		}
		c, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "intermediate agent known at both hosts", func() bool { return stateAt(t, w.alice, c.PID).Claimable() && stateAt(t, w.bob, c.PID).Claimable() })
		firstPID = c.PID
	}
	t.Setenv("CALLBACK_FIRST", firstPID)
	for _, pair := range []struct{ host, peer *Agent }{{w.alice, w.bob}, {w.bob, w.alice}} {
		if err := pair.host.Approve(pair.peer.Address); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct {
		host    *Agent
		harness string
	}{{w.alice, "callbacka"}, {w.bob, "callbackb"}} {
		if err := pair.host.SetResponder(&Responder{Harness: pair.harness, Dir: dir, Timeout: 30 * time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := w.alice.AskAgent(tctx(t), a.PID, envelope.KindQuestion, "A0")
	if err != nil {
		t.Fatal(err)
	}
	readID := func(name string) string { data, _ := os.ReadFile(filepath.Join(dir, name)); return string(data) }
	touch := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "root harness running", func() bool { return readID("a0") == root.ID })
	unrelated, err := w.alice.AskAgent(tctx(t), a.PID, envelope.KindQuestion, "independent root")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "simple" {
		// Already-running work must be able to finish during an idle update wait.
		w.alice.update.Lock()
		w.alice.update.pending = &UpdateRequest{ID: "callback-pending-update", To: "v9.9.9"}
		w.alice.update.Unlock()
	}
	touch("go")
	eventually(t, "remote harness running", func() bool { return readID("b0") != "" })
	lastRemote := "b0"
	if mode == "named_hop" {
		eventually(t, "remote descendant on another local executor", func() bool { return readID("c0") != "" })
		eventually(t, "second remote callback", func() bool { return readID("b1") != "" })
		lastRemote = "b1"
	}
	var callback string
	eventually(t, "received callback admitted", func() bool {
		return w.alice.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND pid=? AND reply_to=? AND local=0 AND replica=0`, conv, a.PID, readID(lastRemote)).Scan(&callback) == nil
	})
	// This assertion fails on the released scheduler: the received A1 stays
	// waiting while its real A0/B0 harnesses hold their lanes waiting for it.
	eventually(t, "callback borrows its live ancestor lane", func() bool { return readID("a1") == callback })
	if jobState(t, w.alice, root.ID) != stateRunning || jobState(t, w.bob, readID("b0")) != stateRunning {
		t.Fatal("ancestors stopped before callback ran")
	}
	if jobState(t, w.alice, unrelated.ID) != stateAgentWaiting {
		t.Fatal("unrelated same-executor root borrowed ancestor lane")
	}
	if resume, err := w.alice.PauseForAppUpdate(); err == nil {
		resume()
		t.Fatal("app update admitted with active callback")
	}
	parent := root.ID
	if mode == "named_hop" {
		parent = readID("c0")
	}
	w.alice.workerLanes.Lock()
	callbackLane := w.alice.workerLanes.jobs[callback]
	intermediateLane := w.alice.workerLanes.jobs[readID("c0")]
	w.alice.workerLanes.Unlock()
	if callbackLane.parent != parent || mode == "named_hop" && intermediateLane.parent != root.ID {
		t.Fatal("root scheduler stole remote descendant lane ownership")
	}
	if mode == "simple" {
		testRoomCallbackAuthority(t, w.alice, conv, callback, root.ID, readID("b0"))
		if w.alice.updatePending() == nil {
			t.Fatal("callback lost the pending update")
		}
		w.alice.update.Lock()
		w.alice.update.pending = nil
		w.alice.update.Unlock()
	}
	if mode == "cancel" {
		if err := w.alice.Cancel(root.ID); err != nil {
			t.Fatal(err)
		}
		waitState(t, w.alice, root.ID, stateCancelled)
		eventually(t, "cancellation joins callback cleanup before next root", func() bool {
			w.alice.workerLanes.Lock()
			defer w.alice.workerLanes.Unlock()
			_, old := w.alice.workerLanes.jobs[callback]
			_, next := w.alice.workerLanes.jobs[unrelated.ID]
			return !old && next
		})
		touch("release")
		waitState(t, w.alice, unrelated.ID, stateAnswered)
		// The remote wait may still be live until its ordinary timeout/cancel.
		// Local cancellation proves only local stop and fences its late output.
		if err := w.bob.Cancel(readID("b0")); err != nil && jobState(t, w.bob, readID("b0")) == stateRunning {
			t.Fatal(err)
		}
		if _, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool {
			return m.Kind == envelope.KindAnswer && (m.ReplyTo == callback || m.ReplyTo == root.LID)
		}); n != 0 {
			t.Fatal("cancelled callback emitted a late answer")
		}
	} else {
		touch("release")
		edges := []struct {
			host         *Agent
			id, pid, key string
		}{{w.bob, callback, a.PID, w.alice.Self().Fingerprint()}, {w.alice, readID("b0"), b.PID, w.bob.Self().Fingerprint()}, {w.alice, root.LID, a.PID, w.alice.Self().Fingerprint()}}
		if mode == "named_hop" {
			edges = append(edges, struct {
				host         *Agent
				id, pid, key string
			}{w.alice, readID("b1"), b.PID, w.bob.Self().Fingerprint()}, struct {
				host         *Agent
				id, pid, key string
			}{w.bob, readID("c0"), firstPID, w.alice.Self().Fingerprint()})
		}
		for _, edge := range edges {
			var reply ConvMessage
			eventually(t, "verified reply to "+edge.id, func() bool {
				var n int
				reply, n = convMsg(t, edge.host, conv, func(m ConvMessage) bool { return m.Kind == envelope.KindAnswer && m.ReplyTo == edge.id })
				return n == 1
			})
			if !reply.VerifiedAgent || reply.PID != edge.pid || reply.Key != edge.key || !strings.Contains(reply.Body, "CALLBACK_COMPLETE") {
				t.Fatalf("wrong correlated reply: %+v", reply)
			}
		}
		waitState(t, w.alice, unrelated.ID, stateAnswered)
	}
	eventually(t, "ancestor and callback cleanup joins", func() bool { return w.alice.executorsIdle() && w.bob.executorsIdle() })
	data, err := os.ReadFile(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, id := range strings.Fields(string(data)) {
		counts[id]++
	}
	want := 4
	if mode == "named_hop" {
		want = 6
	}
	if len(counts) != want {
		t.Fatalf("unexpected actual invocations: %v", counts)
	}
	for id, n := range counts {
		if n != 1 {
			t.Fatalf("request %s invoked %d times", id, n)
		}
	}
}

// Alter only rolled-back transaction views of the naturally admitted chain.
// These checks cannot wake a worker, revive work, or manufacture run ownership.
func testRoomCallbackAuthority(t *testing.T, a *Agent, conv, callback, root, remote string) {
	t.Helper()
	original, err := roomCauseIn(a.store.db, conv, callback, a.Address, a.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	r := agentReq{ID: callback, Conv: conv, PID: original.pid, Sender: original.from, Key: original.key, Kind: original.kind, Target: original.target, State: stateAgentWaiting}
	for _, change := range []string{"valid", "wrong_cause", "missing_cause", "wrong_key", "wrong_agent", "ambiguous", "cycle", "stopped_parent", "completed_parent", "cancel_requested_parent", "missing_local_run", "missing_parent", "revoked_grant", "question_to_task"} {
		t.Run(change, func(t *testing.T) {
			tx, err := a.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := tx.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			request := r
			want := verdictStop
			switch change {
			case "wrong_cause":
				exec(`UPDATE inbox SET reply_to=? WHERE id=?`, root, callback)
			case "missing_cause":
				exec(`UPDATE inbox SET reply_to=? WHERE id=?`, strings.Repeat("f", 32), callback)
				want = verdictWait
			case "wrong_key":
				exec(`UPDATE inbox SET verified_by=? WHERE id=?`, strings.Repeat("f", 32), callback)
			case "wrong_agent":
				exec(`UPDATE inbox SET target=json_set(target,'$.agent_id',?) WHERE id=?`, strings.Repeat("f", 32), callback)
			case "ambiguous":
				exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,verified_by,conv,lid,pid,target,human,reply_to,state)
				 SELECT ?,sender,ts,kind,body,received_at,?,conv,lid,pid,target,human,reply_to,state FROM inbox WHERE id=?`, strings.Repeat("e", 32), strings.Repeat("f", 32), callback)
			case "cycle":
				exec(`UPDATE inbox SET reply_to=? WHERE conv=? AND lid=?`, callback, conv, remote)
				exec(`UPDATE outbox SET reply_to=? WHERE conv=? AND lid=?`, callback, conv, remote)
			case "stopped_parent":
				exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelled, root)
			case "completed_parent":
				exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, root)
			case "cancel_requested_parent":
				exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, root)
			case "missing_local_run":
				// Keep the ordinary sent copy but remove the local running cause
				// from this transaction's proof view. Its outbox state is not a run.
				exec(`UPDATE inbox SET target=NULL WHERE id=?`, root)
			case "missing_parent":
				exec(`UPDATE inbox SET target=NULL WHERE id=?`, root)
				exec(`UPDATE outbox SET target=NULL WHERE conv=? AND lid=?`, conv, root)
				want = verdictWait
			case "revoked_grant":
				exec(`DELETE FROM approvals WHERE address=?`, original.from)
				want = verdictAsk
			case "question_to_task":
				request.Kind = envelope.KindTask
			}
			v, why, err := agentVerdict(tx, request, a.Address, a.Self().Fingerprint(), false, map[string]*partView{})
			if err != nil {
				t.Fatal(err)
			}
			if change != "valid" {
				if v != want {
					t.Fatalf("invalid callback: got verdict %d (%s), want %d", v, why, want)
				}
				return
			}
			if v != verdictRun {
				t.Fatalf("valid callback refused: %s", why)
			}
			owner, err := roomLocalParent(tx, request, a.Address, a.Self().Fingerprint())
			if err != nil || owner != root {
				t.Fatalf("wrong exact local ancestor: %q %v", owner, err)
			}
		})
	}
}
