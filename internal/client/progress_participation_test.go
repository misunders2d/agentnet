package client

import (
	"os"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An outside participation's progress is its nonterminal output: exact host,
// agent, PID and request, in the same conversation, waiting for prg1 and the
// participation capability across a restart, and fenced by the end.
func TestProgressParticipationAuthorityCapsAndEnd(t *testing.T) {
	w, host, conv, _, stub, records, stopHost := externalAgentWorld(t)
	stub.mode("sleep")
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invite at host", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		eventually(t, "active at "+a.Address, func() bool { return stateAt(t, a, p.PID).Claimable() })
	}
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "work slowly")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "host runs the request", func() bool { _, err := os.Stat(stub.log + ".started"); return err == nil })

	sent, err := host.SendProgress(tctx(t), w.alice.Address, q.ID, "EXTERNAL PROGRESS", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		eventually(t, "progress output at "+a.Address, func() bool {
			return inboxCount(t, a, "conv = ? AND pid = ? AND agent_id = ? AND status = ? AND reply_to = ? AND sender = ? AND body = ?",
				conv, p.PID, records[0].ID, envelope.StatusProgress, q.ID, host.Address, "EXTERNAL PROGRESS") == 1
		})
	}
	if s, _ := host.store.jobState(q.ID); s != stateRunning {
		t.Fatalf("progress ended the run: %s", s)
	}
	if _, err = host.SendProgress(tctx(t), w.bob.Address, q.ID, "wrong requester", 0, false); err == nil {
		t.Fatal("progress to another requester")
	}

	// Wrong agent, PID or request: held at the member, never stored.
	_, root, _, _ := host.store.conversation(conv)
	m, err := host.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := w.bob.Self().Recipient()
	forge := func(agentID, pid, replyTo string) string {
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: host.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Body: "forged", ReplyTo: replyTo, Conv: conv, LID: protocol.NewID(), Root: root, PID: pid, Origin: envelope.OriginAgentPrefix + "agentstub",
			Emotion: "neutral", Status: envelope.StatusProgress, AgentID: agentID}
		for _, mem := range m.root.Members {
			in.Fan = append(in.Fan, envelope.Fan{Person: mem.Person, Roster: m.persons[mem.Person].info.Roster})
		}
		env, err := envelope.Seal(in, host.id.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.bob.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	for _, id := range []string{forge(records[1].ID, p.PID, q.ID), forge(records[0].ID, protocol.NewID(), q.ID), forge(records[0].ID, p.PID, protocol.NewID())} {
		if inboxCount(t, w.bob, "id = ?", id) != 0 {
			t.Fatal("progress with wrong provenance stored")
		}
	}

	// Bob reads neither prg1 nor, then, the outside participation cap: each
	// copy to bob waits; alice's copies go.
	signCapsNow(t, w.bob, without(ownCaps, protocol.CapProgress))
	noProgress, err := host.SendProgress(tctx(t), w.alice.Address, q.ID, "BOB LACKS PRG1", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	signCapsNow(t, w.bob, without(ownCaps, protocol.CapExternalParticipation))
	noOutside, err := host.SendProgress(tctx(t), w.alice.Address, q.ID, "BOB LACKS APX1", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	waitingAt := func(a *Agent, body string) int {
		return func() int {
			var n int
			a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND body=? AND state=?`, w.bob.Address, body, stateConvWaiting).Scan(&n)
			return n
		}()
	}
	if waitingAt(host, "BOB LACKS PRG1") != 1 || waitingAt(host, "BOB LACKS APX1") != 1 {
		t.Fatalf("copies to bob not held: %+v %+v", noProgress, noOutside)
	}
	eventually(t, "alice still gets both", func() bool {
		return inboxCount(t, w.alice, "body IN ('BOB LACKS PRG1','BOB LACKS APX1') AND status = ?", envelope.StatusProgress) == 2
	})

	// Host restart; both capabilities back release each held copy once.
	stopHost()
	home := host.home
	host.Close()
	host, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	signCapsNow(t, w.bob, ownCaps)
	feats, err := host.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	host.releaseConv(tctx(t), feats)
	if err = host.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "held progress released to bob", func() bool {
		return inboxCount(t, w.bob, "body IN ('BOB LACKS PRG1','BOB LACKS APX1') AND status = ? AND pid = ?", envelope.StatusProgress, p.PID) == 2
	})
	host.releaseConv(tctx(t), feats)
	host.FlushOutbox(tctx(t))
	time.Sleep(200 * time.Millisecond)
	if n := inboxCount(t, w.bob, "body IN ('BOB LACKS PRG1','BOB LACKS APX1')"); n != 2 {
		t.Fatalf("released copies %d, want 2", n)
	}

	// A queued update after the participation ends is held, never sent; a
	// new one is refused.
	f := injectFaults(host)
	f.add("GET", "/profile", 4, false)
	late, err := host.SendProgress(tctx(t), w.alice.Address, q.ID, "LATE PROGRESS", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	runAgent(t, host)
	eventually(t, "end at host", func() bool { return stateAt(t, host, p.PID).State == PartDismissed })
	host.holdEndedOutputs("")
	var lateStates []string
	rows, _ := host.store.db.Query(`SELECT state FROM outbox WHERE body='LATE PROGRESS'`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		lateStates = append(lateStates, s)
	}
	rows.Close()
	for _, s := range lateStates {
		if s == stateQueued || s == stateConvWaiting {
			t.Fatalf("late progress still pending after end: %v (%+v)", lateStates, late)
		}
	}
	if inboxCount(t, w.alice, "body = 'LATE PROGRESS'")+inboxCount(t, w.bob, "body = 'LATE PROGRESS'") != 0 {
		t.Fatal("progress sent after the end")
	}
	if _, err = host.SendProgress(tctx(t), w.alice.Address, q.ID, "AFTER END", 0, false); err == nil {
		t.Fatal("progress after the end")
	}
	if s, _, _, _ := host.store.outboxState(sent.ID); s == "" {
		t.Fatal("first progress copy not recorded")
	}
}

// A group visitor agent's progress reaches the group as its nonterminal
// output with exact PID and agent while the request still runs.
func TestProgressGroupParticipationOutput(t *testing.T) {
	stub := installAgentStub(t)
	stub.mode("sleep")
	w, producer, packet, stops := groupTurnsFixture(t)
	host := proofReader(t, w, "progress-visitor")
	runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	fakeNotify(host)
	record, err := host.CreateLocalAgent("Progress reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	p, err := producer.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor invitation", func() bool { v, e := host.Participation(p.PID); return e == nil && v.State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor active", func() bool { v, e := producer.Participation(p.PID); return e == nil && v.Claimable() })
	q, err := producer.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "group work slowly")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor runs", func() bool { _, err := os.Stat(stub.log + ".started"); return err == nil })
	if _, err = host.SendProgress(tctx(t), producer.Address, q.ID, "GROUP PROGRESS", 0, false); err != nil {
		t.Fatal(err)
	}
	members := 0
	for a := range stops {
		if a == host {
			continue
		}
		members++
		eventually(t, "group progress at "+a.Address, func() bool {
			return inboxCount(t, a, "conv = ? AND pid = ? AND agent_id = ? AND status = ? AND reply_to = ? AND body = ?",
				conv, p.PID, record.ID, envelope.StatusProgress, q.ID, "GROUP PROGRESS") == 1
		})
	}
	if members == 0 {
		t.Fatal("fixture has no members")
	}
	if s, _ := host.store.jobState(q.ID); s != stateRunning {
		t.Fatalf("group progress ended the run: %s", s)
	}
}
