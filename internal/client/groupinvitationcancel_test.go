package client

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupInvitationCancelFencesLateConsentAndReinvite(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	duplicate := groupLifecycleInvite(t, w, p, nil)
	if duplicate.ID != inv.ID {
		t.Fatal("repeated Invite duplicated live proposal")
	}
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.CancelGroupInvitation(tctx(t), inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.CancelGroupInvitation(tctx(t), inv.ID); err != nil {
		t.Fatal(err)
	}
	deliverGroupLifecycleSubtype(t, w.bob, w.alice, envelope.SubGroupConsent)
	if err := w.alice.RecoverGroupInvitations(tctx(t)); err != nil {
		t.Fatal(err)
	}
	old, err := groupInvitationIn(w.alice.store.db, inv.ID, "out")
	if err != nil || old.State != "cancelled" {
		t.Fatalf("late consent revived cancellation: %+v %v", old, err)
	}
	if err = w.alice.PublishGroupInvitation(tctx(t), inv.ID); err == nil {
		t.Fatal("cancelled invitation published")
	}
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupConsent)
	incoming, err := groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil || incoming.State != "cancelled" {
		t.Fatalf("signed cancellation not applied: %+v %v", incoming, err)
	}
	if err = w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err == nil {
		t.Fatal("cancelled invitation accepted again")
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
	fresh := groupLifecycleInvite(t, w, p, nil)
	if fresh.ID == inv.ID || fresh.Proposal.Nonce == "" {
		t.Fatal("cancel then invite reused old proposal identity")
	}
	if fresh.Proposal.Seq != inv.Proposal.Seq || fresh.Proposal.Prev != inv.Proposal.Prev {
		t.Fatal("unchanged state unexpectedly rebased")
	}
	var n int
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM group_invitations WHERE direction='out' AND conv=?`, p.Root.ID()).Scan(&n); err != nil || n != 2 {
		t.Fatalf("cancelled invitation automatically reissued: %d %v", n, err)
	}
}

func TestGroupInvitationCancellationBeforeProposalAndWrongSigner(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	if err := w.alice.CancelGroupInvitation(tctx(t), inv.ID); err != nil {
		t.Fatal(err)
	}
	notices := deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupConsent)
	if len(notices) != 1 {
		t.Fatalf("cancellation notices: %d", len(notices))
	}
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	oldCopies := deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	incoming, err := groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil || incoming.State != "cancelled" {
		t.Fatalf("late proposal revived cancellation: %+v %v", incoming, err)
	}
	for _, copy := range oldCopies {
		handled, allowed, e := w.alice.mayDeliverGroupLifecycle(copy)
		if e != nil || !handled || allowed {
			t.Fatalf("queued old proposal passed cancellation fence %t %t %v", handled, allowed, e)
		}
	}
	fresh := groupLifecycleInvite(t, w, p, nil)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	carol := proofReader(t, w, "cancel-forger")
	forged := groupFixturePayload(t, carol, w.bob, p.Root, envelope.SubGroupConsent, protocol.GroupCarrier{V: 1, Seq: fresh.Proposal.Seq, Hash: fresh.Proposal.Prev}, protocol.GroupConsent{V: 1, Invitation: fresh.ID, Decision: "cancelled"}, false)
	if err = carol.uploadAll(tctx(t), forged); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.accept(tctx(t), forged); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.bob, forged.ID) != reasonInvalid {
		t.Fatal("another signed person's cancellation was not refused")
	}
	row, err := groupInvitationIn(w.bob.store.db, fresh.ID, "in")
	if err != nil || row.State != "pending" {
		t.Fatalf("wrong signer cancelled proposal: %+v %v", row, err)
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
}

func TestGroupInvitationExplicitRefreshAfterHeadAdvances(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	next, err := w.alice.RenameGroup(tctx(t), p.Root.ID(), "New reviewed group title")
	if err != nil {
		t.Fatal(err)
	}
	record, err := groupProofRecord(w.alice.store.db, p.Root.ID(), p.Root.Creator.Fingerprint, next.State.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{record}}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); !errors.Is(err, ErrGroupInvitationStale) {
		t.Fatalf("old head accepted: %v", err)
	}
	fresh, err := w.alice.RefreshGroupInvitation(tctx(t), inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	retry, e := w.alice.RefreshGroupInvitation(tctx(t), inv.ID)
	if e != nil || retry.ID != fresh.ID {
		t.Fatalf("refresh retry duplicated successor: %s %v", retry.ID, e)
	}
	var count int
	if e = w.alice.store.db.QueryRow(`SELECT count(*) FROM group_invitations WHERE direction='out' AND conv=?`, p.Root.ID()).Scan(&count); e != nil || count != 2 {
		t.Fatalf("refresh generated extra invitations: %d %v", count, e)
	}
	if fresh.ID == inv.ID || fresh.Proposal.Seq != next.State.Seq+1 || fresh.Proposal.State.Title != next.State.Title {
		t.Fatal("refresh did not produce new current-state proposal")
	}
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupConsent)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	if err = w.bob.DecideGroupInvitation(tctx(t), fresh.ID, true); err != nil {
		t.Fatal(err)
	}
	old, err := groupInvitationIn(w.bob.store.db, inv.ID, "in")
	if err != nil || old.State != "cancelled" {
		t.Fatalf("old consent still eligible: %v %v", old.State, err)
	}
	// Until the inviter receives that new consent, refreshed proposal grants no access.
	assertNoGroupJoin(t, w.bob, p.Root.ID())
}

func TestGroupInvitationCancelRefusesPublicationCustody(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	if err := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	// A real publication whose response/install is uncertain must not be fenced
	// as cancelled. Its signed admission could already be in Hub custody.
	admission, err := w.bob.SignGroupAdmission(p.Root, inv.Proposal.Seq, inv.Proposal.Prev, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := p
	next.State.Seq = inv.Proposal.Seq
	next.State.Prev = inv.Proposal.Prev
	next.State.Members = append(next.State.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: admission.Person, Roster: admission.Roster}, Admission: admission})
	slices.SortFunc(next.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, next); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.CancelGroupInvitation(tctx(t), inv.ID); err == nil || !strings.Contains(err.Error(), "publication already started") {
		t.Fatalf("published membership cancellation: %v", err)
	}
	row, err := groupInvitationIn(w.alice.store.db, inv.ID, "out")
	if err != nil || row.State != "pending" {
		t.Fatalf("publication cancellation changed intent: %s %v", row.State, err)
	}
}
