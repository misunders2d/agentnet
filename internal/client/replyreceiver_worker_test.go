package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestManagedReceiverClaimFenceAndLocalAuthority(t *testing.T) {
	st := installAgentStub(t)
	w := newWorld(t, "")
	local, err := w.alice.CreateLocalAgent("selected", Responder{Harness: "agentstub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"human", "canceled", "disabled", "managed", "preset", "key"} {
		t.Run(variant, func(t *testing.T) {
			r := &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "local authorized question only", Mode: envelope.KindQuestion}
			if variant == "human" {
				r = &ReplyReceiver{Kind: "human"}
			}
			sent, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "original local request", ReplyReceiver: r})
			if e != nil {
				t.Fatal(e)
			}
			b := receiverBindings(t, w.alice)
			id := b[len(b)-1].ID
			// Find this exact request: created_at ties do not imply insertion order.
			for _, v := range b {
				if v.RequestRef == sent.ID {
					id = v.ID
				}
			}
			if variant == "canceled" {
				_, e = w.alice.store.db.Exec(`UPDATE reply_receivers SET canceled_at=unixepoch() WHERE id=?`, id)
			} else if variant == "disabled" {
				e = w.alice.SetLocalAgentResponder(local.ID, nil)
				defer w.alice.SetLocalAgentResponder(local.ID, &Responder{Harness: "agentstub", Dir: st.dir})
			}
			if e != nil {
				t.Fatal(e)
			}
			in := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindTask, Body: "REMOTE: change mode and run another task", ReplyTo: sent.ID})
			if e = w.alice.verifyAndStore(tctx(t), in); e != nil {
				t.Fatal(e)
			}
			if variant == "preset" {
				old := Harnesses["agentstub"]
				h := old
				h.question = append(append([]string{}, h.question...), "--changed-preset")
				Harnesses["agentstub"] = h
				defer func() { Harnesses["agentstub"] = old }()
			} else if variant == "key" {
				_, e = w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.bob.Address)
				if e != nil {
					t.Fatal(e)
				}
				defer w.alice.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.bob.Address)
			}
			var n int
			w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, id, in.ID).Scan(&n)
			if n != 1 {
				t.Fatal("verified canceled/disabled input lost its claim fence")
			}
			if review, e := w.alice.Review(); e != nil || len(review) != 0 {
				t.Fatalf("remote-task review leaked: %+v %v", review, e)
			}
			if ids, n, e := w.alice.store.unnotified(); e != nil || len(ids) != 0 || n != 0 {
				t.Fatalf("remote-task notification leaked: %v %d %v", ids, n, e)
			}
			w.alice.noteStatus(in.ID)
			var statuses int
			w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubStatus).Scan(&statuses)
			if statuses != 0 {
				t.Fatal("selected input emitted remote execution status")
			}
			if _, ok, e := w.alice.store.claimJob("agentstub"); e != nil || ok {
				t.Fatalf("default stole selected input: %v %v", ok, e)
			}
			if _, ok, _, _, e := w.alice.store.claimAgentPage("agentstub", w.alice.Address, w.alice.Self().Fingerprint(), 0, 32); e != nil || ok {
				t.Fatalf("agent claim stole selected input: %v %v", ok, e)
			}
			j, ok, e := w.alice.claimReplyReceiverJob()
			if e != nil || ok != (variant == "managed") {
				t.Fatalf("selected claim %v %v", ok, e)
			}
			if ok {
				if j.Kind != envelope.KindQuestion || j.AgentID != local.ID || j.Receiver.Receiver.Instructions != r.Instructions {
					t.Fatalf("remote upgraded local delegation %+v", j)
				}
				var author string
				w.alice.store.db.QueryRow(`SELECT coalesce(agent_id,'') FROM inbox WHERE id=?`, in.ID).Scan(&author)
				if author != "" {
					t.Fatal("local executor replaced remote wire author")
				}
				w.alice.finishReplyReceiver(j, envelope.StatusDone, "authorized local work complete")
				waitState(t, w.alice, in.ID, stateContinued)
			}
		})
	}
	if st.runs() != 0 {
		t.Fatal("claim-only checks launched a model")
	}
	independent := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindTask, Body: "independent remote task requires approval"})
	if err = w.alice.verifyAndStore(tctx(t), independent); err != nil {
		t.Fatal(err)
	}
	threads, err := w.alice.Threads()
	if err != nil {
		t.Fatal(err)
	}
	review, running, unread := 0, 0, 0
	for _, v := range threads {
		review += v.Review
		running += v.Running
		unread += v.Unread
	}
	if review != 1 || running != 0 || unread != 7 {
		t.Fatalf("thread projection lost data or mislabeled selected input: review%d running%d unread%d", review, running, unread)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, independent.ID); err != nil {
		t.Fatal(err)
	}
	threads, err = w.alice.Threads()
	if err != nil {
		t.Fatal(err)
	}
	review, running = 0, 0
	for _, v := range threads {
		review += v.Review
		running += v.Running
	}
	if review != 0 || running != 1 {
		t.Fatalf("independent remote execution projection changed: %d %d", review, running)
	}
}

