package client

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func groupLifecycleFixture(t *testing.T, online bool) (*world, GroupContext) {
	t.Helper()
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	for _, a := range []*Agent{w.alice, w.bob} {
		var n int
		if err := a.store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='group_invitations'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatal("group lifecycle store migration not installed")
		}
	}
	if online {
		for _, a := range []*Agent{w.alice, w.bob} {
			runAgent(t, a)
			publishGroupFixtureCaps(t, a, true)
		}
	}
	p, err := w.alice.CreateGroup(tctx(t), "Conscious group")
	if err != nil {
		t.Fatal(err)
	}
	return w, p
}

func groupLifecycleInvite(t *testing.T, w *world, p GroupContext, refs []protocol.GroupHistoryRef) GroupInvitationInfo {
	t.Helper()
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := w.alice.InviteGroup(tctx(t), p.Root.ID(), bob.roster.Person, refs)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func awaitGroupInvitation(t *testing.T, a *Agent, id, state string) groupInvitationRow {
	t.Helper()
	var r groupInvitationRow
	eventually(t, "exact group invitation "+state, func() bool {
		var err error
		r, err = groupInvitationIn(a.store.db, id, "in")
		return err == nil && r.State == state
	})
	return r
}

func assertNoGroupJoin(t *testing.T, a *Agent, conv string) {
	t.Helper()
	if _, err := a.GroupContext(conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("preaccept/declined context installed: %v", err)
	}
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM conversations WHERE id=?`, conv).Scan(&n); err != nil || n != 0 {
		t.Fatalf("premature conversation %d %v", n, err)
	}
	if err := a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND (sub IS NULL OR kind IN ('question','task'))`, conv).Scan(&n); err != nil || n != 0 {
		t.Fatalf("premature turn or work %d %v", n, err)
	}
}

