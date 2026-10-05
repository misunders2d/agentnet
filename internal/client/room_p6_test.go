package client

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func p6Member(t *testing.T, inviter, host *Agent, conv string) ParticipationInfo {
	t.Helper()
	p, err := inviter.InviteAgent(tctx(t), conv, host.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "group agent invitation", func() bool {
		p, e := host.Participation(p.PID)
		return e == nil && (p.State == PartInvited || p.Claimable())
	})
	if stateAt(t, host, p.PID).State == PartInvited {
		if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "agent membership accepted", func() bool { return stateAt(t, inviter, p.PID).Claimable() })
	return stateAt(t, inviter, p.PID)
}

func TestP6GroupMemberReuseAndNonInviter(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	private, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "EARLIER_UNSELECTED"})
	if err != nil {
		t.Fatal(err)
	}
	p := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "third member sees accepted membership", func() bool { return stateAt(t, carol, p.PID).Claimable() })
	if !p.Member {
		t.Fatal("not permanent member")
	}
	q, err := carol.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "non-inviter question")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.LID, stateAwaiting)
	if err = w.bob.Accept(q.LID); err != nil {
		t.Fatal(err)
	}
	replyAt(t, carol, conv, q.LID)
	if strings.Contains(stub.last(), "EARLIER_UNSELECTED") {
		t.Fatal("ambient earlier history leaked")
	}
	shared, err := carol.InviteAgent(tctx(t), conv, w.bob.Address, []string{private.LID}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if shared.PID != p.PID {
		t.Fatal("second membership created")
	}
	eventually(t, "shared grant merged at exact host", func() bool {
		p := stateAt(t, w.bob, p.PID)
		return p.Claimable() && len(p.Inviters) == 2 && len(p.Grant) == 1
	})
	if err = w.bob.Approve(carol.Address); err != nil {
		t.Fatal(err)
	}
	q, err = carol.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "approved third member question")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, carol, conv, q.LID)
	if !strings.Contains(stub.last(), "EARLIER_UNSELECTED") {
		t.Fatal("explicitly shared context missing")
	}
	parts, err := carol.Participations(conv)
	if err != nil || len(parts) != 1 {
		t.Fatalf("one stored membership: %+v %v", parts, err)
	}
	if _, err = carol.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "removed", func() bool { return stateAt(t, w.bob, p.PID).State == PartDismissed })
	if _, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "removed"); err == nil {
		t.Fatal("removed agent accepted new work")
	}
}

