package client

import (
	"context"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"os"
	"strings"
	"testing"
	"time"
)

func TestContinueRequestDirectWorker(t *testing.T) {
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		t.Run(kind, func(t *testing.T) {
			st := installStub(t, "answer")
			w := newWorld(t, "")
			fakeNotify(w.bob)
			setResponder(t, w.bob, "stubhuman", st.dir, time.Minute)
			if e := w.bob.Approve(w.alice.Address); e != nil {
				t.Fatal(e)
			}
			if _, e := w.bob.sendKey(tctx(t), w.alice.Address); e != nil {
				t.Fatal(e)
			}
			if _, e := w.bob.GrantTasks(w.alice.Address); e != nil {
				t.Fatal(e)
			}
			runWith(t, w, w.bob, RunOptions{})
			runWith(t, w, w.alice, RunOptions{})
			q, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "original bound work", Kind: kind})
			if e != nil {
				t.Fatal(e)
			}
			waitState(t, w.bob, q.ID, stateNeedHuman)
			view, e := w.bob.ContinuationFor(q.ID, "", nil)
			if e != nil || view == nil || view.Attempt != 1 || view.Host != "" {
				t.Fatalf("%+v %v", view, e)
			}
			ordinary, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "ordinary quoted answer", ReplyTo: q.ID})
			if e != nil {
				t.Fatal(e)
			}
			eventually(t, "ordinary quote stored", func() bool {
				var n int
				return w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, ordinary.ID).Scan(&n) == nil && n == 1
			})
			if state, _ := w.bob.store.jobState(q.ID); state != stateNeedHuman || st.count() != 1 {
				t.Fatal("ordinary quote continued work", state, st.count())
			}
			setResponder(t, w.bob, "stub", st.dir, time.Minute)
			token := protocol.NewID()
			if e = w.bob.ContinueRequest(q.ID, token, 1, "use release budget", view.Key); e != nil {
				t.Fatal(e)
			}
			waitState(t, w.bob, q.ID, stateAnswered)
			prompt, e := os.ReadFile(st.log + ".stdin")
			if e != nil {
				t.Fatal(e)
			}
			for _, want := range []string{"original bound work", "which budget applies?", "use release budget", "fresh-context continuation", "do not replay successful effects"} {
				if !strings.Contains(string(prompt), want) {
					t.Fatalf("missing %q in %s", want, prompt)
				}
			}
			for i := 0; i < 3; i++ {
				if e = w.bob.ContinueRequest(q.ID, token, 1, "use release budget", view.Key); e != nil {
					t.Fatal(e)
				}
			}
			if st.count() != 2 {
				t.Fatal("duplicate repeated effects", st.count())
			}
			if e = w.bob.ContinueRequest(q.ID, token, 1, "different answer"); e == nil {
				t.Fatal("token reused for different answer")
			}
			if e = w.bob.ContinueRequest(q.ID, protocol.NewID(), 1, "old attempt"); e == nil {
				t.Fatal("completed request continued")
			}
			var gotKind, body string
			if e = w.bob.store.db.QueryRow(`SELECT kind,body FROM inbox WHERE id=?`, q.ID).Scan(&gotKind, &body); e != nil || gotKind != kind || body != "original bound work" {
				t.Fatal(gotKind, body, e)
			}
			if _, e = w.bob.store.db.Exec(`UPDATE inbox SET body='' WHERE id=?`, q.ID); e != nil {
				t.Fatal(e)
			}
			var saved int
			if e = w.bob.store.db.QueryRow(`SELECT count(*) FROM request_continuations WHERE request=?`, q.ID).Scan(&saved); e != nil || saved != 0 {
				t.Fatal("erased request retained private continuation", saved, e)
			}
		})
	}
}

