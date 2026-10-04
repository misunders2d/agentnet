package client

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// BUG-24: a question held for the person in a DM (conv_held: nothing runs
// it) closes when the person replies in that conversation, and resolve
// closes one without replying; before, it stayed in review (and doctor's
// count) for good, and resolve, decline and accept all refused it.
func TestHeldTurnClosesOnReply(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	held := func(body string) string {
		t.Helper()
		if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: body}); err != nil {
			t.Fatal(err)
		}
		var id string
		eventually(t, body+" held at bob", func() bool {
			w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND body = ? AND state = ?`, conv, body, stateConvHeld).Scan(&id)
			return id != ""
		})
		return id
	}
	inReview := func(id string) bool {
		review, err := w.bob.Review()
		if err != nil {
			t.Fatal(err)
		}
		p, err := w.bob.PageReview()
		if err != nil {
			t.Fatal(err)
		}
		listed := slices.ContainsFunc(review, func(m Message) bool { return m.ID == id })
		if page := slices.ContainsFunc(p.Held, func(r ConvReview) bool { return r.ID == id }); page != listed {
			t.Fatalf("inbox --review lists %s: %v, the page: %v", id, listed, page)
		}
		return listed
	}
	q := held("decide please: split the shipment?")
	if !inReview(q) {
		t.Fatal("setup: the held question is not in review")
	}
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "Yes, split it"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.bob.store.jobState(q); s != stateManual || inReview(q) {
		t.Fatalf("answered in the conversation, the question is %s (in review %v)", s, inReview(q))
	}
	// Closed without replying: resolve, which sends nothing.
	q2 := held("and the second pallet?")
	if err := w.bob.Resolve(q2); err != nil {
		t.Fatalf("resolve a held question: %v", err)
	}
	if s, _ := w.bob.store.jobState(q2); s != stateResolved || inReview(q2) {
		t.Fatalf("resolved, the question is %s (in review %v)", s, inReview(q2))
	}
	time.Sleep(300 * time.Millisecond)
	if n := inboxCount(t, w.bob, `status_due > 0`); n != 0 {
		t.Fatalf("a status was noted for a person's turn (%d)", n)
	}
	if n := count(t, w.alice, "quarantine"); n != 0 {
		t.Fatalf("alice holds %d message(s): something about a person's turn was sent", n)
	}
	// An agent's output or a request to an agent is no reply of the person.
	q3 := held("third question")
	in := envelope.Inner{Conv: conv, Kind: envelope.KindAnswer, Origin: envelope.OriginAgentPrefix + "claude"}
	tx, _ := w.bob.store.db.Begin()
	if err := turnClosesHeld(tx, in, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	in = envelope.Inner{Conv: conv, Kind: envelope.KindQuestion, Origin: envelope.OriginUI, Target: &envelope.Target{Address: w.bob.Address}}
	if err := turnClosesHeld(tx, in, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if s, _ := w.bob.store.jobState(q3); s != stateConvHeld {
		t.Fatalf("an agent's turn closed a held question: %s", s)
	}
}

// A turn of the person's from another of their devices closes what was
// held for them here, but only what had reached this device when they
// wrote it: one written earlier and delivered late (a phone that was
// offline) answers nothing that arrived after it (review finding 1).
func TestHeldTurnClosesOnlyWhatPrecededIt(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "hi bob"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the conversation at bob", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	phone := linked(t, w.bob)
	eventually(t, "the conversation on bob's phone", func() bool { return len(convBodies(t, phone, conv)) == 1 })
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "split the shipment?"}); err != nil {
		t.Fatal(err)
	}
	var q string
	eventually(t, "the question held at bob's laptop", func() bool {
		w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND state = ?`, conv, stateConvHeld).Scan(&q)
		return q != ""
	})
	// The question reached the laptop two seconds ago (so that the phone's
	// answer below is certainly written after it); a turn written on the
	// phone a minute before is delivered only now.
	arrived := time.Now().Add(-2 * time.Second)
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET received_at = ?, received_ms = ? WHERE id = ?`, arrived.Unix(), arrived.UnixMilli(), q); err != nil {
		t.Fatal(err)
	}
	late := envelope.Inner{ID: protocol.NewID(), From: phone.Address, Conv: conv, Kind: envelope.KindMessage, Body: "hello?", TS: arrived.Unix() - 60}
	tx, err := w.bob.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := turnClosesHeld(tx, late, late.TS*1000); err != nil { // as conv.go passes a turn from another device
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.bob.store.jobState(q); s != stateConvHeld {
		t.Fatalf("a turn written before the question arrived closed it: %s", s)
	}
	// The person answers on the phone: the laptop closes it.
	if _, err := phone.SendConv(tctx(t), conv, ConvOutgoing{Body: "Yes, split it"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the phone's answer to close the question on the laptop", func() bool {
		s, _ := w.bob.store.jobState(q)
		return s == stateManual
	})
}

// BUG-24: inbox --review and doctor list what else waits here, apart from
// review items: a request to this device's agent that has not run (and why:
// no responder chosen), invitations for its agent and for its person to a
// group, a device asking to be linked, and messages held back (one for a
// changed key; not one only waiting for proof). Before, review and doctor
// said nothing waited.
func TestWaitingListsWhatElseWaits(t *testing.T) {
	w, conv, _, _ := agentWorld(t) // bob chose no responder
	pid := participate(t, w, conv, nil, nil)
	if _, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "what failed?"); err != nil {
		t.Fatal(err)
	}
	var id string
	eventually(t, "the request waiting at bob", func() bool {
		w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND body = ? AND state = ?`, conv, "what failed?", stateAgentWaiting).Scan(&id)
		return id != ""
	})
	inv, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "another look")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the invitation at bob", func() bool { _, ok := needsYou(t, w.bob, inv.PID, ""); return ok })
	pub, _ := json.Marshal(w.alice.Self())
	offer := protocol.NewID()
	if _, err := w.bob.store.db.Exec(`INSERT INTO device_links(offer, address, public, join_sig, requested_at, expires, state, updated_at) VALUES(?, ?, ?, x'00', ?, ?, ?, ?)`,
		offer, "bob/tablet", string(pub), time.Now().Unix(), time.Now().Add(10*time.Minute).Unix(), LinkPending, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	keyChanged, proof := protocol.NewID(), protocol.NewID()
	if err := w.bob.store.quarantine(keyChanged, w.alice.Address, reasonKeyChanged, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.quarantine(proof, w.alice.Address, reasonProof, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	wt, err := w.bob.Waiting()
	if err != nil {
		t.Fatal(err)
	}
	if len(wt.AgentRequests) != 1 || wt.AgentRequests[0].ID != id || !wt.AgentRequests[0].Stuck || !strings.Contains(wt.AgentRequests[0].Why, "no responder is chosen here") {
		t.Fatalf("agent requests: %+v", wt.AgentRequests)
	}
	if !slices.ContainsFunc(wt.AgentInvites, func(r ConvReview) bool { return r.PID == inv.PID }) {
		t.Fatalf("agent invitations: %+v", wt.AgentInvites)
	}
	if len(wt.Links) != 1 || wt.Links[0].ID != offer {
		t.Fatalf("device links: %+v", wt.Links)
	}
	if len(wt.Held) != 1 || wt.Held[0].ID != keyChanged {
		t.Fatalf("held messages: %+v", wt.Held)
	}
	checks := map[string]Check{}
	for _, c := range w.bob.Doctor(tctx(t)) {
		checks[c.Name] = c
	}
	for name, want := range map[string]string{
		"agent requests": "1 request(s) to your agent have not run: no responder is chosen here",
		"invitations":    "1 invitation(s) wait for your decision",
		"device links":   "1 device(s) ask to be linked to your person",
		"held messages":  "1 message(s) held here",
	} {
		if c, ok := checks[name]; !ok || !strings.HasPrefix(c.Result, want) {
			t.Fatalf("doctor %s: %+v (want %q)", name, c, want)
		}
	}
	if checks["agent requests"].OK {
		t.Fatal("doctor calls requests nothing would run ok")
	}
}

// BUG-24: a group invitation for this person waits in what doctor and
// inbox --review list, until decided.
func TestWaitingListsGroupInvitation(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	inv := groupLifecycleInvite(t, w, p, nil)
	awaitGroupInvitation(t, w.bob, inv.ID, "pending")
	wt, err := w.bob.Waiting()
	if err != nil {
		t.Fatal(err)
	}
	if len(wt.GroupInvites) != 1 || wt.GroupInvites[0].ID != inv.ID {
		t.Fatalf("group invitations: %+v", wt.GroupInvites)
	}
	if wt, _ := w.alice.Waiting(); len(wt.GroupInvites) != 0 {
		t.Fatalf("the inviter's own invitation waits for the inviter: %+v", wt.GroupInvites)
	}
}