func TestP6AgentQuestionTaskCorrelationAndOrigin(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	from := p6Member(t, w.alice, w.alice, conv)
	to := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "source scope at target", func() bool { return stateAt(t, w.bob, from.PID).Claimable() })
	root, err := w.alice.AskAgent(tctx(t), from.PID, envelope.KindTask, "ask another group agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); err != nil {
		t.Fatal(err)
	}
	ask, err := w.alice.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindQuestion, "joke from a group agent")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, ask.LID)
	if !strings.Contains(stub.last(), "verified asking group agent (claimed name") {
		t.Fatal("asking agent rendered as its human owner")
	}
	reply, err := w.alice.RoomReply(root.LID, ask.LID)
	if err != nil || reply == nil || reply.PID != to.PID || !reply.VerifiedAgent {
		t.Fatalf("correlated return %+v %v", reply, err)
	}
	task, err := w.alice.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindTask, "bounded group task")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.LID, stateAwaiting)
	if err = w.bob.Accept(task.LID); err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, task.LID)
	for i := 0; i < 3; i++ {
		another, e := w.alice.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindQuestion, "another permitted ask")
		if e != nil {
			t.Fatal(e)
		}
		replyAt(t, w.alice, conv, another.LID)
	}
	// A nested ask keeps the full origin chain; the third hop is not capped.
	stops := func(a *Agent, id string) {
		t.Helper()
		if _, e := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, id); e != nil {
			t.Fatal(e)
		}
	}
	stops(w.bob, childIDForP6(t, w.bob, ask.LID))
	if e := w.alice.Approve(w.bob.Address); e != nil {
		t.Fatal(e)
	}
	nested, e := w.bob.SendRoomAsk(tctx(t), ask.LID, from.PID, envelope.KindQuestion, "nested request")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, w.alice, nested.LID, stateAgentWaiting)
	stops(w.alice, nested.LID)
	third, e := w.alice.SendRoomAsk(tctx(t), nested.LID, to.PID, envelope.KindQuestion, "third hop")
	if e != nil {
		t.Fatal(e)
	}
	replyAt(t, w.alice, conv, third.LID)
	// Future context includes the other agent's verified answer, with no forward.
	context, e := w.alice.agentContext(stateAt(t, w.alice, from.PID), "", defaultContextBytes)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, m := range context.Messages {
		found = found || m.ReplyTo == ask.LID && m.VerifiedAgent
	}
	if !found {
		t.Fatal("other agent answer unavailable as group context")
	}
	// Refusal is correlated too, and ends a waiter rather than hanging.
	refused, e := w.alice.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindTask, "decline this task")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, w.bob, refused.LID, stateAwaiting)
	if _, e = w.bob.Decline(tctx(t), refused.LID, "not today"); e != nil {
		t.Fatal(e)
	}
	eventually(t, "correlated refusal", func() bool {
		_, e := w.alice.RoomReply(root.LID, refused.LID)
		return e != nil && strings.Contains(e.Error(), refused.LID)
	})
	// A granted intermediary cannot launder an unapproved original asker.
	foreign, err := carol.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "outsider origin question")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "foreign root at source and target", func() bool { return inboxHas(t, w.alice, foreign.LID) && p6HasLID(t, w.bob, foreign.LID) })
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, foreign.LID); err != nil {
		t.Fatal(err)
	}
	child, err := w.alice.SendRoomAsk(tctx(t), foreign.LID, to.PID, envelope.KindQuestion, "must preserve Carol origin")
	if err != nil {
		t.Fatal(err)
	}
	last := ""
	eventually(t, "unapproved original asker needs host OK", func() bool {
		var state, detail string
		if e := w.bob.store.db.QueryRow(`SELECT state,coalesce(detail,'') FROM inbox WHERE id=?`, child.LID).Scan(&state, &detail); e != nil {
			return false
		}
		if state+detail != last {
			t.Logf("nested request: state=%s detail=%s", state, detail)
			last = state + detail
		}
		return state == stateAwaiting
	})
	if _, err = w.alice.SendRoomAsk(tctx(t), foreign.LID, to.PID, envelope.KindTask, "widen question"); err == nil {
		t.Fatal("question widened to task")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelled, root.LID); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.RoomReply(root.LID, ask.LID); err == nil {
		t.Fatal("cancelled loop still waiting")
	}
}

func childIDForP6(t *testing.T, a *Agent, lid string) string {
	t.Helper()
	var id string
	if e := a.store.db.QueryRow(`SELECT id FROM inbox WHERE lid=?`, lid).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}

func TestP6MembershipSurvivesOfflineRestart(t *testing.T) {
	w, carol, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	p := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "third member has membership", func() bool { return stateAt(t, carol, p.PID).Claimable() })
	stops[w.bob]()
	q, e := carol.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "offline group agent question")
	if e != nil {
		t.Fatal(e)
	}
	home := w.bob.home
	w.bob.Close()
	reopened, e := Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if got := stateAt(t, reopened, p.PID); !got.Member || !got.Claimable() || got.Until != 0 {
		t.Fatalf("membership lost on restart: %+v", got)
	}
	stub := installAgentStub(t)
	if e = reopened.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); e != nil {
		t.Fatal(e)
	}
	if e = reopened.Approve(carol.Address); e != nil {
		t.Fatal(e)
	}
	runAgent(t, reopened)
	replyAt(t, carol, conv, q.LID)
	if lenMustP6(t, reopened, conv) != 1 {
		t.Fatal("restart duplicated membership")
	}
}
func lenMustP6(t *testing.T, a *Agent, conv string) int {
	t.Helper()
	p, e := a.Participations(conv)
	if e != nil {
		t.Fatal(e)
	}
	return len(p)
}

