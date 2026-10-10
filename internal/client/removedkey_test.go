package client

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ownPhoneAddedRemoved signs two steps of owner's person, a phone added
// (human key) and then removed, and pins them at each of at; it returns
// the step that still listed the phone, the step after its removal and the
// phone's key. Nothing is published: the steps are the evidence the
// receivers hold.
func ownPhoneAddedRemoved(t *testing.T, owner *Agent, at ...*Agent) (with, without protocol.PersonRoster, phone identity.Public) {
	t.Helper()
	me, _, err := owner.store.selfPerson(owner.Address)
	if err != nil {
		t.Fatal(err)
	}
	prev := me.roster
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	phone = id.Public(strings.Split(owner.Address, "/")[0] + "/old-phone")
	with = protocol.PersonRoster{Person: prev.Person, Label: prev.Label, Seq: prev.Seq + 1, Prev: prev.Hash(), Devices: append(slices.Clone(prev.Devices), phone),
		HumanKeys: append(prev.Humans(), phone.Fingerprint()), By: owner.Self().Fingerprint()}
	with.Join = ed25519.Sign(id.Sign, protocol.JoinBytes(with.Person, with.Seq, with.Prev, phone))
	with.Sign(owner.id.Sign)
	without = protocol.PersonRoster{Person: prev.Person, Label: prev.Label, Seq: with.Seq + 1, Prev: with.Hash(), Devices: slices.Clone(prev.Devices), HumanKeys: prev.Humans(), By: owner.Self().Fingerprint()}
	without.Sign(owner.id.Sign)
	if _, err := without.VerifyNext(with); err != nil {
		t.Fatal(err)
	}
	for _, a := range at {
		for _, r := range []protocol.PersonRoster{with, without} {
			raw, _ := json.Marshal(r)
			if _, err := a.store.pinChain(r.Person, [][]byte{raw}, a.Self(), false); err != nil {
				t.Fatal(err)
			}
		}
	}
	return with, without, phone
}

// signedEvent stores at each of at an event of conv signed by by's device
// under roster step roster; edit fills the rest before signing.
func signedEvent(t *testing.T, by *Agent, roster protocol.PersonRoster, conv, pid, typ, prev string, edit func(*protocol.ParticipationEvent), at ...*Agent) protocol.ParticipationEvent {
	t.Helper()
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: pid, Type: typ, Prev: prev, TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: roster.Person, Roster: roster.Hash(), Address: by.Address, Fingerprint: by.Self().Fingerprint()}}
	if edit != nil {
		edit(&ev)
	}
	ev.Sign(by.id.Sign)
	if err := ev.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ev)
	for _, a := range at {
		if err := a.store.addParticipationEvent(ev, raw); err != nil {
			t.Fatal(err)
		}
	}
	return ev
}

