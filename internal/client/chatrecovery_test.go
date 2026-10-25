package client

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A linked human device displays its own transport copy of a group request.
// Its handled and stop controls must name the shared logical request the host
// holds, so the decision reaches that exact request instead of being refused.
func TestOwnGroupRequestDecisionNamesSharedRequest(t *testing.T) {
	st := installStub(t, "clarify")
	w, _, group, _ := groupTurnsFixture(t)
	host, conv := w.alice, group.State.Conv
	setResponder(t, host, "stub", st.dir, time.Minute)
	phone := linkedVia(t, host, "phone", approveLink(host))
	publishGroupFixtureCaps(t, phone, true)
	eventually(t, "phone current group", func() bool {
		p, e := phone.GroupContext(conv)
		return e == nil && p.State.Hash() == group.State.Hash()
	})
	part, err := host.InviteAgent(tctx(t), conv, host.Address, nil, nil, "own handled requests")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone active agent", func() bool {
		p, e := phone.Participation(part.PID)
		return e == nil && p.Claimable()
	})
	q, err := phone.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "which release branch?")
	if err != nil {
		t.Fatal(err)
	}
	var request string
	eventually(t, "host stores the group request", func() bool {
		return host.store.db.QueryRow(`SELECT id FROM inbox WHERE lid=? AND conv=? AND replica=0`, q.LID, conv).Scan(&request) == nil
	})
	waitState(t, host, request, stateNeedHuman)
	// The agent is told the exact mention of the person asking and that its
	// reply renders Markdown; no prompt line demands plain text.
	person, _, err := host.Person()
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(st.log + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	if want := mentionAnchor("person", person.Person, person.Label); want == "" || !strings.Contains(string(prompt), want) || !strings.Contains(string(prompt), markdownPromptText) || strings.Contains(strings.ToLower(string(prompt)), "plain text") || strings.Contains(strings.ToLower(string(prompt)), "plain-text") {
		t.Fatalf("group prompt lacks the exact requester mention %q or Markdown, or demands plain text:\n%s", want, prompt)
	}
	var shown ConvMessage
	eventually(t, "phone shows the host's needs-human state", func() bool {
		msgs, e := phone.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, m := range msgs {
			if m.LID == q.LID && m.Exec != nil && m.Exec.State == stateNeedHuman {
				shown = m
				return true
			}
		}
		return false
	})
	t.Logf("phone displays copy %s for logical request %s; host holds %s", shown.ID, shown.LID, request)
	phoneShows := func(lid, state string) (ConvMessage, bool) {
		msgs, e := phone.ConversationMessages(conv)
		if e != nil {
			return ConvMessage{}, false
		}
		for _, m := range msgs {
			if m.LID == lid && m.Exec != nil && m.Exec.State == state {
				return m, true
			}
		}
		return ConvMessage{}, false
	}
	// An unrelated request that also needs its person, asked before the decision.
	other, err := phone.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "which changelog section?")
	if err != nil {
		t.Fatal(err)
	}
	var otherRequest string
	eventually(t, "host stores the unrelated request", func() bool {
		return host.store.db.QueryRow(`SELECT id FROM inbox WHERE lid=? AND conv=? AND replica=0`, other.LID, conv).Scan(&otherRequest) == nil
	})
	waitState(t, host, otherRequest, stateNeedHuman)
	eventually(t, "phone shows the unrelated needs-human", func() bool { _, ok := phoneShows(other.LID, stateNeedHuman); return ok })
	continuation, err := phone.ContinuationFor(shown.ID, shown.Key, shown.Exec)
	if err != nil || continuation == nil || continuation.ID != request || continuation.Host != host.Address {
		t.Fatalf("handled control names %+v %v; host request %s", continuation, err, request)
	}
	// A refused decision is shown on the control, not left as queued.
	if _, err = phone.Decide(WithQueuedSend(tctx(t), protocol.NewID()), continuation.Host, continuation.ID, continuation.Key, "resolve", stateRunning, continuation.Attempt, "", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "refusal shown on the handled control", func() bool {
		c, e := phone.ContinuationFor(shown.ID, shown.Key, shown.Exec)
		return e == nil && c != nil && c.Handled != nil && c.Handled.Refused != ""
	})
	if state, _ := host.store.jobState(request); state != stateNeedHuman {
		t.Fatalf("refused decision changed the request: %s", state)
	}
	sendID := protocol.NewID()
	if _, err = phone.Decide(WithQueuedSend(tctx(t), sendID), continuation.Host, continuation.ID, continuation.Key, "resolve", stateNeedHuman, continuation.Attempt, "", ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, host, request, stateResolved)
	var attempts int
	if err = host.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, request).Scan(&attempts); err != nil || attempts != 1 || st.count() != 2 {
		t.Fatalf("handling reran the request: attempts=%d runs=%d %v", attempts, st.count(), err)
	}
	// The sender's own device converges through the host's signed return:
	// the request shows resolved, nothing is left to decide or continue on
	// it, and the unrelated request still needing its person is untouched.
	var done ConvMessage
	eventually(t, "phone shows the request resolved", func() bool {
		var ok bool
		done, ok = phoneShows(shown.LID, stateResolved)
		return ok
	})
	if c, e := phone.ContinuationFor(done.ID, done.Key, done.Exec); e != nil || c != nil || done.JobDetail != "" {
		t.Fatalf("a resolved request is still actionable on the sender: %+v %q %v", c, done.JobDetail, e)
	}
	still, ok := phoneShows(other.LID, stateNeedHuman)
	if !ok {
		t.Fatal("the unrelated request left needs-human on the sender")
	}
	if c, e := phone.ContinuationFor(still.ID, still.Key, still.Exec); e != nil || c == nil || c.ID != otherRequest {
		t.Fatalf("the unrelated request lost its exact continuation: %+v %v", c, e)
	}
	if state, _ := host.store.jobState(otherRequest); state != stateNeedHuman {
		t.Fatalf("the decision changed the unrelated request: %s", state)
	}
	// Stop shares the same request naming, on a running request.
	t.Setenv("STUB_MODE", "sleep")
	q, err = phone.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "start the long check")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "host runs the second request", func() bool {
		return host.store.db.QueryRow(`SELECT id FROM inbox WHERE lid=? AND conv=? AND replica=0 AND state=?`, q.LID, conv, stateRunning).Scan(&request) == nil
	})
	eventually(t, "phone shows the running request", func() bool {
		msgs, e := phone.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, m := range msgs {
			if m.LID == q.LID && m.Exec != nil && m.Exec.State == stateRunning {
				shown = m
				return true
			}
		}
		return false
	})
	stop, err := phone.StopFor(shown.ID, shown.Key, shown.Exec)
	if err != nil || stop == nil || stop.ID != request {
		t.Fatalf("stop control names %+v %v; host request %s", stop, err, request)
	}
	if _, err = phone.Decide(WithQueuedSend(tctx(t), protocol.NewID()), stop.Host, stop.ID, stop.Key, "cancel", stateRunning, stop.Attempt, "", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "host stops the exact request", func() bool {
		state, _ := host.store.jobState(request)
		return state != stateRunning
	})
}

