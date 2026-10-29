package client

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// MEL-713: a terminal agent's request lands in the person's "My agents"
// room, in its session's topic, posted as that agent, never as the person.
func TestAgentRoomAskPostsAsAgentInSessionTopic(t *testing.T) {
	stub := installAgentStub(t)
	Harnesses["roomasker"] = Harnesses["agentstub"] // one enabled agent per harness per device
	t.Cleanup(func() { delete(Harnesses, "roomasker") })
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	// A participation event needs another device to carry it; a person with
	// one device cannot use the room yet (recorded in docs/plans/MEL_713.md).
	phone := linked(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	publishGroupFixtureCaps(t, phone, true)
	a := w.alice
	asker, err := a.CreateLocalAgent("asker", Responder{Harness: "roomasker", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := a.CreateLocalAgent("helper", Responder{Harness: "agentstub", Dir: stub.dir})
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
	ask := AgentRoomAsk{From: from, SessionKey: "agentstub/session-1", Host: a.Address, AgentID: helper.ID, Kind: envelope.KindQuestion, Body: "which branch holds the fix?"}
	sent, err := a.SendAgentRoomAsk(tctx(t), ask)
	if err != nil {
		t.Fatal(err)
	}
	room, err := a.MyAgentsRoom(tctx(t))
	if err != nil || room != sent.Conv {
		t.Fatalf("request outside the one My agents room: %s vs %s %v", sent.Conv, room, err)
	}
	if m, err := a.dmMembers(room); err != nil || m.group == nil || m.group.State.Title != MyAgentsRoomTitle {
		t.Fatalf("My agents room is not the person's group: %+v %v", m.group, err)
	}
	for _, pid := range []string{sent.From, sent.To} {
		p := stateAt(t, a, pid)
		if p.Topic == nil || *p.Topic != sent.Topic || !p.Claimable() {
			t.Fatalf("agent not active in exactly the job topic: %+v", p)
		}
	}
	msgs, err := a.ConversationMessages(room)
	if err != nil {
		t.Fatal(err)
	}
	var got *ConvMessage
	for i := range msgs {
		if msgs[i].LID == sent.LID {
			got = &msgs[i]
		}
	}
	if got == nil || got.Kind != envelope.KindQuestion || got.PID != sent.To || !strings.HasPrefix(got.Origin, envelope.OriginAgentPrefix) {
		t.Fatalf("request not posted as the asking agent to the helper: %+v", got)
	}
	if got.Human == nil || got.Human.AuthorPID != sent.From {
		t.Fatalf("request author is not the asking agent's participation: %+v", got.Human)
	}

	// Own agents ask and task each other without the person's click (owner
	// rule, MEL-713): both run to an answer on their own.
	jobID := func(lid string) string {
		var id string
		if err := a.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, room, lid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	waitState(t, a, jobID(sent.LID), stateAnswered)
	task := ask
	task.Kind, task.Body = envelope.KindTask, "rebase the fix branch"
	tsent, err := a.SendAgentRoomAsk(tctx(t), task)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, a, jobID(tsent.LID), stateAnswered)

	// Across the person's devices too: the ask leaves this device with no
	// originating run and runs on the other device without a click.
	desk, err := phone.CreateLocalAgent("desk", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = phone.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	remote := task
	remote.Host, remote.AgentID = phone.Address, desk.ID
	rsent, err := a.SendAgentRoomAsk(tctx(t), remote)
	if err != nil {
		t.Fatal(err)
	}
	var rid string
	eventually(t, "remote task stored on the other device", func() bool {
		return phone.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, room, rsent.LID).Scan(&rid) == nil
	})
	waitState(t, phone, rid, stateAnswered)

	// The answers come back to the asking terminal, from the asked agent.
	for _, req := range []AgentRoomSent{sent, rsent} {
		r, err := a.AwaitReply(tctx(t), req.ID, 15*time.Second, nil)
		if err != nil || r.Answer == nil || r.Answer.Conv != room {
			t.Fatalf("answer to %s not returned to the asker: %+v %v", req.ID, r, err)
		}
	}
	// A run serving a request in the room asks on through it, as the run's
	// own agent, in the cause's topic; its chain is checked on the other device.
	cause := jobID(sent.LID)
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, cause); err != nil {
		t.Fatal(err)
	}
	chained := AgentRoomAsk{Host: phone.Address, AgentID: desk.ID, Kind: envelope.KindTask, Body: "check the fix on the other device"}
	csent, err := a.RunAgentRoomAsk(tctx(t), cause, chained)
	if err != nil {
		t.Fatal(err)
	}
	if csent.Conv != room || csent.Topic != sent.Topic || csent.From != sent.To {
		t.Fatalf("run did not ask as its own agent in its cause's topic: %+v vs %+v", csent, sent)
	}
	var cid string
	eventually(t, "chained task stored on the other device", func() bool {
		return phone.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, room, csent.LID).Scan(&cid) == nil
	})
	waitState(t, phone, cid, stateAnswered)

	// Every own device converges on the one room, so an agent there joins it.
	if other, err := phone.MyAgentsRoom(tctx(t)); err != nil || other != room {
		t.Fatalf("the person's other device made or chose another room: %s vs %s %v", other, room, err)
	}

	// The same session continues its topic; another session starts its own.
	again, err := a.SendAgentRoomAsk(tctx(t), ask)
	if err != nil || again.Topic != sent.Topic || again.From != sent.From || again.To != sent.To {
		t.Fatalf("same session left its topic or re-invited: %+v %v", again, err)
	}
	ask.SessionKey = "agentstub/session-2"
	other, err := a.SendAgentRoomAsk(tctx(t), ask)
	if err != nil || other.Conv != room || other.Topic == sent.Topic {
		t.Fatalf("another session must get its own topic in the same room: %+v %v", other, err)
	}
}

func TestSessionAgentRefusesUnregisteredHarness(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice)
	if _, err := w.alice.SessionAgent("codex", ""); !errors.Is(err, ErrNoSessionAgent) || !strings.Contains(err.Error(), "Agents") {
		t.Fatalf("unregistered harness must be refused with where to add it: %v", err)
	}
	if _, err := w.alice.SessionAgent("", ""); !errors.Is(err, ErrNoSessionAgent) {
		t.Fatalf("unknown terminal must be refused: %v", err)
	}
	if h, s := SessionHarness(func(k string) string {
		return map[string]string{ClaudeSessionEnv: "c1", "CODEX_THREAD_ID": "x1"}[k]
	}); h != "" || s != "" {
		t.Fatalf("two named harnesses cannot prove which one asks: %s %s", h, s)
	}
}