// A DM invitation named an own device of its inviter as a task key; that
// device was removed from the inviter's roster afterwards (t5-held: an old
// phone at roster step 6). The signed invitation still counts, so its
// participation's history is admitted, but the removed key has no task
// authority. A key the inviter's signed roster step did not list (never
// its device, or a device added only later) still holds the invitation,
// as does an invitation the removed device signed itself.
func TestDMInviteNamingRemovedOwnTaskKey(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	alice, bob := w.alice.Self(), w.bob.Self()
	me, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	before := me.roster
	with, without, phone := ownPhoneAddedRemoved(t, w.alice, w.bob)
	host := &protocol.ParticipationHost{Person: before.Person, Address: alice.Address, Fingerprint: alice.Fingerprint()}
	invite := func(roster protocol.PersonRoster, keys ...string) string {
		pid := protocol.NewID()
		inv := signedEvent(t, w.alice, roster, conv, pid, protocol.EventInvite, "", func(ev *protocol.ParticipationEvent) {
			ev.Host, ev.Audience, ev.TaskKeys = host, protocol.AudienceConversation, keys
		}, w.bob)
		signedEvent(t, w.alice, without, conv, pid, protocol.EventAccept, inv.Hash(), nil, w.bob)
		return pid
	}

	pid := invite(with, bob.Fingerprint(), phone.Fingerprint())
	p := stateAt(t, w.bob, pid)
	if p.State != PartActive || p.Held != 0 || !p.Claimable() {
		t.Fatalf("an invitation naming a since-removed own device does not count: %+v", p)
	}
	if !slices.Equal(p.TaskKeys, []string{bob.Fingerprint()}) {
		t.Fatalf("task authority %v: the removed key must have none, the current member key keeps it", p.TaskKeys)
	}
	// The participation's history: the host's answer is admitted, as a
	// copy and live, instead of waiting for an invitation forever.
	answer := envelope.Inner{Kind: envelope.KindAnswer, Body: "the answer", Conv: conv, LID: protocol.NewID(), PID: pid, ReplyTo: protocol.NewID(), Origin: "agent:stub"}
	for _, historical := range []bool{true, false} {
		if reason, err := w.bob.checkConversationAgent(answer, alice, historical); reason != "" || err != nil {
			t.Fatalf("the host's answer (history %v) held: %s %v", historical, reason, err)
		}
	}

	for name, pid := range map[string]string{
		"a key in no roster":                      invite(with, bob.Fingerprint(), strings.Repeat("0", 8)+"-"+strings.Repeat("1", 8)+"-"+strings.Repeat("2", 8)+"-"+strings.Repeat("3", 8)),
		"a device added after the signed step":    invite(before, phone.Fingerprint()),
		"a removed device of another step listed": invite(without, phone.Fingerprint()),
	} {
		if p := stateAt(t, w.bob, pid); p.State != PartPending || p.Invite != "" || p.Held == 0 {
			t.Fatalf("%s: the invitation counted: %+v", name, p)
		}
		answer.PID = pid
		if reason, _ := w.bob.checkConversationAgent(answer, alice, true); reason != reasonProof {
			t.Fatalf("%s: its history is not held: %q", name, reason)
		}
	}
	// The removed device itself is no author any more.
	byPhone := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: with.Person, Roster: with.Hash(), Address: phone.Address, Fingerprint: phone.Fingerprint()},
		Host:   host, Audience: protocol.AudienceConversation, TaskKeys: []string{bob.Fingerprint()}}
	m, err := w.bob.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.loadHosts(w.bob.store.db, []protocol.ParticipationEvent{byPhone}); err != nil {
		t.Fatal(err)
	}
	if p := resolve(conv, byPhone.PID, []protocol.ParticipationEvent{byPhone}, m); p.State != PartPending || p.Held != 1 {
		t.Fatalf("an invitation by the removed device counted: %+v", p)
	}
}

// An edit or deletion of a message an own device signed before it was
// removed (t5-held: "the target's sender key is no member's (yet)",
// rechecked forever) is that person's: admitted and shown. The removed
// device of another person is a final, specific refusal; a key in no
// member's chain still waits for evidence.
func TestControlOfRemovedDeviceMessage(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	_, _, alicePhone := ownPhoneAddedRemoved(t, w.alice, w.bob)
	_, _, bobPhone := ownPhoneAddedRemoved(t, w.bob, w.bob)
	rcpt, err := w.bob.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	send := func(fp, sub, body string) envelope.Inner {
		t.Helper()
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(),
			Kind: envelope.KindMessage, Sub: sub, Body: body, Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: protocol.NewID(), Fingerprint: fp}}
		env, err := envelope.Seal(in, w.alice.id.Sign, rcpt)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return in
	}
	held := func(id string) (reason, code string) {
		w.bob.store.db.QueryRow(`SELECT reason, detail_code FROM quarantine WHERE id = ?`, id).Scan(&reason, &code)
		return reason, code
	}
	for _, c := range []struct{ sub, body string }{{envelope.SubRevision, `{"rev":1,"text":"fixed on the desk"}`}, {envelope.SubRetraction, `{}`}} {
		in := send(alicePhone.Fingerprint(), c.sub, c.body)
		if reason, code := held(in.ID); reason != "" || controlRows(t, w.bob, in.LID) != 1 {
			t.Fatalf("%s of the removed own device's message: held %q %q", c.sub, reason, code)
		}
		got, err := w.bob.controlsOf(ControlRef{Conv: conv, ID: in.Ref.ID, Fingerprint: in.Ref.Fingerprint})
		if err != nil || c.sub == envelope.SubRevision && (!got.Edited || got.Text != "fixed on the desk") || c.sub == envelope.SubRetraction && !got.Deleted {
			t.Fatalf("%s not shown on the removed device's message: %+v %v", c.sub, got, err)
		}
	}
	other := send(bobPhone.Fingerprint(), envelope.SubRevision, `{"rev":1,"text":"hijack"}`)
	if reason, code := held(other.ID); reason != reasonInvalid || code != "control_target_person_mismatch" || controlRows(t, w.bob, other.LID) != 0 {
		t.Fatalf("an edit of another person's removed device's message: %q %q", reason, code)
	}
	unknown := send("00000000-11111111-22222222-33333333", envelope.SubRetraction, `{}`)
	if reason, _ := held(unknown.ID); reason != reasonProof {
		t.Fatalf("a key in no member's chain: %q", reason)
	}
}

