package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// persons creates a person on each agent (published through the Hub).
func persons(t *testing.T, agents ...*Agent) {
	t.Helper()
	for _, a := range agents {
		if _, err := a.CreatePerson(tctx(t), "Person of "+a.Address); err != nil {
			t.Fatalf("person for %s: %v", a.Address, err)
		}
	}
}

// newDM waits until the peer's daemon has published its capabilities, then
// starts a DM from a with the person on peer.
func newDM(t *testing.T, a, peer *Agent) string {
	t.Helper()
	var conv string
	eventually(t, "a DM with "+peer.Address, func() bool {
		var err error
		conv, err = a.CreateDM(tctx(t), peer.Address)
		return err == nil
	})
	return conv
}

func convBodies(t *testing.T, a *Agent, conv string) []string {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		out = append(out, m.Dir+":"+m.Body)
	}
	return out
}

func heldReason(t *testing.T, a *Agent, id string) string {
	t.Helper()
	var reason string
	a.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id = ?`, id).Scan(&reason)
	return reason
}

func inboxCount(t *testing.T, a *Agent, where string, args ...any) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// craft seals in from one agent to another, as that sender's device would
// (or a faulty one could), without going through SendConv.
func craft(t *testing.T, from, to *Agent, in envelope.Inner) envelope.Envelope {
	t.Helper()
	r, err := to.id.Public(to.Address).Recipient()
	if err != nil {
		t.Fatal(err)
	}
	if in.ID == "" {
		in.ID = protocol.NewID()
	}
	if in.TS == 0 {
		in.TS = time.Now().Unix()
	}
	in.V, in.From, in.To = envelope.Version2, from.Address, to.Address
	env, err := envelope.Seal(in, from.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func rootOf(t *testing.T, a *Agent, conv string) (protocol.ConvRoot, json.RawMessage) {
	t.Helper()
	root, raw, found, err := a.store.conversation(conv)
	if err != nil || !found {
		t.Fatalf("conversation %s: %v %v", conv, found, err)
	}
	return root, raw
}

// DM1: a person exists only once created, explicitly; there is one per
// installation; others pin it verified against its device's key, and a
// different record for it later is a frozen conflict, never a replacement.
func TestPersonIsExplicit(t *testing.T) {
	w := newWorld(t, "")
	if _, ok, _ := w.alice.Person(); ok {
		t.Fatal("a person exists that nobody created")
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM persons`).Scan(&n)
	if n != 0 {
		t.Fatal("joining created a person")
	}
	p, err := w.alice.CreatePerson(tctx(t), "Alice")
	if err != nil || p.State != personSelf || p.Label != "Alice" || p.Address != w.alice.Address {
		t.Fatalf("create: %+v %v", p, err)
	}
	if _, err := w.alice.CreatePerson(tctx(t), "Alice again"); err == nil {
		t.Fatal("a second person was created on one installation")
	}
	alicePub := w.alice.id.Public(w.alice.Address)
	got, err := w.bob.personOfKey(tctx(t), w.alice.Address, alicePub)
	if err != nil || got.info.Person != p.Person || got.info.State != personPinned || got.info.Roster != p.Roster {
		t.Fatalf("pinned: %+v %v", got.info, err)
	}
	// Bob has no person of his own: nothing was made for him.
	if _, ok, _ := w.bob.Person(); ok {
		t.Fatal("pinning someone else's person created one here")
	}
	other := protocol.PersonRoster{Person: protocol.NewID(), Label: "Alice",
		Devices: []protocol.RosterDevice{{Address: w.alice.Address, Fingerprint: alicePub.Fingerprint()}}}
	other.Sign(w.alice.id.Sign)
	raw, _ := json.Marshal(other)
	if err := w.bob.store.pinPerson(other, raw); !errors.Is(err, errPersonConflict) {
		t.Fatalf("another person for a pinned address: %v", err)
	}
	if _, err := w.bob.personOfKey(tctx(t), w.alice.Address, alicePub); !errors.Is(err, errPersonConflict) {
		t.Fatalf("after the conflict: %v", err)
	}
	row, _, _ := w.bob.store.personByAddress(w.alice.Address)
	if row.info.Person != p.Person || row.info.State != personConflict {
		t.Fatalf("the pinned person was replaced: %+v", row.info)
	}
}