func TestGroupLifecycleCreateInviteAcceptOrdinaryTurns(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	if len(p.State.Members) != 1 || len(p.State.Admins()) != 1 || p.State.Seq != 0 {
		t.Fatal("create did not start alone")
	}
	inv := groupLifecycleInvite(t, w, p, nil)
	awaitGroupInvitation(t, w.bob, inv.ID, "pending")
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	if _, err := w.bob.SendConv(tctx(t), p.Root.ID(), ConvOutgoing{Body: "not joined"}); err == nil {
		t.Fatal("pending invitation sent ordinary turn")
	}
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "accepted consent automatically publishes membership", func() bool {
		r, e := groupInvitationIn(w.alice.store.db, inv.ID, "out")
		return e == nil && r.State == "published"
	})
	eventually(t, "new member gets only current membership", func() bool {
		got, e := w.bob.GroupContext(p.Root.ID())
		return e == nil && got.State.Seq == 1 && len(got.State.Members) == 2
	})
	for _, a := range []*Agent{w.alice, w.bob} {
		body := "ordinary from " + a.Address
		if _, err := a.SendConv(tctx(t), p.Root.ID(), ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
		other := w.bob
		if a == w.bob {
			other = w.alice
		}
		eventually(t, "ordinary encrypted turn", func() bool { return slices.Contains(convBodies(t, other, p.Root.ID()), "in:"+body) })
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=?`, p.Root.ID()).Scan(&n)
	if n != 2 {
		t.Fatalf("duplicate consent publication: %d", n)
	}
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubGroupConsent).Scan(&n)
	if n != 1 {
		t.Fatalf("local repeated accept enqueued %d consents", n)
	}
}

func TestGroupLifecycleDeclineAndExactHistoryBinding(t *testing.T) {
	w, p := groupOrdinaryPublication(t)
	// Invite a third person to actual visible content, rather than fabricated refs.
	w.bob = proofReader(t, w, "history-decline")
	for _, a := range []*Agent{w.alice, w.bob} {
		runAgent(t, a)
		publishGroupFixtureCaps(t, a, true)
	}
	if _, err := w.alice.SendConv(tctx(t), p.Root.ID(), ConvOutgoing{Body: "first exact selection"}); err != nil {
		t.Fatal(err)
	}
	refs, err := w.alice.SelectGroupHistory(tctx(t), p.Root.ID(), GroupHistorySelection{Last: 1})
	if err != nil || len(refs) != 1 {
		t.Fatalf("visible selection %v %v", refs, err)
	}
	inv := groupLifecycleInvite(t, w, p, refs)
	r := awaitGroupInvitation(t, w.bob, inv.ID, "pending")
	if !slices.Equal(r.Proposal.History, refs) || r.Proposal.ID() != inv.ID {
		t.Fatal("selected refs changed")
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "admin receives explicit decline", func() bool {
		row, e := groupInvitationIn(w.alice.store.db, inv.ID, "out")
		return e == nil && row.State == "declined"
	})
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err == nil {
		t.Fatal("declined proposal silently accepted")
	}
	// A separately consented nonempty selection is signed exactly; its bodies
	// and files remain absent before explicit membership consent.
	if _, err := w.alice.SendConv(tctx(t), p.Root.ID(), ConvOutgoing{Body: "second exact selection"}); err != nil {
		t.Fatal(err)
	}
	refs, err = w.alice.SelectGroupHistory(tctx(t), p.Root.ID(), GroupHistorySelection{Last: 1})
	if err != nil || len(refs) != 1 {
		t.Fatal(err)
	}
	inv = groupLifecycleInvite(t, w, p, refs)
	awaitGroupInvitation(t, w.bob, inv.ID, "pending")
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	r, err = groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	var c protocol.GroupConsent
	if json.Unmarshal(r.consent, &c) != nil || c.Admission == nil || !slices.Equal(c.Admission.History, refs) {
		t.Fatal("consent history not exact")
	}
	changed := r.Proposal
	changed.History = slices.Clone(refs)
	changed.History[0].Hash = strings.Repeat("c", 64)
	if changed.ID() == inv.ID || w.alice.verifyGroupConsent(tctx(t), changed, *c.Admission) == nil {
		t.Fatal("history mutation accepted")
	}
	tooMany := make([]protocol.GroupHistoryRef, protocol.MaxGroupHistory+1)
	for i := range tooMany {
		tooMany[i] = protocol.GroupHistoryRef{LID: protocol.NewID(), Author: w.alice.Self().Fingerprint(), Hash: strings.Repeat("e", 64)}
	}
	bounded := inv.Proposal
	bounded.History = tooMany[:protocol.MaxGroupHistory]
	if err := bounded.Validate(); err != nil {
		t.Fatalf("64 distinct selected refs invalid: %v", err)
	}
	bounded.History = tooMany
	if bounded.Validate() == nil {
		t.Fatal("oversize selected refs accepted")
	}
}

func TestGroupLifecycleMissingProofWrongAuthorityAndExactKey(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	var raw []byte
	w.alice.store.db.QueryRow(`SELECT record FROM group_proof_records WHERE conv=? AND seq=0`, p.Root.ID()).Scan(&raw)
	var first protocol.GroupCommit
	if json.Unmarshal(raw, &first) != nil {
		t.Fatal("first record")
	}
	if err := w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.verifyGroupInvitation(tctx(t), inv.Proposal, p.State.Actor, w.alice.Address, w.alice.Self().Fingerprint()); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("missing proof: %v", err)
	}
	if err := w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{first}}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.verifyGroupInvitation(tctx(t), inv.Proposal, p.State.Actor, w.alice.Address, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*protocol.GroupInvitation){
		func(v *protocol.GroupInvitation) { v.Root.Realm = protocol.NewID() },
		func(v *protocol.GroupInvitation) { v.State.Sig = bytes.Repeat([]byte{0}, len(v.State.Sig)) },
		func(v *protocol.GroupInvitation) { v.Seq++ },
		func(v *protocol.GroupInvitation) { v.Roster = strings.Repeat("d", 64) },
	} {
		bad := inv.Proposal
		mutate(&bad)
		if err := w.bob.verifyGroupInvitation(tctx(t), bad, p.State.Actor, w.alice.Address, w.alice.Self().Fingerprint()); err == nil {
			t.Fatal("invalid invitation passed")
		}
	}
	bob, _, _ := w.bob.store.selfPerson(w.bob.Address)
	if err := w.bob.verifyGroupInvitation(tctx(t), inv.Proposal, bob.roster.Person, w.bob.Address, w.bob.Self().Fingerprint()); err == nil {
		t.Fatal("nonadmin inviter passed")
	}
	if err := w.bob.verifyGroupInvitation(tctx(t), inv.Proposal, p.State.Actor, w.alice.Address, w.bob.Self().Fingerprint()); err == nil {
		t.Fatal("wrong inviter key passed")
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
}

func TestGroupLifecycleOfflineRestartAndStaleConsent(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	// The durable association survives close/open, before any carrier is sent.
	home := w.alice.home
	w.alice.Close()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	w.alice = a
	var n int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM group_invitation_copies WHERE invitation=?`, inv.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("association not durable %d %v", n, err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		runAgent(t, a)
		publishGroupFixtureCaps(t, a, true)
	}
	awaitGroupInvitation(t, w.bob, inv.ID, "pending")
	// Another valid local admin transition advances the head without joining
	// the target. The old signature must never be rebased automatically.
	next := p
	next.State.Seq++
	next.State.Prev = p.State.Hash()
	next.State.Title = "Changed head"
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), c, next); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stale signed consent quarantined", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE reason=?`, reasonInvalid).Scan(&n)
		return n > 0
	})
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	var admission []byte
	if err = w.bob.store.db.QueryRow(`SELECT consent FROM group_invitations WHERE id=? AND direction='in'`, inv.ID).Scan(&admission); err != nil {
		t.Fatal(err)
	}
	var consent protocol.GroupConsent
	json.Unmarshal(admission, &consent)
	if consent.Admission == nil || consent.Admission.Seq != inv.Proposal.Seq || consent.Admission.Prev != inv.Proposal.Prev {
		t.Fatal("stale consent silently re-signed")
	}
	// No accepted local intent exists, so recovery cannot invent a publication.
	if err = w.alice.RecoverGroupInvitations(tctx(t)); err != nil {
		t.Fatal(err)
	}
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=?`, p.Root.ID()).Scan(&n)
	if n != 2 {
		t.Fatalf("stale consent published %d records", n)
	}
	stale, err := groupInvitationIn(w.alice.store.db, inv.ID, "out")
	if err != nil || stale.State != "stale" {
		t.Fatalf("stale proposal not visible %s %v", stale.State, err)
	}
	fresh := groupLifecycleInvite(t, w, next, nil)
	if fresh.ID == inv.ID || fresh.Proposal.Seq != 2 {
		t.Fatal("changed head did not require a new proposal")
	}
	awaitGroupInvitation(t, w.bob, fresh.ID, "pending")
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	if err = w.bob.DecideGroupInvitation(tctx(t), fresh.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "fresh explicit consent joins after stale proposal", func() bool { got, e := w.bob.GroupContext(p.Root.ID()); return e == nil && got.State.Seq == 2 })
}

func deliverGroupLifecycleSubtype(t *testing.T, sender, receiver *Agent, sub string) []envelope.Envelope {
	t.Helper()
	rows, err := sender.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub=? AND id IN(SELECT id FROM group_invitation_copies) ORDER BY rowid`, receiver.Address, sub)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &env); err != nil {
			break
		}
		envs = append(envs, env)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range envs {
		if err = sender.uploadAll(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if err = sender.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
			t.Fatal(err)
		}
		if err = receiver.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
	return envs
}