func TestContinueRequestGroupExactContext(t *testing.T) {
	st := installStub(t, "answer")
	w, _, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	from := p6Member(t, w.alice, w.alice, conv)
	for _, stop := range stops {
		stop()
	}
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		q, e := w.alice.AskAgent(tctx(t), from.PID, kind, "bound group work")
		if e != nil {
			t.Fatal(e)
		}
		var actual string
		if e = w.alice.store.db.QueryRow(`SELECT id FROM inbox WHERE lid=? AND local=1`, q.LID).Scan(&actual); e != nil {
			t.Fatal(e)
		}
		if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,detail='Which branch?' WHERE id=?`, stateNeedHuman, actual); e != nil {
			t.Fatal(e)
		}
		view, e := w.alice.ContinuationFor(q.LID, w.alice.Self().Fingerprint(), nil)
		if e != nil || view == nil || view.ID != actual {
			t.Fatalf("%+v %v", view, e)
		}
		if e = w.alice.ContinueRequest(actual, protocol.NewID(), 2, "stale answer"); e == nil {
			t.Fatal("stale attempt accepted")
		}
		token := protocol.NewID()
		if e = w.alice.ContinueRequest(actual, token, 1, "release branch"); e != nil {
			t.Fatal(e)
		}
		if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=2 WHERE id=?`, stateRunning, actual); e != nil {
			t.Fatal(e)
		}
		prompt, e := w.alice.promptWith(context.Background(), job{ID: actual, From: w.alice.Address, Key: w.alice.Self().Fingerprint(), Kind: kind, Body: "bound group work", Conv: conv, PID: from.PID, Local: true}, &Responder{Harness: "stub", Dir: st.dir}, "")
		if e != nil || !strings.Contains(prompt, "Which branch?") || !strings.Contains(prompt, "release branch") {
			t.Fatalf("%s %v", prompt, e)
		}
		if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelled, actual); e != nil {
			t.Fatal(e)
		}
		if e = w.alice.ContinueRequest(actual, protocol.NewID(), 2, "cannot restart cancelled"); e == nil {
			t.Fatal("cancelled request continued")
		}
	}
}

