package client

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestHumanMultipleGuestsOriginalEndAndCapturedQueueFence(t *testing.T) {
	testHumanMultipleEnd(t, false)
}
func TestHumanMultipleGuestsSelfLeaveAndCapturedQueueFence(t *testing.T) {
	testHumanMultipleEnd(t, true)
}
func testHumanMultipleEnd(t *testing.T, selfLeave bool) {
	w, carol, conv, _, stub := humanWorld(t)
	dana := mustJoin(t, filepath.Join(t.TempDir(), "dana"), w.aliceInvites("dana"), "guest")
	runAgent(t, dana)
	persons(t, dana)
	humanTestCaps(t, dana)
	fakeNotify(dana)
	pc, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "Carol invitation", func() bool { return stateAt(t, carol, pc.PID).State == PartInvited })
	if _, err = carol.AcceptParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Carol active at inviter", func() bool { return stateAt(t, w.alice, pc.PID).HumanActive() })
	pd, err := w.bob.InviteHuman(tctx(t), conv, dana.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "Dana invitation", func() bool { return stateAt(t, dana, pd.PID).State == PartInvited })
	if _, err = dana.AcceptParticipation(tctx(t), pd.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Dana active at Alice", func() bool { return stateAt(t, w.alice, pd.PID).HumanActive() })
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "two human audience"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "same turn at both guests", func() bool {
		return humanBodyCount(t, carol, conv, "two human audience") == 1 && humanBodyCount(t, dana, conv, "two human audience") == 1
	})
	if _, err = carol.SendConv(tctx(t), conv, ConvOutgoing{PID: pc.PID, Body: "Carol to everyone"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest turn reaches other guest once", func() bool {
		return humanBodyCount(t, dana, conv, "Carol to everyone") == 1 && humanBodyCount(t, w.alice, conv, "Carol to everyone") == 1 && humanBodyCount(t, w.bob, conv, "Carol to everyone") == 1
	})
	queuedGuest, err := dana.SendConv(tctx(t), conv, ConvOutgoing{PID: pd.PID, Body: "remaining guest before end"})
	if err != nil {
		t.Fatal(err)
	}
	var guestID string
	for _, copy := range queuedGuest.Copies {
		if copy.To == carol.Address {
			guestID = copy.ID
		}
	}
	if guestID == "" {
		t.Fatal("remaining guest had no pre-end captured copy")
	}
	// Clone an actually sealed queued copy with its captured scope. No network
	// send occurs until after the local end; its immutable old audience must stop.
	var id, raw, humanRaw, fp string
	if err = w.alice.store.db.QueryRow(`SELECT id,envelope,human,recipient_fp FROM outbox WHERE conv=? AND recipient=? AND human IS NOT NULL LIMIT 1`, conv, carol.Address).Scan(&id, &raw, &humanRaw, &fp); err != nil {
		t.Fatal(err)
	}
	var queued envelope.Envelope
	json.Unmarshal([]byte(raw), &queued)
	ender := w.alice
	if selfLeave {
		ender = carol
	}
	if _, err = ender.DismissParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "original observes guest end", func() bool { return stateAt(t, w.alice, pc.PID).State == PartDismissed })
	var beforeEndCopies, afterEndCopies int
	ender.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=?`, conv, pc.PID, envelope.SubEvent).Scan(&beforeEndCopies)
	if _, err = ender.DismissParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	ender.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=?`, conv, pc.PID, envelope.SubEvent).Scan(&afterEndCopies)
	if afterEndCopies != beforeEndCopies {
		t.Fatal("end retry recreated control audience")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, id); err != nil {
		t.Fatal(err)
	}
	handled, allowed, err := w.alice.mayDeliverExternal(queued)
	if err != nil || !handled || allowed {
		t.Fatalf("ended queued copy escaped: %v %v %v", handled, allowed, err)
	}
	state, _, _, _ := w.alice.store.outboxState(id)
	if state != stateNotDelivered {
		t.Fatal("ended scope not fenced durably")
	}
	if humanRaw == "" || fp == "" {
		t.Fatal("scope/key not captured")
	}
	eventually(t, "Carol and remaining guest observe original member end", func() bool {
		return stateAt(t, carol, pc.PID).State == PartDismissed && stateAt(t, dana, pc.PID).State == PartDismissed
	})
	var guestRaw, beforeScope, afterScope string
	if err = dana.store.db.QueryRow(`SELECT envelope,human FROM outbox WHERE id=?`, guestID).Scan(&guestRaw, &beforeScope); err != nil {
		t.Fatal(err)
	}
	var guestQueued envelope.Envelope
	json.Unmarshal([]byte(guestRaw), &guestQueued)
	dana.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, guestID)
	h, g, e := dana.mayDeliverExternal(guestQueued)
	if e != nil || !h || g {
		t.Fatalf("remaining guest queued retry bypassed end: %v %v %v", h, g, e)
	}
	dana.store.db.QueryRow(`SELECT human FROM outbox WHERE id=?`, guestID).Scan(&afterScope)
	if beforeScope != afterScope {
		t.Fatal("retry rewrote captured scope")
	}
	file := filepath.Join(t.TempDir(), "remaining-guest.txt")
	os.WriteFile(file, []byte("SYNTHETIC_REMAINING_GUEST_PRIVATE_BYTES"), 0600)
	remaining, err := dana.SendConv(tctx(t), conv, ConvOutgoing{PID: pd.PID, Body: "remaining guest after end", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range remaining.Copies {
		if copy.To == carol.Address {
			t.Fatal("remaining guest queued text/file to ended guest")
		}
	}
	eventually(t, "remaining guest turn reaches originals", func() bool {
		return humanBodyCount(t, w.alice, conv, "remaining guest after end") == 1 && humanBodyCount(t, w.bob, conv, "remaining guest after end") == 1
	})
	if humanBodyCount(t, carol, conv, "remaining guest after end") != 0 {
		t.Fatal("ended guest received remaining guest text/file")
	}
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Dana remains"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "remaining human gets turn", func() bool { return humanBodyCount(t, dana, conv, "Dana remains") == 1 })
	if humanBodyCount(t, carol, conv, "Dana remains") != 0 {
		t.Fatal("ended human still audience")
	}
	if _, err = dana.DismissParticipation(tctx(t), pd.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both originals private", func() bool { return stateAt(t, w.alice, pd.PID).State == PartDismissed })
	private, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "only original private flow"})
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range private.Copies {
		if copy.To == carol.Address || copy.To == dana.Address {
			t.Fatal("private traffic has guest copy")
		}
	}
	if stub.runs() != 0 {
		t.Fatal("human triggered model")
	}
}
func TestHumanOriginalEndDominatesDelayedAcceptance(t *testing.T) {
	w, carol, conv, _, _ := humanWorld(t)
	p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invitation at guest", func() bool { return stateAt(t, carol, p.PID).State == PartInvited })
	if _, err = w.alice.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	// A properly signed late acceptance cannot reactivate the ended ledger.
	self, _, _ := carol.store.selfPerson(carol.Address)
	accept := protocol.ParticipationEvent{V: 1, Conv: conv, PID: p.PID, Type: protocol.EventAccept, Prev: p.Invite, TS: 1790000000, Author: protocol.EventAuthor{Person: self.info.Person, Roster: self.info.Roster, Address: carol.Address, Fingerprint: carol.Self().Fingerprint()}}
	accept.Sign(carol.id.Sign)
	raw, _ := json.Marshal(accept)
	if err = w.alice.store.addParticipationEvent(accept, raw); err != nil {
		t.Fatal(err)
	}
	if stateAt(t, w.alice, p.PID).State != PartDismissed {
		t.Fatal("late acceptance reactivated")
	}
	if _, err = w.alice.humanPlan(tctx(t), conv, p.PID); err == nil {
		t.Fatal("ended author got new send plan")
	}
}
