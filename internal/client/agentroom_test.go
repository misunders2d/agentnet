package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
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
		sent, err := w.alice.postAgentAsk(tctx(t), conv, "", stateAt(t, w.alice, from.PID), stateAt(t, w.alice, to.PID), kind, "root "+kind, "agentstub", nil)
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