func TestContinueRequestOwnPhoneDecision(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	if _, e := w.alice.CreatePerson(tctx(t), "Alice"); e != nil {
		t.Fatal(e)
	}
	runWith(t, w, w.alice, RunOptions{})
	phone := linkedVia(t, w.alice, "phone", func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) })
	q, e := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "original phone question"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "host stores phone question", func() bool { _, e := w.alice.store.jobState(q.ID); return e == nil })
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,detail='Which budget?' WHERE id=?`, stateNeedHuman, q.ID); e != nil {
		t.Fatal(e)
	}
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	descriptor, e := phone.ContinuationFor(q.ID, phone.Self().Fingerprint(), &ExecView{Host: w.alice.Address, State: stateNeedHuman, Attempt: 1})
	if e != nil || descriptor == nil || descriptor.Host != w.alice.Address {
		t.Fatalf("own phone descriptor %+v %v", descriptor, e)
	}
	// Signed control admission, no operator grant and no report; exact own human key.
	body, _ := json.Marshal(envelope.Decision{Action: "continue", Expect: stateNeedHuman, Attempt: 1, Text: "release budget"})
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: phone.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubDecision, Body: string(body), Ref: &envelope.Ref{ID: q.ID, Fingerprint: phone.Self().Fingerprint()}}
	recipient, _ := w.alice.Self().Recipient()
	env, e := envelope.Seal(in, phone.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	sent, e := phone.Decide(WithQueuedSend(tctx(t), in.ID), w.alice.Address, q.ID, phone.Self().Fingerprint(), "continue", stateNeedHuman, 1, "release budget", "")
	if e != nil || sent.ID != in.ID {
		t.Fatalf("queued own continuation %+v %v", sent, e)
	}
	waitState(t, w.alice, q.ID, stateAnswered)
	env, e = phone.store.outboxEnvelope(in.ID)
	if e != nil {
		t.Fatal(e)
	}
	in, e = envelope.Open(env, w.alice.id, w.alice.Address, phone.Self())
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.admitDecision(tctx(t), env, in, phone.Self()); e != nil {
		t.Fatal(e)
	}
	if st.count() != 1 {
		t.Fatal("decision replay reran effects", st.count())
	}
	// Current foreign key cannot apply a continuation, even knowing every binding.
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=2,detail='Second clarification' WHERE id=?`, stateNeedHuman, q.ID); e != nil {
		t.Fatal(e)
	}
	in.ID = protocol.NewID()
	in.From = w.bob.Address
	body, _ = json.Marshal(envelope.Decision{Action: "continue", Expect: stateNeedHuman, Attempt: 2, Text: "foreign answer"})
	in.Body = string(body)
	env, e = envelope.Seal(in, w.bob.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.admitDecision(tctx(t), env, in, w.bob.Self()); e != nil {
		t.Fatal(e)
	}
	if state, _ := w.alice.store.jobState(q.ID); state != stateNeedHuman {
		t.Fatal("foreign decision applied", state)
	}

	agentHost := linkedVia(t, w.alice, "worker", w.alice.ApproveAgentLink)
	in.ID = protocol.NewID()
	in.From = agentHost.Address
	env, e = envelope.Seal(in, agentHost.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.admitDecision(tctx(t), env, in, agentHost.Self()); e != nil {
		t.Fatal(e)
	}
	if state, _ := w.alice.store.jobState(q.ID); state != stateNeedHuman {
		t.Fatal("own agent-host made human continuation", state)
	}
	if e = w.alice.RemoveDevice(tctx(t), phone.Address); e != nil {
		t.Fatal(e)
	}
	in.ID = protocol.NewID()
	in.From = phone.Address
	env, e = envelope.Seal(in, phone.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.admitDecision(tctx(t), env, in, phone.Self()); e != nil {
		t.Fatal(e)
	}
	if state, _ := w.alice.store.jobState(q.ID); state != stateNeedHuman {
		t.Fatal("removed own phone continued request", state)
	}
}

func TestContinueRequestCodexSavedBackgroundStaysFresh(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	script, e := os.ReadFile(Harnesses["xstyle"].bin)
	if e != nil {
		t.Fatal(e)
	}
	script = []byte(strings.Replace(string(script), "cat > /dev/null", `cat > "$STUB_LOG.stdin"`, 1))
	if e = os.WriteFile(Harnesses["xstyle"].bin, script, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("STUB_NO_AGENT", "1")
	t.Setenv("STUB_O", needsHumanMarker+"\nWhich branch?")
	t.Setenv("STUB_THREAD", "prior-worker-thread")
	setResponder(t, w.bob, "xstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "original question"})
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, w.bob, q.ID, stateNeedHuman)
	old := sessionOf(t, w.bob, q.ID)
	if old == nil || old.ID != "prior-worker-thread" {
		t.Fatal(old)
	}
	t.Setenv("STUB_NO_AGENT", "")
	t.Setenv("STUB_THREAD", "new-worker-thread")
	if e = w.bob.ContinueRequest(q.ID, protocol.NewID(), 1, "release branch"); e != nil {
		t.Fatal(e)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	next := sessionOf(t, w.bob, q.ID)
	if next == nil || next.ID != "new-worker-thread" {
		t.Fatal(next)
	}
	runs := st.runs()
	if len(runs) != 2 || strings.Contains(runs[1], "resume") || strings.Contains(runs[1], "--sandbox") || strings.Contains(runs[1], "approval_policy") {
		t.Fatal(runs)
	}
	prompt, e := os.ReadFile(st.log + ".stdin")
	if e != nil || !strings.Contains(string(prompt), "fresh-context continuation") || !strings.Contains(string(prompt), "release branch") {
		t.Fatalf("%s %v", prompt, e)
	}
}

func TestContinueRequestQueuedCapabilityDowngrade(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.bob, RunOptions{})
	key, e := w.alice.sendKey(tctx(t), w.bob.Address)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(envelope.Decision{Action: "continue", Expect: stateNeedHuman, Attempt: 1, Text: "answer"})
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubDecision, Body: string(body), Ref: &envelope.Ref{ID: protocol.NewID(), Fingerprint: w.alice.Self().Fingerprint()}}
	recipient, _ := key.Recipient()
	env, e := envelope.Seal(in, w.alice.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.store.addContinuationOutbox(env, in, key.Fingerprint()); e != nil {
		t.Fatal(e)
	}
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapContinuation))
	if _, e = w.alice.deliver(tctx(t), env, nil); e == nil {
		t.Fatal("continuation reached unsupported host")
	}
	if inboxHas(t, w.bob, env.ID) {
		t.Fatal("old host admitted unsupported continuation")
	}
	var held int
	if e = w.bob.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, env.ID).Scan(&held); e != nil || held != 0 {
		t.Fatal("unsupported control reached old reader quarantine", held, e)
	}
	if e = w.alice.store.addContinuationOutbox(env, in, strings.Repeat("0", 64)); e == nil {
		t.Fatal("captured reader key changed on retry")
	}
}