// DM2: two DMs with the same person stay separate, both ways, and survive
// a restart.
func TestDMsStaySeparate(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	stopBob := runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	c1, c2 := newDM(t, w.alice, w.bob), newDM(t, w.alice, w.bob)
	if c1 == c2 {
		t.Fatal("two DMs share an id")
	}
	for conv, body := range map[string]string{c1: "deploy topic", c2: "budget topic"} {
		if s, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil || s.State == stateConvWaiting {
			t.Fatalf("send: %+v %v", s, err)
		}
	}
	eventually(t, "bob to hold both", func() bool {
		convs, _ := w.bob.Conversations()
		return len(convs) == 2 && len(convBodies(t, w.bob, c1)) == 1 && len(convBodies(t, w.bob, c2)) == 1
	})
	if got := convBodies(t, w.bob, c1); got[0] != "in:deploy topic" {
		t.Fatalf("first DM at bob: %v", got)
	}
	if _, err := w.bob.SendConv(tctx(t), c1, ConvOutgoing{Body: "on deploy"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to get the reply in the first DM only", func() bool {
		return strings.Join(convBodies(t, w.alice, c1), "|") == "out:deploy topic|in:on deploy" &&
			strings.Join(convBodies(t, w.alice, c2), "|") == "out:budget topic"
	})

	stopBob()
	w.bob.Close()
	again, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	convs, _ := again.Conversations()
	if len(convs) != 2 || strings.Join(convBodies(t, again, c1), "|") != "in:deploy topic|out:on deploy" {
		t.Fatalf("after a restart: %d conversations, %v", len(convs), convBodies(t, again, c1))
	}
}

// DM2: a root is pinned only from its creator's own device, with its
// signature; a member must be a pinned person of the root. What cannot be
// proven yet is held, never admitted, and looked at again later.
func TestConversationProof(t *testing.T) {
	w := newWorld(t, "")
	carol := mustJoin(t, t.TempDir(), w.aliceInvites("carol"), "desk")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob, carol)
	conv := newDM(t, w.alice, w.bob)
	root, raw := rootOf(t, w.alice, conv)

	// Carol has a copy of the root and delivers it first: not its creator's
	// device, so it is held, and nothing is admitted.
	fromCarol := craft(t, carol, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: "let me in", Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI})
	if err := w.bob.verifyAndStore(tctx(t), fromCarol); err != nil {
		t.Fatal(err)
	}
	if r := heldReason(t, w.bob, fromCarol.ID); r != reasonProof {
		t.Fatalf("root from a non-creator: held %q", r)
	}
	if _, _, found, _ := w.bob.store.conversation(conv); found {
		t.Fatal("the root was pinned from someone other than its creator")
	}

	// The creator's own message pins it; looking again, carol is no member.
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to admit the creator's message", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	w.bob.retryProof(tctx(t))
	if r := heldReason(t, w.bob, fromCarol.ID); r != reasonInvalid {
		t.Fatalf("a non-member's message after the root is known: %q", r)
	}
	if n := inboxCount(t, w.bob, `sender = ?`, carol.Address); n != 0 {
		t.Fatal("a non-member's message was admitted")
	}

	// A root that is not the creator's signature, and one naming bob's
	// person with another roster, are refused; neither is pinned.
	forged := root
	forged.Nonce = protocol.NewID()
	forgedRaw, _ := json.Marshal(forged)
	env := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: "x", Conv: forged.ID(), LID: protocol.NewID(), Root: forgedRaw})
	w.bob.verifyAndStore(tctx(t), env)
	if r := heldReason(t, w.bob, env.ID); r != reasonInvalid {
		t.Fatalf("forged root: %q", r)
	}
	wrong := root
	wrong.Members = append([]protocol.ConvMember(nil), root.Members...)
	for i := range wrong.Members {
		if wrong.Members[i].Person != root.Creator.Person {
			wrong.Members[i].Roster = strings.Repeat("0", 64)
		}
	}
	wrong.Nonce = protocol.NewID()
	wrong.Sign(w.alice.id.Sign)
	wrongRaw, _ := json.Marshal(wrong)
	env = craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: "x", Conv: wrong.ID(), LID: protocol.NewID(), Root: wrongRaw})
	w.bob.verifyAndStore(tctx(t), env)
	if r := heldReason(t, w.bob, env.ID); r != reasonInvalid {
		t.Fatalf("root with another roster for bob's person: %q", r)
	}
	for _, id := range []string{forged.ID(), wrong.ID()} {
		if _, _, found, _ := w.bob.store.conversation(id); found {
			t.Fatal("a refused root was pinned")
		}
	}
}

// DM2: a message whose sender's person is not published yet is held, and
// the Hub's members push after it is published admits it: nothing polls.
func TestProofArrivesLater(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.bob)
	// Alice's daemon publishes what it has when it connects; wait for that
	// (its capabilities), so the person made below stays unpublished.
	label, name, _ := protocol.SplitAddress(w.alice.Address)
	pub := w.alice.id.Public(w.alice.Address)
	eventually(t, "alice's daemon to publish on connecting", func() bool {
		var prof protocol.Profile
		return w.bob.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof) == nil &&
			prof.Supports(w.alice.Address, pub.SignKey, protocol.CapEnv2)
	})
	// Alice's person exists here but is not published yet.
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Alice", Devices: []protocol.RosterDevice{{Address: w.alice.Address, Fingerprint: pub.Fingerprint()}}}
	r.Sign(w.alice.id.Sign)
	raw, _ := json.Marshal(r)
	if err := w.alice.store.setSelfPerson(r, raw); err != nil {
		t.Fatal(err)
	}
	conv := newDM(t, w.alice, w.bob)
	s, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "before you know me"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it for proof", func() bool { return heldReason(t, w.bob, s.ID) == reasonProof })
	if err := w.alice.publishPerson(tctx(t), true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to admit it once alice's person is published", func() bool {
		return len(convBodies(t, w.bob, conv)) == 1 && heldReason(t, w.bob, s.ID) == ""
	})
}