func TestManagedReceiverOfflineRestartDispatchOnce(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	local, e := w.alice.CreateLocalAgent("selected", Responder{Harness: "stub", Dir: st.dir, Timeout: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	setResponder(t, w.alice, "stub2", st.dir, time.Minute)
	r := &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "original authorized question", Mode: envelope.KindQuestion}
	sent, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "original request", ReplyReceiver: r})
	if e != nil {
		t.Fatal(e)
	}
	runAgent(t, w.bob)
	eventually(t, "remote got original request", func() bool {
		var n int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, sent.ID).Scan(&n)
		return n == 1
	})
	reply, e := w.bob.Reply(tctx(t), sent.ID, "data while selected home offline")
	if e != nil {
		t.Fatal(e)
	}
	if st.count() != 0 {
		t.Fatal("offline home ran a worker")
	}
	home := w.alice.home
	w.alice.Close()
	a, e := Open(home)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	runAgent(t, a)
	waitState(t, a, reply.ID, stateContinued)
	sealed, e := w.bob.store.outboxEnvelope(reply.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.verifyAndStore(tctx(t), sealed); e != nil {
		t.Fatal(e)
	}
	if _, ok, e := a.claimReplyReceiverJob(); e != nil || ok {
		t.Fatalf("replay claimed %v %v", ok, e)
	}
	if st.count() != 1 {
		t.Fatalf("offline/restart continuation runs%d", st.count())
	}
	raw, _ := os.ReadFile(st.log)
	if !strings.Contains(string(raw), "args=--question-mode") || strings.Contains(string(raw), "--second") {
		t.Fatalf("default or mode selected incorrectly: %s", raw)
	}
}

func TestManagedReceiverRunningCancelAndDisable(t *testing.T) {
	for _, variant := range []string{"cancel", "disabled"} {
		t.Run(variant, func(t *testing.T) {
			st := installStub(t, "sleep")
			w := newWorld(t, "")
			local, e := w.alice.CreateLocalAgent("selected", Responder{Harness: "stub", Dir: st.dir, Timeout: time.Minute})
			if e != nil {
				t.Fatal(e)
			}
			setResponder(t, w.alice, "stub2", st.dir, time.Minute)
			r := &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "authorized local task", Mode: envelope.KindTask}
			sent, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "request", ReplyReceiver: r})
			if e != nil {
				t.Fatal(e)
			}
			input := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindTask, Body: "correlated data", ReplyTo: sent.ID})
			if e = w.alice.verifyAndStore(tctx(t), input); e != nil {
				t.Fatal(e)
			}
			runAgent(t, w.alice)
			waitState(t, w.alice, input.ID, stateRunning)
			eventually(t, "selected native child running", func() bool { _, e := os.Stat(st.log + ".child"); return e == nil })
			want := stateCancelled
			if variant == "cancel" {
				e = w.alice.Cancel(input.ID)
			} else {
				e = w.alice.SetLocalAgentResponder(local.ID, nil)
			}
			if e != nil {
				t.Fatal(e)
			}
			waitState(t, w.alice, input.ID, want)
			rows := receiverBindings(t, w.alice)
			if len(rows) != 1 || len(rows[0].Inputs) != 1 || rows[0].Inputs[0].State != "accepted" || rows[0].Inputs[0].Detail == "" {
				t.Fatalf("uncertain canceled input completed or lost reason: %+v", rows)
			}
			if e = w.alice.verifyAndStore(tctx(t), input); e != nil {
				t.Fatal(e)
			}
			if _, ok, e := w.alice.claimReplyReceiverJob(); e != nil || ok {
				t.Fatalf("stopped input rerun %v %v", ok, e)
			}
			if st.count() != 1 {
				t.Fatalf("stopped worker or default ran%d", st.count())
			}
		})
	}
}