func TestGroupLifecycleReorderAtomicConsentAndPublicationRecovery(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	envs := deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	if len(envs) != 1 || heldReason(t, w.bob, envs[0].ID) != reasonProof {
		t.Fatal("reordered invitation was not proof-pending")
	}
	if _, err := groupInvitationIn(w.bob.store.db, inv.ID, "in"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unverified proposal displayed %v", err)
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	w.bob.retryProof(tctx(t))
	r, err := groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil || r.State != "pending" {
		t.Fatalf("ordered proof did not release proposal %v", err)
	}
	if _, err = w.bob.store.db.Exec(`CREATE TRIGGER fail_group_consent BEFORE INSERT ON outbox WHEN NEW.sub='group-consent' BEGIN SELECT RAISE(ABORT,'synthetic consent enqueue failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err == nil {
		t.Fatal("consent enqueue failure succeeded")
	}
	r, err = groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil || r.State != "pending" || len(r.consent) != 0 {
		t.Fatal("failed outbox retained consent")
	}
	w.bob.store.db.Exec(`DROP TRIGGER fail_group_consent`)
	if err = w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	r, _ = groupInvitationIn(w.bob.store.db, inv.ID, "in")
	original := bytes.Clone(r.consent)
	home := w.bob.home
	w.bob.Close()
	b, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	w.bob = b
	if err = b.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	r, _ = groupInvitationIn(b.store.db, inv.ID, "in")
	if !bytes.Equal(original, r.consent) {
		t.Fatal("restart changed recorded consent")
	}
	injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
	consents := deliverGroupLifecycleSubtype(t, w.bob, w.alice, envelope.SubGroupConsent)
	r, err = groupInvitationIn(w.alice.store.db, inv.ID, "out")
	if err != nil || r.State != "accepted" {
		t.Fatalf("response loss lost accepted intent %v", err)
	}
	var record []byte
	if err = w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=1 AND published=0`, p.Root.ID()).Scan(&record); err != nil {
		t.Fatal(err)
	}
	home = w.alice.home
	w.alice.Close()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	w.alice = a
	if err = a.RecoverGroupInvitations(tctx(t)); err != nil {
		t.Fatal(err)
	}
	r, err = groupInvitationIn(a.store.db, inv.ID, "out")
	if err != nil || r.State != "published" {
		t.Fatal("exact publication recovery incomplete")
	}
	var recovered []byte
	a.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=1`, p.Root.ID()).Scan(&recovered)
	if !bytes.Equal(record, recovered) {
		t.Fatal("recovery rebuilt original age ciphertext")
	}
	if err = a.accept(tctx(t), consents[0]); err != nil {
		t.Fatal(err)
	}
	if err = a.RecoverGroupInvitations(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var n int
	a.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=?`, p.Root.ID()).Scan(&n)
	if n != 2 {
		t.Fatalf("replayed accepted intent published %d", n)
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID()) // publication delivery has not occurred yet
}