// DM3: a logical message is admitted once per sender key; the same id with
// other content is refused; conversation questions and tasks, replicas
// included, are held for the person and never enter the legacy worker, even
// from an approved sender, and legacy actions refuse them.
func TestConversationAdmission(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	if err := w.bob.Approve(w.alice.Address); err != nil { // approved for legacy automatic answers
		t.Fatal(err)
	}
	conv := newDM(t, w.alice, w.bob)
	_, raw := rootOf(t, w.alice, conv)
	lid := protocol.NewID()
	question := envelope.Inner{Kind: envelope.KindQuestion, Body: "can you check the deploy?", Conv: conv, LID: lid, Root: raw, Origin: envelope.OriginUI}
	first := craft(t, w.alice, w.bob, question)
	if err := w.bob.verifyAndStore(tctx(t), first); err != nil {
		t.Fatal(err)
	}
	copyEnv := craft(t, w.alice, w.bob, question) // another copy: new envelope id, same logical id and content
	if err := w.bob.verifyAndStore(tctx(t), copyEnv); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, w.bob, `lid = ?`, lid); n != 1 || heldReason(t, w.bob, copyEnv.ID) != "" {
		t.Fatalf("admitted %d times (copy held %q)", n, heldReason(t, w.bob, copyEnv.ID))
	}
	changed := question
	changed.Body = "can you delete production?"
	conflicting := craft(t, w.alice, w.bob, changed)
	w.bob.verifyAndStore(tctx(t), conflicting)
	if r := heldReason(t, w.bob, conflicting.ID); r != reasonDuplicate {
		t.Fatalf("same logical id, other content: %q", r)
	}

	bobPub := w.bob.id.Public(w.bob.Address)
	target := &envelope.Target{Address: w.bob.Address, Fingerprint: bobPub.Fingerprint()}
	task := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: "run the migration", Conv: conv, LID: protocol.NewID(), Root: raw, Target: target})
	replica := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindQuestion, Body: "a copy", Conv: conv, LID: protocol.NewID(), Root: raw, Replica: true, Target: target})
	for _, env := range []envelope.Envelope{task, replica} {
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{first.ID, task.ID, replica.ID} {
		if n := inboxCount(t, w.bob, `id = ? AND state = ?`, id, stateConvHeld); n != 1 {
			t.Errorf("%s not held for the person", id)
		}
	}
	time.Sleep(300 * time.Millisecond) // bob's worker is running and was woken
	if n := inboxCount(t, w.bob, `conv IS NOT NULL AND state != ?`, stateConvHeld); n != 0 {
		t.Fatalf("%d conversation requests left the held state", n)
	}
	if _, ok, err := w.bob.store.claimJob("test"); err != nil || ok {
		t.Fatalf("the legacy worker could claim a conversation request (%v)", err)
	}
	if err := w.bob.Accept(first.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("accept: %v", err)
	}
	if _, _, err := w.bob.AcceptAlways(task.ID); !errors.Is(err, ErrConversationItem) {
		t.Fatalf("accept --always: %v", err)
	}
	if _, err := w.bob.Reply(tctx(t), first.ID, "done"); !errors.Is(err, ErrConversationItem) {
		t.Fatalf("reply: %v", err)
	}
	if _, err := w.bob.Decline(tctx(t), task.ID, "no"); !errors.Is(err, ErrConversationItem) {
		t.Fatalf("decline: %v", err)
	}
}

// DM3: when the other device cannot read conversations now (an older
// program's record is its latest), a message in an existing conversation is
// kept as waiting, never sent in the older format, and goes out when the
// device publishes support again.
func TestConversationWaitsForSupport(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	label, name, _ := protocol.SplitAddress(w.bob.Address)
	var prof protocol.Profile
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil || len(prof.Sessions) != 1 {
		t.Fatalf("bob's profile: %+v %v", prof, err)
	}
	publish := func(ts int64, caps ...string) {
		rec := protocol.CapsRecord{Address: w.bob.Address, Session: prof.Sessions[0], Caps: caps, TS: ts}
		rec.Sign(w.bob.id.Sign)
		if err := w.bob.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish(time.Now().Unix() + 100) // no conversations
	s, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "kept for later"})
	if err != nil || s.State != stateConvWaiting || !strings.Contains(s.Detail, "cannot read conversations") {
		t.Fatalf("send while unsupported: %+v %v", s, err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := inboxCount(t, w.bob, `body = ?`, "kept for later"); n != 0 {
		t.Fatal("sent while the recipient could not read it")
	}
	publish(time.Now().Unix()+200, protocol.CapEnv2)
	eventually(t, "the kept message to go out once supported", func() bool {
		return strings.Join(convBodies(t, w.bob, conv), "|") == "in:kept for later"
	})
	var state string
	w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id = ?`, s.ID).Scan(&state)
	if state == stateConvWaiting || state == stateQueued {
		t.Fatalf("outbox state %q after release", state)
	}
}

var _ = context.Background
