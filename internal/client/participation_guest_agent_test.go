package client

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A selected excerpt is shown under the original logical ID. A guest must be
// able to reply to that visible message, without acquiring unselected history.
func TestTopicGuestReplyToSelectedContext(t *testing.T) {
	t.Run("DM", func(t *testing.T) {
		w, guest, conv, _, _ := humanWorld(t)
		testTopicGuestReplyToSelectedContext(t, w, guest, conv)
	})
	t.Run("group", func(t *testing.T) {
		w, carol, packet, _ := groupTurnsFixture(t)
		guest := proofReader(t, w, "topicguest")
		runAgent(t, guest)
		publishGroupFixtureCaps(t, guest, true)
		for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
			roomReader(t, a)
		}
		testTopicGuestReplyToSelectedContext(t, w, guest, packet.State.Conv)
	})
}

func testTopicGuestReplyToSelectedContext(t *testing.T, w *world, guest *Agent, conv string) {
	t.Helper()
	topic := protocol.NewID()
	hidden, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "UNSELECTED_GUEST_PARENT", Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "SELECTED_GUEST_PARENT", Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteHumanInScope(tctx(t), conv, guest.Address, []string{selected.LID}, "", &topic)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "scoped guest invite", func() bool { return stateAt(t, guest, p.PID).State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "selected guest parent", func() bool { return humanBodyCount(t, guest, conv, "SELECTED_GUEST_PARENT") == 1 })
	msgs, err := guest.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var carrierID string
	for _, m := range msgs {
		if m.ExcerptPID == p.PID && m.LID == selected.LID {
			carrierID = m.ID
		}
	}
	if carrierID == "" || carrierID == selected.LID {
		t.Fatal("fixture needs distinct excerpt carrier and original identities")
	}
	for i, ref := range []string{selected.LID, carrierID} {
		body := fmt.Sprintf("GUEST_SELECTED_REPLY_%d", i)
		if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{Body: body, PID: p.PID, ReplyTo: ref}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "guest selected reply at members", func() bool {
			for _, a := range []*Agent{w.alice, w.bob} {
				rows, e := a.ConversationMessages(conv)
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, m := range rows {
					found = found || m.Body == body && m.ReplyTo == selected.LID && m.Topic == topic
				}
				if !found {
					return false
				}
			}
			return true
		})
	}
	for _, ref := range []string{hidden.LID, protocol.NewID()} {
		if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{Body: "NO_UNSELECTED_PARENT", Topic: topic, ReplyTo: ref}); err == nil {
			t.Fatal("unselected or missing parent accepted")
		}
	}
	var raw string
	if err = guest.store.db.QueryRow(`SELECT body FROM inbox WHERE id=?`, carrierID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original HistoryItem
	if err = json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []HistoryItem{
		func() HistoryItem { h := original; h.LID = hidden.LID; return h }(),
		func() HistoryItem { h := original; h.FromKey = w.bob.Self().Fingerprint(); return h }(),
		func() HistoryItem { h := original; h.Topic = protocol.NewID(); return h }(),
	} {
		data, _ := json.Marshal(bad)
		if _, err = guest.store.db.Exec(`UPDATE inbox SET body=? WHERE id=?`, string(data), carrierID); err != nil {
			t.Fatal(err)
		}
		if _, _, err = humanReplyParent(guest.store.db, conv, carrierID); err == nil {
			t.Fatal("forged or wrong-topic selected parent accepted")
		}
	}
	if _, err = guest.store.db.Exec(`UPDATE inbox SET body=? WHERE id=?`, raw, carrierID); err != nil {
		t.Fatal(err)
	}
}

func TestTopicGuestInvitesOwnAgent(t *testing.T) {
	t.Run("DM", func(t *testing.T) {
		w, guest, conv, _, stub := humanWorld(t)
		testTopicGuestInvitesOwnAgent(t, w, guest, conv, stub)
	})
	t.Run("group", func(t *testing.T) {
		stub := installAgentStub(t)
		w, carol, packet, _ := groupTurnsFixture(t)
		guest := proofReader(t, w, "ownguest")
		runAgent(t, guest)
		publishGroupFixtureCaps(t, guest, true)
		for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
			roomReader(t, a)
		}
		testTopicGuestInvitesOwnAgent(t, w, guest, packet.State.Conv, stub)
	})
}