func TestP6OriginAndTargetFences(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	from := p6Member(t, w.alice, w.alice, conv)
	to := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "source membership", func() bool { return stateAt(t, w.bob, from.PID).Claimable() })
	root, e := w.alice.AskAgent(tctx(t), from.PID, envelope.KindTask, "source request")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); e != nil {
		t.Fatal(e)
	}
	ask, e := w.alice.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindTask, "request to check")
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "chain admitted", func() bool { return inboxHas(t, w.bob, ask.LID) })
	original, e := roomCauseIn(w.bob.store.db, conv, ask.LID, w.bob.Address, w.bob.Self().Fingerprint())
	if e != nil {
		t.Fatal(e)
	}
	duplicate := protocol.NewID()
	if _, e = w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,verified_by,conv,lid,pid,target,human,reply_to,state) SELECT ?,?,ts,kind,body,received_at,?,conv,lid,pid,target,human,reply_to,state FROM inbox WHERE id=?`, duplicate, w.bob.Address, w.bob.Self().Fingerprint(), ask.LID); e != nil {
		t.Fatal(e)
	}
	if _, e = roomCauseIn(w.bob.store.db, conv, ask.LID, w.bob.Address, w.bob.Self().Fingerprint()); e == nil {
		t.Fatal("ambiguous signed origin counted")
	}
	request := agentReq{ID: childIDForP6(t, w.bob, ask.LID), Sender: original.from, Key: original.key, Conv: conv, PID: to.PID, Kind: original.kind, Target: original.target, State: stateAccepted}
	members, e := w.bob.dmMembers(conv)
	if e != nil {
		t.Fatal(e)
	}
	if v, _, err := roomChain(w.bob.store.db, request, members, stateAt(t, w.bob, to.PID), w.bob.Address, w.bob.Self().Fingerprint(), false); err != nil || v != verdictStop {
		t.Fatalf("ambiguous request must stop only itself: verdict=%d err=%v", v, err)
	}
	// An unrelated ordinary turn with the same lid is not a second cause.
	if _, e = w.bob.store.db.Exec(`UPDATE inbox SET kind='message',target=NULL WHERE id=?`, duplicate); e != nil {
		t.Fatal(e)
	}
	if _, e = roomCauseIn(w.bob.store.db, conv, ask.LID, w.bob.Address, w.bob.Self().Fingerprint()); e != nil {
		t.Fatalf("ordinary collision stalled addressed request: %v", e)
	}
	if _, e = w.bob.store.db.Exec(`DELETE FROM inbox WHERE id=?`, duplicate); e != nil {
		t.Fatal(e)
	}
	for _, change := range []string{"key", "cause", "epoch", "target"} {
		t.Run(change, func(t *testing.T) {
			r := agentReq{ID: childIDForP6(t, w.bob, ask.LID), Sender: original.from, Key: original.key, Conv: conv, PID: to.PID, Kind: original.kind, Target: original.target, State: stateAccepted}
			var old string
			query := ""
			value := ""
			switch change {
			case "key":
				query = "verified_by"
				value = strings.Repeat("f", 8) + "-" + strings.Repeat("f", 8) + "-" + strings.Repeat("f", 8) + "-" + strings.Repeat("f", 8)
			case "cause":
				query = "reply_to"
				value = protocol.NewID()
			case "target":
				r.Target = &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: protocol.NewID()}
			case "epoch":
				if _, e := w.bob.store.db.Exec(`UPDATE inbox SET target=? WHERE lid=?`, `{"address":"`+w.alice.Address+`","fingerprint":"`+w.alice.Self().Fingerprint()+`","group_admission":"`+strings.Repeat("e", 64)+`"}`, root.LID); e != nil {
					t.Fatal(e)
				}
				defer func() {
					raw := targetJSONForP6(t, w.alice, root.LID)
					w.bob.store.db.Exec(`UPDATE inbox SET target=? WHERE lid=?`, raw, root.LID)
				}()
			}
			if query != "" {
				if e := w.bob.store.db.QueryRow(`SELECT `+query+` FROM inbox WHERE id=?`, r.ID).Scan(&old); e != nil {
					t.Fatal(e)
				}
				if _, e := w.bob.store.db.Exec(`UPDATE inbox SET `+query+`=? WHERE id=?`, value, r.ID); e != nil {
					t.Fatal(e)
				}
				defer w.bob.store.db.Exec(`UPDATE inbox SET `+query+`=? WHERE id=?`, old, r.ID)
			}
			v, _, e := agentVerdict(w.bob.store.db, r, w.bob.Address, w.bob.Self().Fingerprint(), false, map[string]*partView{})
			if e != nil {
				t.Fatal(e)
			}
			if v == verdictRun {
				t.Fatal("forged origin or changed target ran after accept")
			}
		})
	}
	if _, e = w.alice.DismissParticipation(tctx(t), from.PID); e != nil {
		t.Fatal(e)
	}
	eventually(t, "ended source", func() bool { return stateAt(t, w.bob, from.PID).State == PartDismissed })
	r := agentReq{ID: childIDForP6(t, w.bob, ask.LID), Sender: original.from, Key: original.key, Conv: conv, PID: to.PID, Kind: original.kind, Target: original.target, State: stateAccepted}
	v, _, e := agentVerdict(w.bob.store.db, r, w.bob.Address, w.bob.Self().Fingerprint(), false, map[string]*partView{})
	if e != nil || v == verdictRun {
		t.Fatalf("ended source: %d %v", v, e)
	}
}
func targetJSONForP6(t *testing.T, a *Agent, lid string) string {
	t.Helper()
	var raw string
	if e := a.store.db.QueryRow(`SELECT target FROM outbox WHERE lid=? LIMIT 1`, lid).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	return raw
}

func p6HasLID(t *testing.T, a *Agent, lid string) bool {
	t.Helper()
	var n int
	if e := a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE lid=?`, lid).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n > 0
}

