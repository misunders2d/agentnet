package client

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestRoomReciprocalRootQuestions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell reciprocal harness")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "agentnet")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", cli, "./cmd/agentnet")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, out)
	}
	t.Setenv("RECIP_CLI", cli)
	for _, mode := range []string{"complete", "cancel"} {
		t.Run(mode, func(t *testing.T) { testRoomReciprocalRoots(t, mode) })
	}
}

func testRoomReciprocalRoots(t *testing.T, mode string) {
	dir := t.TempDir()
	t.Setenv("RECIP_DIR", dir)
	const script = `#!/bin/sh
set -e
cat >/dev/null
case " $* " in *" --resume "*|*" --session-id "*) exit 22;; esac
printf '%s\n' "$AGENTNET_ROOM_REQUEST" >> "$RECIP_DIR/runs"
if mkdir "$RECIP_DIR/__SIDE__.root" 2>/dev/null; then
 printf '%s' "$AGENTNET_ROOM_REQUEST" > "$RECIP_DIR/__SIDE__.root-id"
 while [ ! -f "$RECIP_DIR/go" ]; do sleep 0.02; done
 "$RECIP_CLI" --home "$AGENTNET_HOME" room ask --pid "$RECIP___OTHER__" reciprocal-child
else
 printf '%s' "$AGENTNET_ROOM_REQUEST" > "$RECIP_DIR/__SIDE__.child-id"
 while [ ! -f "$RECIP_DIR/release" ]; do sleep 0.02; done
 printf 'done-__SIDE__\n'
fi
`
	for _, x := range []struct{ name, side, other string }{{"reciprocala", "a", "B"}, {"reciprocalb", "b", "A"}} {
		path := filepath.Join(dir, x.name)
		body := strings.NewReplacer("__SIDE__", x.side, "__OTHER__", x.other).Replace(script)
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		Harnesses[x.name] = harness{bin: path, stdin: true, sessions: claudeSessions, question: []string{sessionOneShot}}
		t.Cleanup(func() { delete(Harnesses, x.name) })
	}
	w, _, packet, _ := groupTurnsFixture(t)
	a, conv := w.alice, packet.State.Conv
	var parts []ParticipationInfo
	for _, name := range []string{"reciprocala", "reciprocalb"} {
		entry, err := a.CreateLocalAgent("same display name", Responder{Harness: name, Dir: dir, Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err = a.PublishAgentCatalog(tctx(t)); err != nil {
			t.Fatal(err)
		}
		p, err := a.InviteNamedAgent(tctx(t), conv, a.Address, entry.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "own named participation active", func() bool { return stateAt(t, a, p.PID).Claimable() })
		parts = append(parts, p)
	}
	t.Setenv("RECIP_A", parts[0].PID)
	t.Setenv("RECIP_B", parts[1].PID)
	read := func(name string) string { data, _ := os.ReadFile(filepath.Join(dir, name)); return string(data) }
	touch := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	roots := make([]ConvSent, 2)
	for i, side := range []string{"a", "b"} {
		var err error
		roots[i], err = a.AskAgent(tctx(t), parts[i].PID, envelope.KindQuestion, "independent root "+side)
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "actual root subprocess "+side, func() bool { return read(side+".root-id") == roots[i].ID })
	}
	a.update.Lock()
	a.update.pending = &UpdateRequest{ID: "reciprocal-update", To: "v9.9.9"}
	a.update.Unlock()
	unrelated, err := a.AskAgent(tctx(t), parts[0].PID, envelope.KindQuestion, "unrelated root")
	if err != nil {
		t.Fatal(err)
	}
	touch("go")
	eventually(t, "both reciprocal child subprocesses", func() bool { return read("a.child-id") != "" && read("b.child-id") != "" })
	children := []string{read("a.child-id"), read("b.child-id")}
	childLIDs := make([]string, 2)
	for i, id := range children {
		var reply, pid, session string
		if err := a.store.db.QueryRow(`SELECT coalesce(reply_to,''),pid,coalesce(session,''),coalesce(lid,id) FROM inbox WHERE id=?`, id).Scan(&reply, &pid, &session, &childLIDs[i]); err != nil {
			t.Fatal(err)
		}
		if reply != roots[1-i].LID || pid != parts[i].PID || session != "" {
			t.Fatalf("child lost exact cause or fresh session: %s %s %s", reply, pid, session)
		}
		a.workerLanes.Lock()
		lane, ok := a.workerLanes.jobs[id]
		a.workerLanes.Unlock()
		if !ok || lane.parent != roots[1-i].ID {
			t.Fatal("child ownership moved from causal parent to lender")
		}
		if jobState(t, a, id) != stateRunning || jobState(t, a, roots[i].ID) != stateRunning {
			t.Fatal("root/child ended before both children entered")
		}
	}
	testReciprocalGuards(t, a, roots[0].ID, roots[1].ID, children[1], children[0])
	if jobState(t, a, unrelated.ID) != stateAgentWaiting {
		t.Fatal("unrelated work borrowed a slot")
	}
	if resume, err := a.PauseForAppUpdate(); err == nil {
		resume()
		t.Fatal("update crossed active reciprocal work")
	}
	if a.updatePending() == nil {
		t.Fatal("active children released pending update fence")
	}
	if mode == "cancel" {
		if err := a.Cancel(roots[0].ID); err != nil {
			t.Fatal(err)
		}
		waitState(t, a, roots[0].ID, stateCancelled)
		eventually(t, "causal child cleanup joined", func() bool {
			a.workerLanes.Lock()
			defer a.workerLanes.Unlock()
			_, child := a.workerLanes.jobs[children[1]]
			_, parent := a.workerLanes.jobs[roots[0].ID]
			return !child && !parent
		})
		if jobState(t, a, roots[1].ID) != stateRunning || jobState(t, a, children[0]) != stateRunning {
			t.Fatal("cancellation crossed into independent root")
		}
		if jobState(t, a, unrelated.ID) != stateAgentWaiting || a.updatePending() == nil {
			t.Fatal("cancellation released update fence")
		}
	}
	a.update.Lock()
	a.update.pending = nil
	a.update.Unlock()
	touch("release")
	for i, root := range roots {
		if mode == "cancel" && i == 0 {
			continue
		}
		waitState(t, a, root.ID, stateAnswered)
		if mode != "cancel" {
			waitState(t, a, children[i], stateAnswered)
		}
		eventually(t, "exact root answer", func() bool {
			answer, n := convMsg(t, a, conv, func(m ConvMessage) bool { return m.Kind == envelope.KindAnswer && m.ReplyTo == root.LID })
			return n == 1 && answer.VerifiedAgent && answer.PID == parts[i].PID && strings.Contains(answer.Body, "done-"+[]string{"b", "a"}[i])
		})
	}
	if mode == "cancel" {
		waitState(t, a, children[0], stateAnswered)
		_, n := convMsg(t, a, conv, func(m ConvMessage) bool {
			return m.Kind == envelope.KindAnswer && (m.ReplyTo == roots[0].LID || m.ReplyTo == childLIDs[1])
		})
		if n != 0 {
			t.Fatal("cancelled work published an answer")
		}
	}
	eventually(t, "all reciprocal cleanup joined", a.executorsIdle)
	waitState(t, a, unrelated.ID, stateAnswered)
	for _, id := range []string{roots[0].ID, roots[1].ID, children[0], children[1], unrelated.ID} {
		var attempts int
		if err := a.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, id).Scan(&attempts); err != nil || attempts != 1 {
			t.Fatalf("unexpected attempt count %d: %v", attempts, err)
		}
	}
	starts := strings.Fields(read("runs"))
	if len(starts) != 5 {
		t.Fatalf("unexpected actual subprocesses: %v", starts)
	}
}