func TestGroupLifecycleLinkedPersonOneSemanticProposal(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	phone, await, _ := linkPhone(t, w.bob, "phone")
	request := pendingLink(t, w.bob)
	if err := w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	inv := groupLifecycleInvite(t, w, p, nil)
	for _, a := range []*Agent{w.bob, phone} {
		r := awaitGroupInvitation(t, a, inv.ID, "pending")
		if r.Proposal.ID() != inv.ID {
			t.Fatal("per-device proposal ID")
		}
		assertNoGroupJoin(t, a, p.Root.ID())
	}
	// Pause the inviter's delivery of the published context so both devices
	// can consciously sign the same proposal. First valid person consent wins.
	if _, err := w.alice.store.db.Exec(`CREATE TRIGGER hold_group_context BEFORE INSERT ON outbox WHEN NEW.sub='group-context' BEGIN SELECT RAISE(ABORT,'synthetic paused fanout'); END`); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.bob, phone} {
		if err := a.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "linked person consent recorded", func() bool {
		r, e := groupInvitationIn(w.alice.store.db, inv.ID, "out")
		return e == nil && r.State == "accepted"
	})
	w.alice.store.db.Exec(`DROP TRIGGER hold_group_context`)
	if err := w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.RecoverGroupInvitations(tctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.bob, phone} {
		eventually(t, "linked devices joined once", func() bool {
			p, e := a.GroupContext(p.Root.ID())
			return e == nil && p.State.Seq == 1 && len(p.State.Members) == 2
		})
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=?`, p.Root.ID()).Scan(&n)
	if n != 2 {
		t.Fatalf("linked device duplicated publication %d", n)
	}
}

func TestGroupLifecycleSignedInvalidIngressAndChangedRosterRetry(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	for _, name := range []string{"state signature", "wrong realm", "wrong target", "wrong key"} {
		bad := inv.Proposal
		switch name {
		case "state signature":
			bad.State.Sig = bytes.Repeat([]byte{0}, len(bad.State.Sig))
		case "wrong realm":
			bad.Root.Realm = protocol.NewID()
			bad.Root.Sign(w.alice.id.Sign)
		case "wrong target":
			bad.Target = p.State.Actor
		}
		env := groupFixturePayload(t, w.alice, w.bob, p.Root, envelope.SubGroupInvite, protocol.GroupCarrier{V: 1, Seq: p.State.Seq, Hash: p.State.Hash()}, bad, false)
		if name == "wrong key" {
			in, err := envelope.Open(env, w.bob.id, w.bob.Address, w.alice.Self())
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(protocol.GroupCarrier{V: 1, Seq: p.State.Seq, Hash: p.State.Hash(), ToKey: w.alice.Self().Fingerprint()})
			in.Body = string(body)
			recipient, _ := w.bob.Self().Recipient()
			env, err = envelope.Seal(in, w.alice.id.Sign, recipient)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := w.alice.uploadAll(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if err := w.alice.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
			t.Fatal(err)
		}
		if err := w.bob.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if heldReason(t, w.bob, env.ID) != reasonInvalid {
			t.Fatalf("%s not refused", name)
		}
		assertNoGroupJoin(t, w.bob, p.Root.ID())
	}
	if list, err := w.bob.GroupInvitations(); err != nil || len(list) != 0 {
		t.Fatalf("invalid proposal displayed %+v %v", list, err)
	}
	admission, err := w.bob.SignGroupAdmission(p.Root, inv.Proposal.Seq, inv.Proposal.Prev, nil)
	if err != nil {
		t.Fatal(err)
	}
	unsolicited := protocol.GroupConsent{V: 1, Invitation: strings.Repeat("f", 64), Decision: "accepted", Admission: &admission}
	unsolicitedEnv := groupFixturePayload(t, w.bob, w.alice, p.Root, envelope.SubGroupConsent, protocol.GroupCarrier{V: 1, Seq: inv.Proposal.Seq, Hash: inv.Proposal.Prev}, unsolicited, false)
	if err = w.bob.uploadAll(tctx(t), unsolicitedEnv); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.hub.do(tctx(t), "POST", "/v1/messages", unsolicitedEnv, nil); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.accept(tctx(t), unsolicitedEnv); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, unsolicitedEnv.ID) != reasonInvalid {
		t.Fatal("unsolicited consent not permanently refused")
	}
	// One normal valid invitation is admitted, then the invited person's
	// independently signed roster changes before consent or queued retry.
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	runAgent(t, w.bob)
	_, await, _ := linkPhone(t, w.bob, "phone")
	request := pendingLink(t, w.bob)
	if err := w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); !errors.Is(err, ErrGroupInvitationStale) {
		t.Fatalf("changed roster consent %v", err)
	}
	var raw []byte
	w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND id IN(SELECT id FROM group_invitation_copies WHERE invitation=?) LIMIT 1`, envelope.SubGroupInvite, inv.ID).Scan(&raw)
	var env envelope.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	handled, allowed, err := w.alice.mayDeliverGroupLifecycle(env)
	if !handled || allowed || err != nil {
		t.Fatalf("stale queued exact key %v %v %v", handled, allowed, err)
	}
	r, err := groupInvitationIn(w.alice.store.db, inv.ID, "out")
	if err != nil || r.State != "stale" {
		t.Fatalf("stale invitation not visible %+v %v", r, err)
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
}