func TestP6InviterLeavesWithoutRemovingAgent(t *testing.T) {
	w, carol, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	p := p6Member(t, carol, w.bob, conv)
	eventually(t, "group has accepted room membership", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	if _, e := carol.LeaveGroup(tctx(t), conv); e != nil {
		t.Fatal(e)
	}
	eventually(t, "departure applied", func() bool { m, e := w.bob.GroupMembers(conv); return e == nil && len(m) == 2 })
	for _, a := range []*Agent{w.alice, w.bob} {
		got := stateAt(t, a, p.PID)
		if !got.Member || !got.Claimable() {
			t.Fatalf("inviter departure removed agent: %+v", got)
		}
	}
	stops[w.bob]()
	home := w.bob.home
	w.bob.Close()
	host, e := Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	if got := stateAt(t, host, p.PID); !got.Claimable() {
		t.Fatalf("consent lost on restart: %+v", got)
	}
	stub := installAgentStub(t)
	if e = host.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); e != nil {
		t.Fatal(e)
	}
	if e = host.Approve(w.alice.Address); e != nil {
		t.Fatal(e)
	}
	runAgent(t, host)
	q, e := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "agent remains after inviter departure")
	if e != nil {
		t.Fatal(e)
	}
	replyAt(t, w.alice, conv, q.LID)
	if _, e = w.alice.DismissParticipation(tctx(t), p.PID); e != nil {
		t.Fatal(e)
	}
	eventually(t, "explicit removal still counts", func() bool { return stateAt(t, host, p.PID).State == PartDismissed })
}
