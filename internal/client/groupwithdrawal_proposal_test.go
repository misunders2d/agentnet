package client

import (
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
	"time"
)

func ordinaryWithdrawalFixture(t *testing.T) (*world, GroupContext, protocol.GroupWithdrawal) {
	t.Helper()
	w, p, c := newGroupPublicationFixture(t)
	first := c
	if _, err := w.alice.PublishGroup(tctx(t), c, p); err != nil {
		t.Fatal(err)
	}
	next := p
	next.Proof = []protocol.GroupState{p.State}
	next.State.Seq++
	next.State.Prev = p.State.Hash()
	next.State.Members = append([]protocol.GroupMember{}, p.State.Members...)
	own, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	for i := range next.State.Members {
		if next.State.Members[i].Person == own.roster.Person {
			next.State.Members[i].Admin = false
		}
	}
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	c, err = w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), c, next); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.IngestGroupProofPage(tctx(t), next.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{first, c}}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.AcceptGroupCommit(tctx(t), c); err != nil {
		t.Fatal(err)
	}
	withdrawal, err := w.bob.SignGroupWithdrawal(tctx(t), next.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	return w, next, withdrawal
}

func TestGroupWithdrawalProposalPendingBeforeContext(t *testing.T) {
	w, p, withdrawal := ordinaryWithdrawalFixture(t)
	reader := w.alice
	if _, err := reader.store.db.Exec("DELETE FROM group_context WHERE conv=?", p.State.Conv); err != nil {
		t.Fatal(err)
	}
	if err := reader.AcceptGroupWithdrawal(tctx(t), withdrawal); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("pending receipt: %v", err)
	}
	pending, err := groupWithdrawalRows(reader.store.db, "group_pending_withdrawals", p.State.Conv)
	if err != nil || len(pending) != 1 {
		t.Fatalf("durable pending: %v %v", pending, err)
	}
	record, err := groupProofRecord(reader.store.db, p.State.Conv, p.Root.Creator.Fingerprint, p.State.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.AcceptGroupCommit(tctx(t), record); err != nil {
		t.Fatal(err)
	}
	pins, err := reader.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 1 {
		t.Fatalf("pins: %v %v", pins, err)
	}
}

func TestGroupWithdrawalProposalPromotionRollback(t *testing.T) {
	w, p, withdrawal := ordinaryWithdrawalFixture(t)
	promoted := p
	promoted.State.Seq++
	promoted.State.Prev = p.State.Hash()
	promoted.State.Members = append([]protocol.GroupMember{}, p.State.Members...)
	for i := range promoted.State.Members {
		if promoted.State.Members[i].Person == withdrawal.Person {
			promoted.State.Members[i].Admin = true
		}
	}
	tx, err := w.alice.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = installGroupContext(tx, promoted); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = w.alice.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = installGroupMembership(tx, p, []protocol.GroupWithdrawal{withdrawal}); err == nil {
		t.Fatal("concurrent promotion accepted")
	}
	tx.Rollback()
	pins, err := w.alice.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 0 {
		t.Fatalf("rollback pins: %v %v", pins, err)
	}
}

func TestGroupWithdrawalProposalAcceptedPinsSurviveRosterAdvance(t *testing.T) {
	w, p, withdrawal := ordinaryWithdrawalFixture(t)
	if err := w.alice.AcceptGroupWithdrawal(tctx(t), withdrawal); err != nil {
		t.Fatal(err)
	}
	own, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	roster := own.roster
	roster.Seq++
	roster.Prev = own.roster.Hash()
	roster.Label = "New current roster"
	roster.By = w.bob.id.Public(w.bob.Address).Fingerprint()
	roster.Sign(w.bob.id.Sign)
	raw, _ := json.Marshal(roster)
	if _, err = w.bob.store.pinChain(roster.Person, [][]byte{raw}, w.bob.Self(), true); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.publishPerson(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AcceptGroupWithdrawal(tctx(t), withdrawal); err != nil {
		t.Fatalf("accepted exact pin lost: %v", err)
	}
	reader := proofReader(t, w, "old-roster-reader")
	if err = reader.AcceptGroupWithdrawal(tctx(t), withdrawal); err == nil {
		t.Fatal("new receipt with old roster accepted")
	}
	_ = p
}

func TestGroupWithdrawalProposalRemovedLinkedSigner(t *testing.T) {
	w, p, _ := ordinaryWithdrawalFixture(t)
	runAgent(t, w.bob)
	phone, awaited, _ := linkPhone(t, w.bob, "group-phone")
	req := pendingLink(t, w.bob)
	if err := w.bob.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-awaited:
		if out.err != nil {
			t.Fatal(out.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("linked signer timeout")
	}
	own, ok, err := phone.store.selfPerson(phone.Address)
	if err != nil || !ok {
		t.Fatal(err)
	}
	member, _ := p.State.Member(own.roster.Person)
	withdrawal := protocol.GroupWithdrawal{Conv: p.State.Conv, Realm: p.State.Realm, Person: own.roster.Person, Admission: member.Admission.Hash(), Roster: own.roster.Hash(), By: phone.Self().Fingerprint()}
	withdrawal.Sign(phone.id.Sign)
	// Pin the linked roster via the approved receive boundary before removal.
	if err = w.alice.AcceptGroupWithdrawal(tctx(t), withdrawal); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AcceptGroupWithdrawal(tctx(t), withdrawal); err != nil {
		t.Fatalf("accepted pin disappeared after removal: %v", err)
	}
	reader := proofReader(t, w, "removed-device-reader")
	if err = reader.AcceptGroupWithdrawal(tctx(t), withdrawal); err == nil || errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("new removed signer evidence accepted/deferred: %v", err)
	}
}

func TestGroupWithdrawalProposalPublishPinsPending(t *testing.T) {
	w, p, withdrawal := ordinaryWithdrawalFixture(t)
	if err := w.alice.deferGroupWithdrawal(withdrawal); err != nil {
		t.Fatal(err)
	}
	next := p
	next.Proof = nil
	next.State.Seq++
	next.State.Prev = p.State.Hash()
	next.State.Title = "After withdrawal"
	next, err := w.alice.SignGroupState(tctx(t), next)
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
	pins, err := w.alice.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 1 {
		t.Fatalf("publication did not atomically pin: %v %v", pins, err)
	}
	pending, err := groupWithdrawalRows(w.alice.store.db, "group_pending_withdrawals", p.State.Conv)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending not resolved: %v %v", pending, err)
	}
}