// Self-invite consent (D3) needs task keys only of trusted own devices: an
// invitation naming an own device removed since waits for the person's
// click even when that device's key is still in the local trust set, and
// its participation then gives the removed key no task authority.
func TestSelfConsentRefusesRemovedTaskKey(t *testing.T) {
	w, conv, _ := selfConsentWorld(t)
	alice := w.alice.Self()
	with, _, phone := ownPhoneAddedRemoved(t, w.alice, w.alice)
	if err := w.alice.setSelfConsentTrust(func(tx *sql.Tx) error { return addSelfConsentTrustIn(tx, phone.Address, phone.Fingerprint()) }); err != nil {
		t.Fatal(err)
	}
	pid := storedInvite(t, w.alice, w.alice, conv, func(ev *protocol.ParticipationEvent) {
		ev.Author.Roster, ev.TaskKeys = with.Hash(), []string{alice.Fingerprint(), phone.Fingerprint()}
	})
	if p := stateAt(t, w.alice, pid); p.State != PartInvited || !p.HostHere || !slices.Equal(p.TaskKeys, []string{alice.Fingerprint()}) {
		t.Fatalf("not an invitation waiting here without the removed key: %+v", p)
	}
	if ok, err := w.alice.selfConsent(tctx(t), pid); err != nil || ok || decisionsBy(t, w.alice, conv, pid, alice.Fingerprint()) != 0 {
		t.Fatalf("accepted an invitation naming a removed device without a click: %v %v", ok, err)
	}
	// The same invitation without the removed key is the person's own.
	own := storedInvite(t, w.alice, w.alice, conv, func(ev *protocol.ParticipationEvent) {
		ev.Author.Roster, ev.TaskKeys = with.Hash(), []string{alice.Fingerprint()}
	})
	if ok, err := w.alice.selfConsent(tctx(t), own); err != nil || !ok {
		t.Fatalf("the own invitation without the removed key: %v %v", ok, err)
	}
}

// within fails the test unless f returns in time: a read of the store
// made inside a transaction that holds its one connection never returns.
func within(t *testing.T, what string, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never returned: a store read waits on its own transaction", what)
		return nil
	}
}