func testTopicGuestInvitesOwnAgent(t *testing.T, w *world, guest *Agent, conv string, stub *agentStub) {
	t.Helper()
	host := linkedVia(t, guest, "agent-host", guest.ApproveAgentLink)
	if err := host.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	humanTestCaps(t, host)
	if err := host.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	owner, _, _ := guest.store.selfPerson(guest.Address)
	for _, a := range []*Agent{w.alice, w.bob} {
		if _, err := a.refreshPerson(tctx(t), owner.info.Person, false); err != nil {
			t.Fatal(err)
		}
	}
	topic := protocol.NewID()
	hidden, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "GUEST_CHILD_PRIVATE", Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "GUEST_CHILD_SELECTED", Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteHumanInScope(tctx(t), conv, guest.Address, []string{selected.LID}, "", &topic)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "scoped guest invite", func() bool { return stateAt(t, guest, p.PID).State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest selected history", func() bool { return humanBodyCount(t, guest, conv, "GUEST_CHILD_SELECTED") == 1 })
	if _, err = guest.InviteAgentInScope(tctx(t), conv, host.Address, "", []string{hidden.LID}, nil, "", &topic); err == nil {
		t.Fatal("guest shared unselected private history")
	}
	child, err := guest.InviteAgentInScope(tctx(t), conv, host.Address, "", []string{selected.LID}, nil, "", &topic)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, a := range []*Agent{guest, host, w.alice} {
			x, e := a.Participation(child.PID)
			t.Logf("derived at %s state=%s held=%d parent=%s member=%v error=%v", a.Address, x.State, x.Held, x.Parent, x.Member, e)
		}
		for _, sender := range []*Agent{guest, host} {
			rows, e := sender.store.db.Query(`SELECT recipient,sub,state,coalesce(error,'') FROM outbox WHERE conv=? AND (pid=? OR sub IN ('group-proof','group-context'))`, conv, child.PID)
			if e != nil {
				return
			}
			for rows.Next() {
				var address, sub, state, why string
				if rows.Scan(&address, &sub, &state, &why) == nil {
					t.Logf("derived copy from %s to %s %s %s %s", sender.Address, address, sub, state, why)
				}
			}
			rows.Close()
		}
		rows, e := host.store.db.Query(`SELECT id,kind,state,coalesce(error,'') FROM inbox WHERE conv=? AND pid=?`, conv, child.PID)
		if e == nil {
			for rows.Next() {
				var id, kind, state, why string
				if rows.Scan(&id, &kind, &state, &why) == nil {
					t.Logf("host inbox %s %s %s %s", id, kind, state, why)
				}
			}
			rows.Close()
		}
	})
	for _, a := range []*Agent{w.alice, w.bob, host} {
		eventually(t, "derived helper active at "+a.Address, func() bool {
			p, e := a.Participation(child.PID)
			return e == nil && p.Claimable() && p.Parent != "" && !p.Member
		})
	}
	eventually(t, "derived selected context", func() bool { return humanBodyCount(t, host, conv, "GUEST_CHILD_SELECTED") == 1 })
	if humanBodyCount(t, host, conv, "GUEST_CHILD_PRIVATE") != 0 {
		t.Fatal("private history backfilled to derived host")
	}
	// Scope-only readers never obtain child private grants and keep its exact parent.
	evs, e := w.alice.store.participationEvents(conv, child.PID)
	if e != nil {
		t.Fatal(e)
	}
	var public []protocol.ParticipationEvent
	for _, ev := range evs {
		if ev.Type != protocol.EventInvite {
			public = append(public, ev)
		}
	}
	m, e := w.alice.dmMembers(conv)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.loadHosts(w.alice.store.db, public); e != nil {
		t.Fatal(e)
	}
	scopeOnly := resolve(conv, child.PID, public, m)
	if scopeOnly.Parent != child.Parent || scopeOnly.Member || !scopeOnly.Claimable() || len(scopeOnly.Grant) != 0 {
		t.Fatalf("scope-only derived authority: %+v", scopeOnly)
	}
	accepted := stateAt(t, guest, p.PID)
	if child.Parent != accepted.Decision || child.Member {
		t.Fatalf("wrong parent binding: %+v", child)
	}
	ask, err := w.alice.AskAgentInTopic(tctx(t), child.PID, envelope.KindQuestion, "GUEST_AGENT_QUESTION", topic, nil)
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, ask.ID)
	if stub.runs() != 1 {
		t.Fatalf("runs=%d", stub.runs())
	}
	if _, err = w.alice.AskAgent(tctx(t), child.PID, envelope.KindQuestion, "PRIVATE_MAIN"); err == nil {
		t.Fatal("derived helper expanded into Main")
	}
	if _, err = w.alice.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside own host receives exact parent end", func() bool {
		x, e := host.Participation(child.PID)
		return e == nil && !x.Claimable() && x.State == PartDismissed
	})
	if _, err = w.alice.AskAgentInTopic(tctx(t), child.PID, envelope.KindQuestion, "AFTER_PARENT_END", topic, nil); err == nil {
		t.Fatal("ended parent still grants request authority")
	}
	m, e = w.alice.dmMembers(conv)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.loadHosts(w.alice.store.db, public); e != nil {
		t.Fatal(e)
	}
	scopeOnly = resolve(conv, child.PID, public, m)
	if scopeOnly.Parent != child.Parent || scopeOnly.Member || scopeOnly.State != PartDismissed || scopeOnly.Claimable() {
		t.Fatalf("scope-only parent end lost: %+v", scopeOnly)
	}
	reinvite, e := w.alice.InviteHumanInScope(tctx(t), conv, guest.Address, nil, "new exact invitation", &topic)
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "new parent invitation", func() bool { return stateAt(t, guest, reinvite.PID).State == PartInvited })
	if _, e = guest.AcceptParticipation(tctx(t), reinvite.PID); e != nil {
		t.Fatal(e)
	}
	if old, e := w.alice.Participation(child.PID); e != nil || old.Claimable() || old.Parent != child.Parent {
		t.Fatalf("reinvitation revived old child: %+v %v", old, e)
	}
}