func TestReactionOnly(t *testing.T) {
	for _, c := range []struct {
		in, emoji string
		remove    bool
	}{
		{"reaction: ❤️", "❤️", false},
		{"  Reaction:  -🎉 \n", "🎉", true},
		{"reaction: ❤️\nthanks", "", false},
		{"thanks\nreaction: ❤️", "", false},
		{"reaction: Very Happy", "", false},
		{"reaction: 👍👍", "", false},
		{"a plain answer", "", false},
	} {
		got := reactionOnly(c.in)
		if (got != nil) != (c.emoji != "") || got != nil && (got.emoji != c.emoji || got.remove != c.remove) {
			t.Errorf("%q: %+v", c.in, got)
		}
	}
}

// An assistant that answers only with its reaction attaches it to the request
// and answers it; no text bubble repeats the reaction line.
func TestReactionOnlyAnswerDeviceThread(t *testing.T) {
	reactHarness(t, "reactonly", `reaction: ❤️\n`)
	w := newWorld(t, "")
	setResponder(t, w.bob, "reactonly", t.TempDir(), time.Minute)
	w.bob.Approve(w.alice.Address)
	runAgent(t, w.bob)
	progressReader(t, w.alice, w.bob)
	withAgentReaction(t, w.alice, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "great job"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "reaction on the request", func() bool {
		rs := v1Reactors(t, w.alice, q.ID, "❤️")
		return len(rs) == 1 && rs[0].Assistant
	})
	waitState(t, w.bob, q.ID, stateAnswered)
	eventually(t, "requester told it is answered", func() bool {
		e := execOf(t, w.alice, q.ID)
		return e != nil && e.State == stateAnswered
	})
	if n := inboxCount(t, w.alice, "reply_to = ?", q.ID); n != 0 {
		t.Fatalf("a reaction-only answer posted %d text replies", n)
	}
}

