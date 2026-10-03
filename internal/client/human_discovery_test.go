package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type sharedCopy struct {
	id, state, body, fp, required string
	env                           envelope.Envelope
	ev                            protocol.ParticipationEvent
}

// humanSharedCopies are a's event copies of pid's lifecycle sent to address.
func humanSharedCopies(t *testing.T, a *Agent, conv, pid, address string) []sharedCopy {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT id,state,body,envelope,coalesce(recipient_fp,''),coalesce(required_cap,'') FROM outbox WHERE conv=? AND pid=? AND sub=? AND recipient=? ORDER BY created_ms,id`, conv, pid, envelope.SubEvent, address)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sharedCopy
	for rows.Next() {
		var c sharedCopy
		var raw string
		if err := rows.Scan(&c.id, &c.state, &c.body, &raw, &c.fp, &c.required); err != nil {
			t.Fatal(err)
		}
		json.Unmarshal([]byte(raw), &c.env)
		c.ev, _ = protocol.ParseParticipationEvent([]byte(c.body))
		out = append(out, c)
	}
	return out
}

func sharedOf(copies []sharedCopy, typ string) (sharedCopy, bool) {
	for _, c := range copies {
		if c.ev.Type == typ {
			return c, true
		}
	}
	return sharedCopy{}, false
}

// twoGuests: Carol invited by Alice, Dana by Bob, Dana accepted first.
func twoGuests(t *testing.T) (w *world, carol, dana *Agent, conv string, pc, pd ParticipationInfo, stopDana func(), stub *agentStub) {
	t.Helper()
	w, carol, conv, _, stub = humanWorld(t)
	dana = mustJoin(t, filepath.Join(t.TempDir(), "dana"), w.aliceInvites("dana"), "guest")
	stopDana = runAgent(t, dana)
	persons(t, dana)
	humanTestCaps(t, dana)
	fakeNotify(dana)
	var err error
	if pc, err = w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, ""); err != nil {
		t.Fatal(err)
	}
	if pd, err = w.bob.InviteHuman(tctx(t), conv, dana.Address, nil, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both invitations", func() bool {
		return stateAt(t, carol, pc.PID).State == PartInvited && stateAt(t, dana, pd.PID).State == PartInvited
	})
	if _, err = dana.AcceptParticipation(tctx(t), pd.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Dana active at both originals", func() bool {
		return stateAt(t, w.alice, pd.PID).HumanActive() && stateAt(t, w.bob, pd.PID).HumanActive()
	})
	return
}

// Accepted guests learn each other from the originals' exact signed proof,
// before any original speaks; reordered arrival holds, a lone invitation is
// inert, copies are captured once per guest key, and guests then exchange
// text, files and logical replies directly. Nothing runs.
func TestHumanGuestsDiscoverEachOtherBeforeAnyOriginalTurn(t *testing.T) {
	w, carol, dana, conv, pc, pd, stopDana, stub := twoGuests(t)
	stopDana() // the shared proof waits for Dana at the Hub
	if _, err := carol.AcceptParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	for _, o := range []*Agent{w.alice, w.bob} {
		eventually(t, "each original shares both directions", func() bool {
			return len(humanSharedCopies(t, o, conv, pc.PID, dana.Address)) == 2 && len(humanSharedCopies(t, o, conv, pd.PID, carol.Address)) == 2
		})
		for _, c := range append(humanSharedCopies(t, o, conv, pc.PID, dana.Address), humanSharedCopies(t, o, conv, pd.PID, carol.Address)...) {
			to := dana
			if c.ev.PID == pd.PID {
				to = carol
			}
			if c.required != protocol.CapHumanParticipation || c.fp != to.Self().Fingerprint() || c.ev.Type == protocol.EventDecline {
				t.Fatalf("shared copy %+v", c)
			}
		}
		before := count(t, o, "outbox")
		o.discloseHumanAudience(tctx(t))
		o.discloseHumanAudience(tctx(t))
		if count(t, o, "outbox") != before {
			t.Fatal("repeated disclosure queued again")
		}
	}
	eventually(t, "Carol knows Dana", func() bool { return stateAt(t, carol, pd.PID).HumanActive() })

	// Reordered at Dana: acceptance first is held; the public scope alone is
	// inert. The private invitation itself never goes to the other guest.
	copies := humanSharedCopies(t, w.alice, conv, pc.PID, dana.Address)
	invite, iok := sharedOf(copies, protocol.EventScope)
	accept, aok := sharedOf(copies, protocol.EventAccept)
	if _, leaked := sharedOf(copies, protocol.EventInvite); !iok || !aok || leaked {
		t.Fatalf("shared pair %+v", copies)
	}
	var invites int
	dana.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND pid=? AND type=?`, conv, pc.PID, protocol.EventInvite).Scan(&invites)
	if invites != 0 {
		t.Fatal("other guest holds Carol's private invitation")
	}
	if err := dana.accept(tctx(t), accept.env); err != nil {
		t.Fatal(err)
	}
	var held int
	dana.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=? AND reason=?`, accept.env.ID, reasonProof).Scan(&held)
	if held != 1 || stateAt(t, dana, pc.PID).HumanActive() {
		t.Fatalf("acceptance before invitation held=%d", held)
	}
	if err := dana.accept(tctx(t), invite.env); err != nil {
		t.Fatal(err)
	}
	if p := stateAt(t, dana, pc.PID); p.State != PartInvited || p.HumanActive() {
		t.Fatalf("lone invitation %+v", p)
	}
	if h, err := dana.humanPlan(tctx(t), conv, pd.PID); err != nil || len(h.Audience) != 1 || h.Audience[0].PID != pd.PID {
		t.Fatalf("inert invitation widened audience: %+v %v", h, err)
	}
	dana.retryProof(tctx(t))
	if !stateAt(t, dana, pc.PID).HumanActive() {
		t.Fatal("verified pair did not activate after reorder")
	}
	runAgent(t, dana) // restart: the Hub's and Bob's copies arrive again, idempotently
	eventually(t, "restarted Dana's fixture capability", func() bool {
		humanTestCaps(t, dana)
		return carol.requireParticipationCaps(tctx(t), dana.Self(), protocol.CapHumanParticipation) == nil
	})
	if !stateAt(t, dana, pc.PID).HumanActive() {
		t.Fatal("restart changed shared participation")
	}

	// Guests exchange text and files directly; no original turn was sent.
	file := filepath.Join(t.TempDir(), "dana.txt")
	os.WriteFile(file, []byte("SYNTHETIC_GUEST_TO_GUEST_BYTES"), 0o600)
	first, err := dana.SendConv(tctx(t), conv, ConvOutgoing{PID: pd.PID, Body: "Dana to Carol first", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for _, c := range first.Copies {
		reached = reached || c.To == carol.Address
	}
	if !reached {
		t.Fatalf("no copy for the other guest: %+v", first.Copies)
	}
	var atCarol ConvMessage
	eventually(t, "guest text and file reach the other guest", func() bool {
		msgs, _ := carol.ConversationMessages(conv)
		for _, m := range msgs {
			if m.Body == "Dana to Carol first" && len(m.Attachments) == 1 {
				atCarol = m
				return humanBodyCount(t, w.alice, conv, "Dana to Carol first") == 1 && humanBodyCount(t, w.bob, conv, "Dana to Carol first") == 1
			}
		}
		return false
	})
	if atCarol.ID == first.LID || atCarol.LID != first.LID {
		t.Fatalf("fixture needs a physical copy id: %+v", atCarol)
	}
	// Carol replies naming her own physical copy: every peer gets the logical parent.
	if _, err = carol.SendConv(tctx(t), conv, ConvOutgoing{PID: pc.PID, Body: "Carol replies", ReplyTo: atCarol.ID}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{dana, w.alice, w.bob} {
		eventually(t, "logical reply parent at "+a.Address, func() bool {
			msgs, _ := a.ConversationMessages(conv)
			for _, m := range msgs {
				if m.Body == "Carol replies" {
					return m.ReplyTo == first.LID
				}
			}
			return false
		})
	}
	if _, err = carol.SendConv(tctx(t), conv, ConvOutgoing{PID: pc.PID, Body: "unknown parent", ReplyTo: protocol.NewID()}); err == nil {
		t.Fatal("reply to an unknown parent sent")
	}
	// A held physical copy id is an alias: it maps to the logical parent, so
	// ingress refuses a reply naming it; a never-held parent stays opaque.
	if parent, known, err := humanReplyParent(dana.store.db, conv, atCarol.ID); !known || err != nil || parent != first.LID {
		t.Fatalf("physical alias %q %v %v", parent, known, err)
	}
	if parent, known, err := humanReplyParent(dana.store.db, conv, protocol.NewID()); known || err != nil || parent != "" {
		t.Fatalf("unknown parent %q %v %v", parent, known, err)
	}
	outside := envelope.Inner{ID: protocol.NewID(), From: carol.Address, To: dana.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "outside"}
	if err = dana.store.addInbox(outside, carol.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = humanReplyParent(dana.store.db, conv, outside.ID); err == nil {
		t.Fatal("parent outside the conversation accepted")
	}
	if stub.runs() != 0 {
		t.Fatal("human lifecycle triggered a model")
	}
}

// An end reaches every guest the acceptance may have reached, even when its
// receipt was lost; a queued acceptance is never sent after the local end and
// a replayed acceptance cannot undo it.
func TestHumanSharedEndAfterLostAcceptanceReceiptAndReplay(t *testing.T) {
	w, carol, dana, conv, pc, pd, _, stub := twoGuests(t)
	if _, err := carol.AcceptParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guests know each other", func() bool {
		return stateAt(t, dana, pc.PID).HumanActive() && stateAt(t, carol, pd.PID).HumanActive()
	})
	accept, ok := sharedOf(humanSharedCopies(t, w.alice, conv, pc.PID, dana.Address), protocol.EventAccept)
	if !ok {
		t.Fatal("Alice shared no acceptance")
	}
	// Lost receipt: the shared acceptance still looks queued at Alice.
	if _, err := w.alice.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, accept.id); err != nil {
		t.Fatal(err)
	}
	if _, err := carol.DismissParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Alice shares Carol's own end with Dana", func() bool {
		end, ok := sharedOf(humanSharedCopies(t, w.alice, conv, pc.PID, dana.Address), protocol.EventDismiss)
		return ok && end.ev.Author.Address == carol.Address && end.fp == dana.Self().Fingerprint()
	})
	eventually(t, "Dana applies the end", func() bool { return stateAt(t, dana, pc.PID).State == PartDismissed })
	handled, allowed, err := w.alice.mayDeliverExternal(accept.env)
	if err != nil || !handled || allowed {
		t.Fatalf("stale acceptance escaped: %v %v %v", handled, allowed, err)
	}
	if state, _, _, _ := w.alice.store.outboxState(accept.id); state != stateNotDelivered {
		t.Fatalf("stale acceptance state %s", state)
	}

	// A fresh copy of the old acceptance from an original cannot resurrect it.
	_, root, _, err := w.bob.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.bob.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.bob.Address, To: dana.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
		Body: accept.body, Conv: conv, LID: protocol.NewID(), Root: root, PID: pc.PID, Sub: envelope.SubEvent, Origin: envelope.OriginUI}
	for _, mem := range m.root.Members {
		in.Fan = append(in.Fan, envelope.Fan{Person: mem.Person, Roster: m.persons[mem.Person].info.Roster})
	}
	recipient, err := dana.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	replay, err := envelope.Seal(in, w.bob.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = dana.accept(tctx(t), replay); err != nil {
		t.Fatal(err)
	}
	if p := stateAt(t, dana, pc.PID); p.State != PartDismissed {
		t.Fatalf("replayed acceptance resurrected %+v", p)
	}
	after, err := dana.SendConv(tctx(t), conv, ConvOutgoing{PID: pd.PID, Body: "after Carol left"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range after.Copies {
		if c.To == carol.Address {
			t.Fatal("remaining guest sent to departed guest")
		}
	}
	if stub.runs() != 0 {
		t.Fatal("human lifecycle triggered a model")
	}
}