func TestManagedReceiverFollowupRollbackAndUncertainRestart(t *testing.T) {
	st := installAgentStub(t)
	w := newWorld(t, "")
	local, e := w.alice.CreateLocalAgent("selected", Responder{Harness: "agentstub", Dir: st.dir})
	if e != nil {
		t.Fatal(e)
	}
	r := &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "only original local work", Mode: envelope.KindTask}
	sent, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "request", ReplyReceiver: r})
	if e != nil {
		t.Fatal(e)
	}
	b := receiverBindings(t, w.alice)[0]
	input := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindQuestion, ReplyTo: sent.ID, Body: "clarification data"})
	if e = w.alice.verifyAndStore(tctx(t), input); e != nil {
		t.Fatal(e)
	}
	selected, e := w.alice.ReplyReceiverForBinding(b.ID)
	if e != nil {
		t.Fatal(e)
	}
	selected.Mode = envelope.KindQuestion
	before := count(t, w.alice, "outbox")
	if _, e = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "attempt mode upgrade", ReplyReceiver: selected}); e == nil {
		t.Fatal("existing delegation altered")
	}
	if count(t, w.alice, "outbox") != before {
		t.Fatal("refused followup became deliverable")
	}
	selected, e = w.alice.ReplyReceiverForBinding(b.ID)
	if e != nil {
		t.Fatal(e)
	}
	j, ok, e := w.alice.claimReplyReceiverJob()
	if e != nil || !ok {
		t.Fatalf("claim %v %v", ok, e)
	}
	if _, e = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "bound follow-up", ReplyTo: input.ID, ReplyReceiver: selected}); e != nil {
		t.Fatal(e)
	}
	w.alice.finishReplyReceiver(j, envelope.StatusDone, "asked authorized followup")
	if got := receiverBindings(t, w.alice); len(got) != 1 || got[0].State != "pending" || !strings.Contains(got[0].Detail, "awaiting") {
		t.Fatalf("unanswered followup falsely complete: %+v", got)
	}
	var followup string
	w.alice.store.db.QueryRow(`SELECT id FROM outbox WHERE reply_receiver=? AND id!=?`, b.ID, sent.ID).Scan(&followup)
	final := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindAnswer, ReplyTo: followup, Body: "final data"})
	if e = w.alice.verifyAndStore(tctx(t), final); e != nil {
		t.Fatal(e)
	}
	if _, ok, e = w.alice.claimReplyReceiverJob(); e != nil || !ok {
		t.Fatalf("final claim %v %v", ok, e)
	}
	// Crash after claim: existing inbox recovery must never repeat uncertain effects.
	if e = w.alice.store.interruptRunning(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(w.alice.home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if e = reopened.verifyAndStore(tctx(t), final); e != nil {
		t.Fatal(e)
	}
	if _, ok, e = reopened.claimReplyReceiverJob(); e != nil || ok {
		t.Fatalf("uncertain continuation rerun %v %v", ok, e)
	}
	if _, ok, e = reopened.store.claimJob("agentstub"); e != nil || ok {
		t.Fatalf("default stole interrupted input %v %v", ok, e)
	}
	got := receiverBindings(t, reopened)
	if len(got) != 1 || got[0].State != "refused" || len(got[0].Inputs) != 2 || got[0].Inputs[1].State != "accepted" || !strings.Contains(got[0].Inputs[1].Detail, "daemon stopped") {
		t.Fatalf("uncertain input falsely complete: %+v", got)
	}
	late := receiverDirect(t, w.bob, reopened, envelope.Inner{Kind: envelope.KindAnswer, ReplyTo: followup, Body: "new data after uncertain local effects"})
	if e = reopened.verifyAndStore(tctx(t), late); e != nil {
		t.Fatal(e)
	}
	if _, ok, e = reopened.claimReplyReceiverJob(); e != nil || ok {
		t.Fatalf("later reply repeated uncertain original work %v %v", ok, e)
	}
	if _, ok, e = reopened.store.claimJob("agentstub"); e != nil || ok {
		t.Fatalf("late uncertain input went to default %v %v", ok, e)
	}
}

// This fixture executes the real installed-harness command shape on a private
// PATH. It never invokes a vendor/model and never writes a real harness home.
const managedReceiverScript = `#!/usr/bin/env python3
import os,sys,re,json,subprocess,hashlib
from pathlib import Path
p=sys.stdin.read(); d=Path.cwd(); name=d.name
with (d/'runs').open('a') as f: f.write(json.dumps({'args':sys.argv[1:],'binding':os.getenv('AGENTNET_REPLY_BINDING'),'prompt':p})+'\n')
if name=='remote':
 assert 'FOLLOW_UP_A' in p
 print('FINAL_DATA_A: approved answer for original plan'); sys.exit(0)
if name=='default':
 raise AssertionError('default must never receive selected input')
assert '--permission-mode' not in sys.argv, 'explicit local task mode lost'
assert os.getenv('AGENTNET_REPLY_BINDING')
assert 'Original LOCAL continuation instructions (authority)' in p
if name=='selectedB':
 assert 'CREATE_REPORT_B' in p and 'CREATE_REPORT_A' not in p
 (d/'final').write_text('authorized B result'); print('B local work complete');sys.exit(0)
assert 'CREATE_REPORT_A' in p and 'CREATE_REPORT_B' not in p
matches=re.findall(r'Verified attachment "payload.txt" .*?SHA256 ([0-9a-f]+)\) is at ("[^"]+")',p)
assert matches, 'verified attached bytes absent'
for digest,path in matches:
 data=Path(json.loads(path)).read_bytes()
 assert data==b'verified original work input\n' and hashlib.sha256(data).hexdigest()==digest
if 'FINAL_DATA_A' not in p:
 assert not (d/'step').exists(), 'initial local effect repeated'
 (d/'step').write_text('verified attached bytes consumed')
 ids=re.findall(r'## Authorized AgentNet in ([0-9a-f]+)',p)
 assert ids
 subprocess.run(['agentnet','ask','--wait','0','--reply-to',ids[-1],os.environ['RECEIVER_REMOTE'],'FOLLOW_UP_A'],check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
 print('follow-up queued under original local delegation')
else:
 assert (d/'step').read_text()=='verified attached bytes consumed'
 (d/'final').write_text('actual authorized result A from verified bytes and final reply')
 print('A local work complete')
`

func TestManagedReceiverDaemonContinuesAfterCLIExit(t *testing.T) {
	bin := os.Getenv("AGENTNET_TEST_BIN")
	if bin == "" {
		t.Skip("requires frozen synthetic agentnet binary")
	}
	w := newWorld(t, "")
	tools := t.TempDir()
	if e := os.WriteFile(filepath.Join(tools, "claude"), []byte(managedReceiverScript), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(bin, filepath.Join(tools, "agentnet")); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RECEIVER_REMOTE", w.bob.Address)
	work := t.TempDir()
	dirs := map[string]string{}
	for _, name := range []string{"selectedA", "selectedB", "default", "remote"} {
		dirs[name] = filepath.Join(work, name)
		if e := os.Mkdir(dirs[name], 0700); e != nil {
			t.Fatal(e)
		}
	}
	a, e := w.alice.CreateLocalAgent("selected A", Responder{Harness: "claude", Dir: dirs["selectedA"], Timeout: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	b, e := w.alice.CreateLocalAgent("selected B", Responder{Harness: "claude", Dir: dirs["selectedB"], Timeout: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	setResponder(t, w.alice, "claude", dirs["default"], time.Minute)
	if e = w.bob.Approve(w.alice.Address); e != nil {
		t.Fatal(e)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	sendCLI := func(kind, receiver, instructions string) string {
		t.Helper()
		args := []string{"--home", w.alice.home, kind, "--wait", "0", "--reply-receiver", receiver}
		if receiver != "human" {
			args = append(args, "--continue", instructions, "--continue-mode", "task")
		}
		args = append(args, w.bob.Address, "original request "+instructions)
		cmd := exec.Command(bin, args...)
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("originating CLI %v: %s", e, out)
		}
		id := strings.Fields(string(out))[0]
		if !protocol.ValidID(id) {
			t.Fatalf("no request ID %q", out)
		}
		eventually(t, "remote stored originating process request", func() bool {
			var n int
			w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, id).Scan(&n)
			return n == 1
		})
		return id
	}
	id := sendCLI("ask", a.ID, "CREATE_REPORT_A") // originating process has exited here
	payload := filepath.Join(t.TempDir(), "payload.txt")
	os.WriteFile(payload, []byte("verified original work input\n"), 0600)
	reply, e := w.bob.Reply(tctx(t), id, "FIRST_DATA_A: clarification for original plan", payload)
	if e != nil {
		t.Fatal(e)
	}
	setResponder(t, w.bob, "claude", dirs["remote"], time.Minute)
	eventually(t, "selected A performs actual multi-step local work", func() bool { _, e := os.Stat(filepath.Join(dirs["selectedA"], "final")); return e == nil })
	eventually(t, "selected A completed inputs", func() bool {
		rows, e := w.alice.ReplyReceiverBindings()
		return e == nil && len(rows) == 1 && rows[0].State == "completed" && len(rows[0].Inputs) == 2
	})
	var raw string
	w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, reply.ID).Scan(&raw)
	var duplicate envelope.Envelope
	if e = json.Unmarshal([]byte(raw), &duplicate); e != nil {
		t.Fatal(e)
	}
	if e = w.alice.verifyAndStore(tctx(t), duplicate); e != nil {
		t.Fatal(e)
	}
	idB := sendCLI("task", b.ID, "CREATE_REPORT_B")
	if _, e = w.bob.Reply(tctx(t), idB, "B_DATA"); e != nil {
		t.Fatal(e)
	}
	eventually(t, "selected B isolated work", func() bool { _, e := os.Stat(filepath.Join(dirs["selectedB"], "final")); return e == nil })
	human := sendCLI("task", "human", "")
	if _, e = w.bob.Reply(tctx(t), human, "HUMAN_DATA"); e != nil {
		t.Fatal(e)
	}
	eventually(t, "human binding input retained", func() bool {
		rows, e := w.alice.ReplyReceiverBindings()
		if e != nil {
			return false
		}
		for _, v := range rows {
			if v.Receiver.Kind == "human" {
				return len(v.Inputs) == 1 && v.State == "pending"
			}
		}
		return false
	})
	readRuns := func(name string) []map[string]any {
		t.Helper()
		raw, _ := os.ReadFile(filepath.Join(dirs[name], "runs"))
		var rows []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line == "" {
				continue
			}
			var r map[string]any
			if e := json.Unmarshal([]byte(line), &r); e != nil {
				t.Fatal(e)
			}
			rows = append(rows, r)
		}
		return rows
	}
	if len(readRuns("default")) != 0 || len(readRuns("selectedA")) != 2 || len(readRuns("selectedB")) != 1 || len(readRuns("remote")) != 1 {
		t.Fatal("duplicate/default/cross-binding execution")
	}
	aRuns := readRuns("selectedA")
	args1 := aRuns[0]["args"].([]any)
	args2 := aRuns[1]["args"].([]any)
	findArg := func(args []any, flag string) string {
		for i, v := range args {
			if v == flag && i+1 < len(args) {
				return args[i+1].(string)
			}
		}
		return ""
	}
	if first, resume := findArg(args1, "--session-id"), findArg(args2, "--resume"); first == "" || first != resume {
		t.Fatalf("binding managed session not resumed: %v %v", args1, args2)
	}
	bRuns := readRuns("selectedB")
	if findArg(bRuns[0]["args"].([]any), "--session-id") == findArg(args1, "--session-id") {
		t.Fatal("different bindings shared session")
	}
	eventually(t, "private verified attachment staging cleaned", func() bool {
		paths, _ := filepath.Glob(filepath.Join(w.alice.home, "opened", ".agentnet-apx-*"))
		return len(paths) == 0
	})
}