// The same in a conversation: the reaction-only reply before its emotion line.
func TestReactionOnlyAnswerParticipation(t *testing.T) {
	w, host, conv, _, _, _, _ := externalAgentWorld(t)
	reactHarness(t, "reactonlyagent", `reaction: ❤️\nemotion: happy\n`)
	record, err := host.CreateLocalAgent("Reactor", Responder{Harness: "reactonlyagent", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	withAgentReaction(t, w.alice, w.bob, host)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invite at host", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "active", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "great job")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		eventually(t, "assistant reaction at "+a.Address, func() bool {
			msgs, _ := a.ConversationMessages(conv)
			rs := reactorsOn(msgs, q.LID, "❤️")
			return len(rs) == 1 && rs[0].ID == "assistant:"+p.PID
		})
	}
	eventually(t, "request answered at the host", func() bool {
		var state string
		return host.store.db.QueryRow(`SELECT state FROM inbox WHERE lid=? AND conv=? AND replica=0`, q.LID, conv).Scan(&state) == nil && state == stateAnswered
	})
	msgs, err := w.alice.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ReplyTo == q.LID || m.ReplyTo == q.ID || strings.Contains(m.Body, "reaction:") {
			t.Fatalf("a reaction-only answer posted a text reply: %+v", m)
		}
	}
}