func TestGroupLifecycleSelectedOriginalRefsLeakNoHistoryOrFiles(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	phone, await, _ := linkPhone(t, w.alice, "phone")
	request := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	copies, err := w.alice.groupDeliveryCopies(tctx(t), p)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "linked creator gets only current context", func() bool { _, e := phone.GroupContext(p.Root.ID()); return e == nil })
	var selected protocol.GroupHistoryRef
	var oldBlobs []string
	for _, name := range []string{"selected-private.bin", "unselected-private.bin"} {
		path := filepath.Join(t.TempDir(), name)
		if err = os.WriteFile(path, []byte("private file bytes "+name), 0600); err != nil {
			t.Fatal(err)
		}
		sent, e := w.alice.SendConv(tctx(t), p.Root.ID(), ConvOutgoing{Body: "private earlier turn " + name, Files: []OutgoingFile{{Path: path, Name: name}}})
		if e != nil {
			t.Fatal(e)
		}
		env := groupTurnEnvelope(t, w.alice, sent.ID)
		in, e := envelope.Open(env, phone.id, phone.Address, w.alice.Self())
		if e != nil {
			t.Fatal(e)
		}
		oldBlobs = append(oldBlobs, in.Attachments[0].Blob.ID)
		if name == "selected-private.bin" {
			selected = protocol.GroupHistoryRef{LID: in.LID, Author: w.alice.Self().Fingerprint(), Hash: contentHash(in)}
		}
	}
	inv := groupLifecycleInvite(t, w, p, []protocol.GroupHistoryRef{selected})
	r := awaitGroupInvitation(t, w.bob, inv.ID, "pending")
	if !slices.Equal(r.Proposal.History, []protocol.GroupHistoryRef{selected}) {
		t.Fatal("original selected ref changed")
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	var leaked int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM attachments WHERE name IN('selected-private.bin','unselected-private.bin')`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("prejoin old file metadata %d %v", leaked, err)
	}
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE body LIKE '%private earlier turn%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("prejoin old plaintext %d %v", leaked, err)
	}
	for _, blob := range oldBlobs {
		if _, e := os.Stat(w.bob.downloadPath(blob)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("prejoin old file bytes %v", e)
		}
	}
	if err = w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	r, err = groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	var consent protocol.GroupConsent
	if json.Unmarshal(r.consent, &consent) != nil || consent.Admission == nil || !consent.Admission.AllowsHistory(selected) {
		t.Fatal("signed original ref absent")
	}
	// Importer/file-gate implementation is a later required slice; accepting a
	// ref does not claim its message or attachment has been delivered here.
}

func TestGroupLifecycleOfflineExplicitConsentReconnect(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	w.hub.Stop()
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatalf("offline explicit consent not durably queued: %v", err)
	}
	r, err := groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil || r.State != "accepted" || len(r.consent) == 0 {
		t.Fatal("offline exact consent lost")
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
	for _, a := range []*Agent{w.alice, w.bob} {
		runAgent(t, a)
		publishGroupFixtureCaps(t, a, true)
	}
	eventually(t, "offline explicit consent joins on reconnect", func() bool { got, e := w.bob.GroupContext(p.Root.ID()); return e == nil && got.State.Seq == 1 })
}

// BUG-13: several invitations at one head. The first acceptance moves the
// group on; a later one was signed for the old head and is never rebased.
// The inviter re-issues it at the new head with the same target and history,
// the invitee's earlier consent shows as stale, and a fresh acceptance joins.
func TestGroupLifecycleConcurrentInvitationsReissuedForFreshConsent(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	carol := proofReader(t, w, "carol")
	runAgent(t, carol)
	publishGroupFixtureCaps(t, carol, true)
	carolSelf, _, err := carol.store.selfPerson(carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	conv := p.Root.ID()
	bobInv := groupLifecycleInvite(t, w, p, nil)
	carolInv, err := w.alice.InviteGroup(tctx(t), conv, carolSelf.roster.Person, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitGroupInvitation(t, w.bob, bobInv.ID, "pending")
	awaitGroupInvitation(t, carol, carolInv.ID, "pending")
	if err = w.bob.DecideGroupInvitation(tctx(t), bobInv.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first acceptance published", func() bool {
		r, e := groupInvitationIn(w.alice.store.db, bobInv.ID, "out")
		return e == nil && r.State == "published"
	})
	if err = carol.DecideGroupInvitation(tctx(t), carolInv.ID, true); err != nil {
		t.Fatal(err)
	}
	var fresh GroupInvitationInfo
	eventually(t, "a fresh invitation for carol at the new head", func() bool {
		list, e := carol.GroupInvitations()
		if e != nil {
			return false
		}
		for _, i := range list {
			if i.Direction == "in" && i.ID != carolInv.ID && i.State == "pending" && i.Proposal.Target == carolSelf.roster.Person && i.Proposal.Seq == 2 {
				fresh = i
				return true
			}
		}
		return false
	})
	if !slices.Equal(fresh.Proposal.History, carolInv.Proposal.History) {
		t.Fatal("re-issued invitation changed the selected history")
	}
	eventually(t, "carol's earlier consent shown as stale", func() bool {
		r, e := groupInvitationIn(carol.store.db, carolInv.ID, "in")
		return e == nil && r.State == "stale"
	})
	if r, e := groupInvitationIn(w.alice.store.db, carolInv.ID, "out"); e != nil || r.State != "stale" {
		t.Fatalf("inviter's old invitation %s %v", r.State, e)
	}
	if r, e := groupInvitationIn(w.alice.store.db, fresh.ID, "out"); e != nil || r.State != "pending" {
		t.Fatalf("inviter's re-issued invitation %s %v", r.State, e)
	}
	if err = carol.DecideGroupInvitation(tctx(t), carolInv.ID, true); err == nil || !strings.Contains(err.Error(), "newer invitation") {
		t.Fatalf("accepting the stale invitation again: %v", err)
	}
	if err = carol.DecideGroupInvitation(tctx(t), fresh.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		eventually(t, "every invitee joined at "+a.Address, func() bool {
			got, e := a.GroupContext(conv)
			return e == nil && got.State.Seq == 2 && len(got.State.Members) == 3
		})
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=?`, conv).Scan(&n)
	if n != 3 {
		t.Fatalf("publications %d, want create and two admissions", n)
	}
	// Re-issuing happens once: recovery finds the successor and adds nothing.
	if err = w.alice.RecoverGroupInvitations(tctx(t)); err != nil {
		t.Fatal(err)
	}
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_invitations WHERE direction='out' AND conv=?`, conv).Scan(&n)
	if n != 3 {
		t.Fatalf("outgoing invitations %d, want bob's, carol's stale one and its re-issue", n)
	}
}

// A decline that arrives after the group moved on is never answered with
// another invitation: only an acceptance is re-issued.
func TestGroupLifecycleStaleDeclineNotReissued(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	carol := proofReader(t, w, "carol")
	runAgent(t, carol)
	publishGroupFixtureCaps(t, carol, true)
	carolSelf, _, err := carol.store.selfPerson(carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	conv := p.Root.ID()
	bobInv := groupLifecycleInvite(t, w, p, nil)
	carolInv, err := w.alice.InviteGroup(tctx(t), conv, carolSelf.roster.Person, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitGroupInvitation(t, w.bob, bobInv.ID, "pending")
	awaitGroupInvitation(t, carol, carolInv.ID, "pending")
	if err = w.bob.DecideGroupInvitation(tctx(t), bobInv.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first acceptance published", func() bool {
		r, e := groupInvitationIn(w.alice.store.db, bobInv.ID, "out")
		return e == nil && r.State == "published"
	})
	if err = carol.DecideGroupInvitation(tctx(t), carolInv.ID, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the late decline at alice", func() bool {
		r, e := groupInvitationIn(w.alice.store.db, carolInv.ID, "out")
		return e == nil && r.State == "stale"
	})
	if err = w.alice.RecoverGroupInvitations(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_invitations WHERE direction='out' AND conv=?`, conv).Scan(&n)
	if n != 2 {
		t.Fatalf("a decline was answered with another invitation: %d outgoing", n)
	}
}