// A group edit or deletion is decided again inside the transaction that
// stores or queues it: checkGroup live, and groupControlHistoryCheck for a
// new device's catch-up and its own-replica history (forwarded copies use
// the same guard). For a message an own device signed before its removal,
// the target key's person comes from the removed step of the chain, read
// in that transaction: the store's one connection is the transaction's,
// so a read of the store there never returns. The same person's controls
// are admitted live and reach a newly linked own device in its catch-up;
// another member's edit is a final, specific refusal.
func TestGroupControlOfRemovedDeviceMessage(t *testing.T) {
	w, carol, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	with, _, phone := ownPhoneAddedRemoved(t, w.alice, w.alice, w.bob, carol)
	edited, deleted := protocol.NewID(), protocol.NewID()
	for _, a := range []*Agent{w.alice, w.bob, carol} { // as each received the phone's turns
		admission, err := groupMemberAdmission(a.store.db, packet, a.Address, a.Self().Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		for _, lid := range []string{edited, deleted} {
			if _, err := a.store.db.Exec(`INSERT INTO inbox(id,lid,sender,ts,kind,body,received_at,received_ms,verified_by,conv,group_admission) VALUES(?,?,?,1,'message','typed on the old phone',1,1000,?,?,?)`, lid, lid, phone.Address, phone.Fingerprint(), conv, admission.Hash()); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Alice edits and deletes them from her desk. Each control reaches bob
	// only when the test hands it to him, and his new phone (linked after
	// they were sent) only from him.
	stops[w.bob]()
	stops[carol]()
	editRef := ControlRef{Conv: conv, ID: edited, Fingerprint: phone.Fingerprint()}
	deleteRef := ControlRef{Conv: conv, ID: deleted, Fingerprint: phone.Fingerprint()}
	if _, err := w.alice.Revise(tctx(t), editRef, "fixed on the desk"); err != nil {
		t.Fatalf("alice may not edit her removed phone's message: %v", err)
	}
	if _, err := w.alice.Retract(tctx(t), deleteRef, ""); err != nil {
		t.Fatalf("alice may not delete her removed phone's message: %v", err)
	}
	// What a control's sender sealed for bob, taken as bob's daemon would.
	take := func(from *Agent, sub string) envelope.Envelope {
		t.Helper()
		var raw []byte
		if err := from.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub=? ORDER BY rowid DESC LIMIT 1`, w.bob.Address, sub).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env envelope.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if err := within(t, "bob's admission of a "+sub, func() error { return w.bob.accept(tctx(t), env) }); err != nil {
			t.Fatal(err)
		}
		return env
	}
	held := func(id string) (reason, code string) {
		w.bob.store.db.QueryRow(`SELECT reason, detail_code FROM quarantine WHERE id = ?`, id).Scan(&reason, &code)
		return reason, code
	}
	for _, c := range []struct {
		sub string
		ref ControlRef
	}{{envelope.SubRevision, editRef}, {envelope.SubRetraction, deleteRef}} {
		env := take(w.alice, c.sub)
		got, err := w.bob.controlsOf(c.ref)
		if reason, code := held(env.ID); reason != "" || err != nil || c.sub == envelope.SubRevision && got.Text != "fixed on the desk" || c.sub == envelope.SubRetraction && !got.Deleted {
			t.Fatalf("%s of the removed own device's message: held %q %q, shown %+v %v", c.sub, reason, code, got, err)
		}
	}
	// Another member's edit of it: the key's person is known and is not
	// carol's, so it is refused for good instead of waiting for proof.
	if _, err := carol.sendControl(tctx(t), editRef, envelope.SubRevision, `{"rev":9,"text":"hijack"}`); err != nil {
		t.Fatal(err)
	}
	forged := take(carol, envelope.SubRevision)
	if reason, code := held(forged.ID); reason != reasonInvalid || code != "control_target_person_mismatch" {
		t.Fatalf("another member's edit of the removed device's message: %q %q", reason, code)
	}
	if got, _ := w.bob.controlsOf(editRef); got.Text != "fixed on the desk" {
		t.Fatalf("the refused edit is shown: %+v", got)
	}

	// Bob links a new phone (his daemon takes the request).
	stop := runAgent(t, w.bob)
	newPhone, await, _ := linkPhone(t, w.bob, "new-phone")
	request := pendingLink(t, w.bob)
	stop()
	if err := w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(func() {
		logReplicationFailure(t, w.bob, "group source")
		logReplicationFailure(t, newPhone, "group phone")
	})

	// The new phone's catch-up: bob checks the stored edit again inside the
	// transaction that queues it (checkHistoryCopies).
	if err := within(t, "bob's catch-up page", func() error { _, e := w.bob.historyCatchupPage(tctx(t), newPhone.Self()); return e }); err != nil {
		t.Fatalf("catch-up holding the removed device's edited message: %v", err)
	}
	// The phone holds alice's chain as bob does, the removed step included,
	// and stores what bob sent as own-replica history, checking each edit
	// inside that transaction too.
	var chain [][]byte
	rows, err := w.bob.store.db.Query(`SELECT record FROM person_chain WHERE person=? ORDER BY seq`, with.Person)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		chain = append(chain, []byte(raw))
	}
	rows.Close()
	if _, err = newPhone.store.pinChain(with.Person, chain, newPhone.Self(), false); err != nil {
		t.Fatal(err)
	}
	runAgent(t, newPhone)
	publishGroupFixtureCaps(t, newPhone, true)
	// The intercepted controls and transactional catch-up above were driven
	// with Bob stopped. Resume his real daemon now so newly advertised phone
	// capabilities release the waiting signed proof/context prerequisites.
	runAgent(t, w.bob)
	if err = w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new phone shows alice's edit of her removed phone's message", func() bool {
		var replica int
		got, err := newPhone.controlsOf(editRef)
		return err == nil && got.Text == "fixed on the desk" &&
			newPhone.store.db.QueryRow(`SELECT replica FROM inbox WHERE ref_id=? AND sub=?`, edited, envelope.SubRevision).Scan(&replica) == nil && replica == 1
	})
}