// The reported case (oct23 T5): the owner asks their own group agent on the
// agent's own host and the agent answers only with a reaction. It is attached
// to that exact request for every member, the owner's own view included, and
// no text reply stands in for it.
func TestReactionOnlyAnswerLocalGroupRequester(t *testing.T) {
	st := installAgentStub(t)
	st.mode("echo")
	t.Setenv("AGENT_ANSWER", "reaction: ❤️")
	w, carol, group, stops := groupTurnsFixture(t)
	for a := range stops {
		addCapSuccessor(t, a, protocol.CapAgentReaction)
	}
	host, conv := w.alice, group.State.Conv
	setResponder(t, host, "agentstub", st.dir, time.Minute)
	part, err := host.InviteAgent(tctx(t), conv, host.Address, nil, nil, "local reactions")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "agent active", func() bool { return stateAt(t, host, part.PID).Claimable() })
	q, err := host.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "great job")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{host, w.bob, carol} {
		eventually(t, "assistant reaction at "+a.Address, func() bool {
			msgs, _ := a.ConversationMessages(conv)
			rs := reactorsOn(msgs, q.LID, "❤️")
			return len(rs) == 1 && rs[0].ID == "assistant:"+part.PID && rs[0].Assistant
		})
	}
	waitState(t, host, q.LID, stateAnswered)
	var answers int
	if err = host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND kind=? AND coalesce(sub,'')=''`, conv, envelope.KindAnswer).Scan(&answers); err != nil || answers != 0 {
		t.Fatalf("a reaction-only answer posted %d text replies: %v", answers, err)
	}
	for _, a := range []*Agent{host, w.bob, carol} {
		msgs, err := a.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.ReplyTo == q.LID || m.Body == "❤️" || strings.Contains(m.Body, "reaction:") {
				t.Fatalf("a text bubble stands in for the reaction at %s: %+v", a.Address, m)
			}
		}
	}
	if st.runs() != 1 {
		t.Fatalf("runs %d", st.runs())
	}
}

// A same-host group agent asking another agent of this device waits for a
// reply it can read: a reaction-only answer attaches the real reaction to its
// request and replies only that AgentNet saw that answer, never the trailer.
func TestReactionOnlyAnswerToAgentAsker(t *testing.T) {
	reactHarness(t, "reactpeer", `reaction: 👍\nemotion: calm\n`) // never started: the outcome is given below
	w, _, packet, stops := groupTurnsFixture(t)
	for a := range stops {
		addCapSuccessor(t, a, protocol.CapAgentReaction)
	}
	host, conv := w.alice, packet.State.Conv
	record, err := host.CreateLocalAgent("Peer", Responder{Harness: "reactpeer", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	from := p6Member(t, host, host, conv)
	to, err := host.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "peer active", func() bool { return stateAt(t, host, to.PID).Claimable() })
	root, err := host.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "running source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); err != nil {
		t.Fatal(err)
	}
	sent, err := host.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindQuestion, "great job")
	if err != nil {
		t.Fatal(err)
	}
	// The exact same-host request as stored for this device's peer agent.
	j := job{Kind: envelope.KindQuestion, Body: "great job", Conv: conv, PID: to.PID, Local: true}
	var target string
	if err = host.store.db.QueryRow(`SELECT id, sender, verified_by, coalesce(target,'') FROM inbox WHERE conv=? AND lid=? AND local=1`, conv, sent.LID).Scan(&j.ID, &j.From, &j.Key, &target); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(target), &j.Target); err != nil {
		t.Fatal(err)
	}
	j.AgentID = record.ID // as the worker's claim stamps the named executor
	if !host.agentAsked(j.ID) {
		t.Fatal("the same-host room ask is not seen as asked by an agent")
	}
	prompt, err := host.agentPrompt(j, &Responder{Harness: "reactpeer"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, reactionConvAgentPromptText) || strings.Contains(prompt, "no text message is posted") {
		t.Fatalf("agent-asked prompt misstates a reaction-only reply:\n%s", prompt)
	}
	if _, err = host.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, j.ID); err != nil {
		t.Fatal(err)
	}
	host.finishAgent(tctx(t), j, &Responder{Harness: "reactpeer"}, envelope.StatusDone, "reaction: 👍\nemotion: calm")
	var state, detail string
	if err = host.store.db.QueryRow(`SELECT state, coalesce(detail,'') FROM inbox WHERE id=?`, j.ID).Scan(&state, &detail); err != nil || state != stateAnswered {
		t.Fatalf("peer request %s %q %v", state, detail, err)
	}
	reply, err := host.RoomReply(root.LID, sent.LID)
	if err != nil || reply == nil || reply.PID != to.PID || !strings.HasSuffix(reply.Body, "\n\nAgentNet: answered with the reaction 👍 (no text reply).") || strings.Contains(reply.Body, "reaction: ") {
		t.Fatalf("the asking agent's wait has no readable reply: %+v %v", reply, err)
	}
	for _, a := range []*Agent{host, w.bob} {
		eventually(t, "the peer's reaction attached at "+a.Address, func() bool {
			msgs, _ := a.ConversationMessages(conv)
			rs := reactorsOn(msgs, sent.LID, "👍")
			return len(rs) == 1 && rs[0].ID == "assistant:"+to.PID && rs[0].Assistant
		})
	}
}

// A reaction-only answer whose reaction cannot be attached ends not sent,
// saying why; no emoji text bubble stands in for it.
func TestReactionOnlyAnswerUnavailableIsNotABubble(t *testing.T) {
	st := installAgentStub(t)
	st.mode("echo")
	t.Setenv("AGENT_ANSWER", "reaction: ❤️")
	w, carol, group, _ := groupTurnsFixture(t)
	host, conv := w.alice, group.State.Conv
	for _, a := range []*Agent{w.bob, carol} {
		dropCapSuccessor(t, a, protocol.CapControl) // no other reader can show reactions
	}
	setResponder(t, host, "agentstub", st.dir, time.Minute)
	part, err := host.InviteAgent(tctx(t), conv, host.Address, nil, nil, "unavailable reactions")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "agent active", func() bool { return stateAt(t, host, part.PID).Claimable() })
	q, err := host.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "great job")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, host, q.LID, stateNotDelivered)
	var detail string
	if err = host.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, q.LID).Scan(&detail); err != nil || !strings.Contains(detail, "reaction: ❤️") || !strings.Contains(detail, "not sent") {
		t.Fatalf("not-sent detail %q %v", detail, err)
	}
	var answers, reactions int
	if err = host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND kind=? AND coalesce(sub,'')=''`, conv, envelope.KindAnswer).Scan(&answers); err != nil || answers != 0 {
		t.Fatalf("an emoji bubble was posted: %d %v", answers, err)
	}
	if err = host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND sub=?`, conv, envelope.SubReaction).Scan(&reactions); err != nil || reactions != 0 {
		t.Fatalf("an unreadable reaction was stored: %d %v", reactions, err)
	}
}

// A stop requested before the reaction-only answer settles wins: nothing is
// sent and the request is not marked answered.
func TestReactionOnlyAnswerKeepsStop(t *testing.T) {
	w := newWorld(t, "")
	w.bob.Approve(w.alice.Address)
	stop := runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "great job"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "request stored", func() bool { return inboxHas(t, w.bob, q.ID) })
	stop()
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.answerWithReaction(tctx(t), job{ID: q.ID, Kind: envelope.KindQuestion, From: w.alice.Address}, "reactonly", &reactionChoice{emoji: "❤️"}); !errors.Is(err, errNotOwned) {
		t.Fatalf("a stopped request was answered with a reaction: %v", err)
	}
	if state := jobState(t, w.bob, q.ID); state != stateCancelReq {
		t.Fatalf("stop lost: %s", state)
	}
	var reactions int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubReaction).Scan(&reactions); err != nil || reactions != 0 {
		t.Fatalf("reaction sent for a stopped request: %d %v", reactions, err)
	}
}

// A stop stored while the reaction is being handed over still wins: the
// sent reaction stays, the request ends stopped, and no text bubble follows.
func TestReactionOnlyAnswerStopDuringDelivery(t *testing.T) {
	reactHarness(t, "reactonly", `reaction: ❤️\n`)
	w := newWorld(t, "")
	setResponder(t, w.bob, "reactonly", t.TempDir(), time.Minute)
	w.bob.Approve(w.alice.Address)
	var stopped atomic.Bool
	var request atomic.Value
	w.bob.hub.http.Transport = postHook{w.bob.hub.http.Transport, func(env envelope.Envelope) {
		id, _ := request.Load().(string)
		var reaction int
		if id == "" || stopped.Load() || w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id=? AND sub=?`, env.ID, envelope.SubReaction).Scan(&reaction) != nil || reaction != 1 {
			return
		}
		if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=? AND state=?`, stateCancelReq, id, stateRunning); err != nil {
			t.Error(err)
		}
		stopped.Store(true)
	}}
	runAgent(t, w.bob)
	progressReader(t, w.alice, w.bob)
	withAgentReaction(t, w.alice, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "great job"})
	if err != nil {
		t.Fatal(err)
	}
	request.Store(q.ID)
	eventually(t, "reaction on the request", func() bool {
		rs := v1Reactors(t, w.alice, q.ID, "❤️")
		return len(rs) == 1 && rs[0].Assistant
	})
	waitState(t, w.bob, q.ID, stateCancelled)
	if !stopped.Load() {
		t.Fatal("the stop was not stored during the reaction hand-over")
	}
	if n := inboxCount(t, w.alice, "reply_to = ?", q.ID); n != 0 {
		t.Fatalf("a stopped reaction-only answer posted %d text replies", n)
	}
}

func TestDevicePromptsUseMarkdown(t *testing.T) {
	w := newWorld(t, "")
	for _, j := range []job{
		{From: w.alice.Address, Kind: envelope.KindQuestion, Body: "Prepare the report"},
		{From: w.alice.Address, Kind: envelope.KindTask, Body: "Prepare the report"},
	} {
		prompt, err := w.bob.prompt(j, &Responder{})
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(prompt)
		if !strings.Contains(prompt, markdownPromptText) || strings.Contains(lower, "plain text") || strings.Contains(lower, "plain-text") {
			t.Fatalf("%s prompt:\n%s", j.Kind, prompt)
		}
	}
}

// agentAnswerBody is the text the host's agent posted for request lid.
// taggedAnswer is the exact group answer body: the asking person's canonical
// mention, a blank line, then what the agent wrote (oct23 T4).
func taggedAnswer(t *testing.T, asker *Agent, body string) string {
	t.Helper()
	person, _, err := asker.Person()
	if err != nil {
		t.Fatal(err)
	}
	return mentionAnchor("person", person.Person, person.Label) + "\n\n" + body
}

func agentAnswerBody(t *testing.T, host *Agent, conv, lid string) string {
	t.Helper()
	var body string
	eventually(t, "agent answer posted", func() bool {
		return host.store.db.QueryRow(`SELECT body FROM outbox WHERE conv=? AND kind=? AND reply_to IN (SELECT id FROM inbox WHERE conv=? AND lid=? UNION SELECT ?)`, conv, envelope.KindAnswer, conv, lid, lid).Scan(&body) == nil
	})
	return body
}

// In a group the agent's answer opens with the asker's exact mention, once,
// whatever the agent wrote; a plain DM gets none (oct23 T4).
func TestGroupAnswerTagsExactRequester(t *testing.T) {
	st := installAgentStub(t)
	w, _, group, _ := groupTurnsFixture(t)
	host, conv := w.alice, group.State.Conv
	setResponder(t, host, "agentstub", st.dir, time.Minute)
	part, err := host.InviteAgent(tctx(t), conv, host.Address, nil, nil, "tagged answers")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "agent active", func() bool { return stateAt(t, host, part.PID).Claimable() })
	person, _, err := host.Person()
	if err != nil {
		t.Fatal(err)
	}
	anchor := mentionAnchor("person", person.Person, person.Label)
	q, err := host.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "why did it fail?")
	if err != nil {
		t.Fatal(err)
	}
	if body := agentAnswerBody(t, host, conv, q.LID); body != anchor+"\n\nthe deploy failed at step 3" {
		t.Fatalf("group answer %q, want it to open with %q", body, anchor)
	}
	if !strings.Contains(st.last(), anchor) {
		t.Fatal("the prompt did not give the exact requester mention")
	}
	// Already tagged anywhere, under any label: not tagged twice.
	st.mode("echo")
	t.Setenv("AGENT_ANSWER", "## Branch\n\n- use main, [@Owner](agentnet:person/"+person.Person+")")
	q, err = host.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if body := agentAnswerBody(t, host, conv, q.LID); body != "## Branch\n\n- use main, [@Owner](agentnet:person/"+person.Person+")" {
		t.Fatalf("an already tagged answer changed: %q", body)
	}
	// A bare reference is not a mention: the real one is still added.
	t.Setenv("AGENT_ANSWER", "see (agentnet:person/"+person.Person+")")
	q, err = host.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "bare reference?")
	if err != nil {
		t.Fatal(err)
	}
	if body := agentAnswerBody(t, host, conv, q.LID); body != anchor+"\n\nsee (agentnet:person/"+person.Person+")" {
		t.Fatalf("a bare reference suppressed the mention: %q", body)
	}
}

// A group agent asking another group agent is tagged as that exact agent
// participation, in the prompt and on the answer.
func TestGroupAnswerTagsAskingAgent(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	if e := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); e != nil {
		t.Fatal(e)
	}
	if e := w.bob.Approve(w.alice.Address); e != nil {
		t.Fatal(e)
	}
	from := p6Member(t, w.alice, w.alice, conv)
	to := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "source scope", func() bool { return stateAt(t, w.bob, from.PID).Claimable() })
	root, e := w.alice.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "running source")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); e != nil {
		t.Fatal(e)
	}
	child, e := w.alice.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindQuestion, "agent question")
	if e != nil {
		t.Fatal(e)
	}
	// Check the answer's tagging independently of the tool's synchronous
	// send-and-wait budget; its ask path has separate coverage.
	body := agentAnswerBody(t, w.bob, conv, child.LID)
	raw, _ := json.Marshal(map[string]string{"action": "wait", "id": child.LID})
	reply, e := w.alice.codexRoomTool(tctx(t), root.LID, raw)
	if e != nil || !strings.Contains(reply, "reply "+child.LID+" from agent participation "+to.PID) {
		t.Fatalf("%s %v", reply, e)
	}
	source, e := w.bob.Participation(from.PID)
	if e != nil {
		t.Fatal(e)
	}
	anchor := mentionAnchor("agent", from.PID, agentMentionLabel(source.Host.Label))
	if body != anchor+"\n\nthe deploy failed at step 3" || !strings.Contains(stub.last(), anchor) {
		t.Fatalf("agent-origin answer %q; prompt names %q: %v", body, anchor, strings.Contains(stub.last(), anchor))
	}
}

// A plain two-person DM answer is not tagged.
func TestDMAnswerIsNotTagged(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "why did it fail?")
	if err != nil {
		t.Fatal(err)
	}
	if body := agentAnswerBody(t, w.bob, conv, q.LID); body != "the deploy failed at step 3" {
		t.Fatalf("DM answer %q", body)
	}
}