// MEL-713 fence: another person's agent asking from its terminal (no
// upstream request) is checked as its host device. Without a grant there,
// it waits for this host's person; it never borrows own-agent freedom.
func TestAgentRoomRootAskFromAnotherPersonNeedsConsent(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	from := p6Member(t, w.alice, w.alice, conv)
	to := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "asking agent known at target", func() bool { return stateAt(t, w.bob, from.PID).Claimable() })
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		sent, err := w.alice.postAgentAsk(tctx(t), conv, "", "", stateAt(t, w.alice, from.PID), stateAt(t, w.alice, to.PID), kind, "root "+kind, "agentstub", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		eventually(t, "root ask stored at the other person's host", func() bool {
			return w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=?`, conv, sent.LID).Scan(&id) == nil
		})
		waitState(t, w.bob, id, stateAwaiting)
	}
	if n := stub.runs(); n != 0 {
		t.Fatalf("another person's agent ran work here without consent: %d run(s)", n)
	}
}

// Chain rule (owner, October 29, 2026): a run tasks the person's other
// agents only when its requester may task this agent; a requester who may
// only ask makes the run only ask.
func TestRunMayTaskFollowsRequesterAuthority(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	a, bob := w.alice, w.bob.Self()
	for _, c := range []struct {
		name, sender, key, kind string
		want                    bool
	}{
		{"own question", a.Address, a.Self().Fingerprint(), envelope.KindQuestion, true},
		{"admitted task", w.bob.Address, bob.Fingerprint(), envelope.KindTask, true},
		{"question-only requester", w.bob.Address, bob.Fingerprint(), envelope.KindQuestion, false},
	} {
		if got, err := a.runMayTask(c.sender, c.key, c.kind); err != nil || got != c.want {
			t.Fatalf("%s: %v %v", c.name, got, err)
		}
	}
	if err := a.Approve(w.bob.Address); err != nil { // questions only
		t.Fatal(err)
	}
	if got, err := a.runMayTask(w.bob.Address, bob.Fingerprint(), envelope.KindQuestion); err != nil || got {
		t.Fatalf("a question approval let the run task: %v %v", got, err)
	}
	if _, err := a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,1,?,?,1,?,?)`,
		protocol.NewID(), w.bob.Address, envelope.KindQuestion, "question from bob", stateRunning, bob.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	var cause string
	a.store.db.QueryRow(`SELECT id FROM inbox WHERE sender=? AND state=?`, w.bob.Address, stateRunning).Scan(&cause)
	if _, err := a.RunAgentRoomAsk(tctx(t), cause, AgentRoomAsk{Kind: envelope.KindTask, Body: "x"}); err == nil || !strings.Contains(err.Error(), "may only ask") {
		t.Fatalf("question-only requester's run tasked another agent: %v", err)
	}
}

// MEL-713 (owner, October 29, 2026): another person's agent visits the "My
// agents" room in the job's topic. Its person accepts the visit, or its host
// joins without a click when its person approved the asker; questions run on
// that approval, a task waits for a grant or a one-time accept there, and a
// run's chained ask is judged there by the asking device, since that host
// never holds the room's earlier history.
func TestAgentRoomAskVisitsAnotherPersonsAgent(t *testing.T) {
	stub := installAgentStub(t)
	Harnesses["roomasker"] = Harnesses["agentstub"] // one enabled agent per harness per device
	t.Cleanup(func() { delete(Harnesses, "roomasker") })
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	a, b := w.alice, w.bob
	runAgent(t, a)
	runAgent(t, b)
	phone := linked(t, a) // a participation event needs another own device to carry it
	for _, d := range []*Agent{a, phone, b} {
		publishGroupFixtureCaps(t, d, true)
	}
	asker, err := a.CreateLocalAgent("asker", Responder{Harness: "roomasker", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := a.CreateLocalAgent("helper", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	neel, err := b.CreateLocalAgent("neel", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*Agent{a, b} {
		if err = d.PublishAgentCatalog(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	from, err := a.SessionAgent("", asker.ID)
	if err != nil {
		t.Fatal(err)
	}
	inboxAt := func(d *Agent, conv, lid string) string {
		var id string
		eventually(t, "request stored at its host", func() bool {
			return d.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, conv, lid).Scan(&id) == nil
		})
		return id
	}

	// Without an approval there, the visitor's person accepts the invite.
	ask := AgentRoomAsk{From: from, SessionKey: "agentstub/visit", Host: b.Address, AgentID: neel.ID, Kind: envelope.KindQuestion, Body: "which fee rate applies?", Join: 2 * time.Second}
	pending, err := a.SendAgentRoomAsk(tctx(t), ask)
	if err == nil || !strings.Contains(err.Error(), "has not joined") || pending.To == "" {
		t.Fatalf("an unapproved visit must wait for its person: %+v %v", pending, err)
	}
	eventually(t, "visit invitation at its host", func() bool {
		p, e := b.Participation(pending.To)
		return e == nil && p.State == PartInvited
	})
	if _, err = b.AcceptParticipation(tctx(t), pending.To); err != nil {
		t.Fatal(err)
	}
	if err = b.Approve(a.Address); err != nil { // questions only
		t.Fatal(err)
	}
	ask.Join = 0
	sent, err := a.SendAgentRoomAsk(tctx(t), ask)
	if err != nil || sent.To != pending.To {
		t.Fatalf("the same session must reuse its accepted visit: %+v %v", sent, err)
	}
	room, err := a.MyAgentsRoom(tctx(t))
	if err != nil || sent.Conv != room {
		t.Fatalf("visit outside the My agents room: %s vs %s %v", sent.Conv, room, err)
	}
	if m, err := a.dmMembers(room); err != nil || len(m.persons) != 1 {
		t.Fatalf("a visiting agent must not make its person a room member: %v", err)
	}
	visit := stateAt(t, a, sent.To)
	if !visit.External || visit.Topic == nil || *visit.Topic != sent.Topic || len(visit.TaskKeys) != 0 || visit.Host.Address != b.Address {
		t.Fatalf("not a topic-only visitor without task keys: %+v", visit)
	}
	waitState(t, b, inboxAt(b, room, sent.LID), stateAnswered)
	if r, err := a.AwaitReply(tctx(t), sent.ID, 15*time.Second, nil); err != nil || r.Answer == nil || r.Answer.Conv != room {
		t.Fatalf("visitor's answer not returned to the asker: %+v %v", r, err)
	}

	// A question approval never lets a task run: the visitor's person decides.
	task := ask
	task.Kind, task.Body = envelope.KindTask, "draft the fee table"
	tsent, err := a.SendAgentRoomAsk(tctx(t), task)
	if err != nil || tsent.To != sent.To {
		t.Fatalf("same session must reuse the visit: %+v %v", tsent, err)
	}
	tid := inboxAt(b, room, tsent.LID)
	waitState(t, b, tid, stateAwaiting)
	if err = b.Accept(tid); err != nil {
		t.Fatal(err)
	}
	waitState(t, b, tid, stateAnswered)

	// A run of the person's own agent serving a request in the room asks the
	// visitor on, in the cause's topic, which the visitor's host joined late;
	// that host's question approval lets the new visit in without a click.
	own := AgentRoomAsk{From: from, SessionKey: "agentstub/own", Host: a.Address, AgentID: helper.ID, Kind: envelope.KindQuestion, Body: "which month?"}
	osent, err := a.SendAgentRoomAsk(tctx(t), own)
	if err != nil {
		t.Fatal(err)
	}
	cause := inboxAt(a, room, osent.LID)
	waitState(t, a, cause, stateAnswered)
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, cause); err != nil {
		t.Fatal(err)
	}
	csent, err := a.RunAgentRoomAsk(tctx(t), cause, AgentRoomAsk{Host: b.Address, AgentID: neel.ID, Kind: envelope.KindQuestion, Body: "send the rate you used"})
	if err != nil {
		t.Fatal(err)
	}
	if csent.Topic != osent.Topic || csent.From != osent.To {
		t.Fatalf("run did not ask as its own agent in its cause's topic: %+v vs %+v", csent, osent)
	}
	waitState(t, b, inboxAt(b, room, csent.LID), stateAnswered)
}