func testReciprocalGuards(t *testing.T, a *Agent, parent, lender, child, reverse string) {
	a.workerLanes.Lock()
	defer a.workerLanes.Unlock()
	saved := a.workerLanes.jobs
	defer func() { a.workerLanes.jobs = saved }()
	for _, name := range []string{"running-reverse", "waiting-reverse", "orphan-reverse", "wrong-reverse-owner", "task", "receiver", "missing-reverse", "wrong-cause", "wrong-key", "cancelling-lender", "second-slot"} {
		t.Run(name, func(t *testing.T) {
			tx, e := a.store.db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			edit := func(sql string, args ...any) {
				t.Helper()
				r, e := tx.Exec(sql, args...)
				if e != nil {
					t.Fatal(e)
				}
				n, e := r.RowsAffected()
				if e != nil || n != 1 {
					t.Fatalf("fixture edit %d: %v", n, e)
				}
			}
			a.workerLanes.jobs = map[string]executionLane{}
			for id, lane := range saved {
				if id != child {
					a.workerLanes.jobs[id] = lane
				}
			}
			defer func() { a.workerLanes.jobs = saved }()
			edit(`UPDATE inbox SET state=? WHERE id=?`, stateAgentWaiting, child)
			r, _, e := roomLaneRequest(tx, child)
			if e != nil || r.Target == nil {
				t.Fatalf("child: %v", e)
			}
			j := job{ID: r.ID, From: r.Sender, Key: r.Key, Kind: r.Kind, Conv: r.Conv, PID: r.PID, Local: r.Local, Target: r.Target, AgentID: r.Target.AgentID}
			switch name {
			case "waiting-reverse":
				edit(`UPDATE inbox SET state=? WHERE id=?`, stateAgentWaiting, reverse)
			case "orphan-reverse":
				delete(a.workerLanes.jobs, reverse)
			case "wrong-reverse-owner":
				lane := a.workerLanes.jobs[reverse]
				lane.parent = parent
				a.workerLanes.jobs[reverse] = lane
			case "task":
				j.Kind = envelope.KindTask
			case "receiver":
				j.Receiver = &ReplyReceiverBinding{}
			case "missing-reverse":
				edit(`DELETE FROM inbox WHERE id=?`, reverse)
			case "wrong-cause":
				edit(`UPDATE inbox SET reply_to=? WHERE id=?`, protocol.NewID(), reverse)
			case "wrong-key":
				edit(`UPDATE inbox SET verified_by=? WHERE id=?`, strings.Repeat("f", 32), reverse)
			case "cancelling-lender":
				edit(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, lender)
			case "second-slot":
				a.workerLanes.jobs["extra-slot"] = executionLane{executor: j.AgentID, parent: lender}
			}
			stamp, e := a.reciprocalResolver(nil, parent)(tx, j)
			if name == "running-reverse" || name == "waiting-reverse" {
				if e != nil || stamp == nil || stamp.AgentID != j.AgentID {
					t.Fatalf("valid loan: %+v %v", stamp, e)
				}
			} else if !errors.Is(e, errExecutorBusy) || stamp != nil {
				t.Fatalf("invalid loan: %+v %v", stamp, e)
			}
		})
	}
}
