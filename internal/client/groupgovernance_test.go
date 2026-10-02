package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func groupGovernanceAwait(t *testing.T, packet GroupContext, agents ...*Agent) {
	t.Helper()
	for _, a := range agents {
		eventually(t, "exact governance context", func() bool {
			p, e := a.GroupContext(packet.State.Conv)
			return e == nil && p.State.Hash() == packet.State.Hash()
		})
	}
}
func groupGovernanceDeparture(t *testing.T, from, to *Agent) envelope.Envelope {
	t.Helper()
	var raw []byte
	var env envelope.Envelope
	if err := from.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub=? ORDER BY rowid DESC LIMIT 1`, to.Address, envelope.SubGroupWithdrawal).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env
}
func groupGovernanceDeliver(t *testing.T, from, to *Agent, env envelope.Envelope) {
	t.Helper()
	if err := from.uploadAll(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if err := from.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
		t.Fatal(err)
	}
	if err := to.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
}

func TestGroupGovernanceLinkedAdminAndTransfer(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	phone, await, _ := linkPhone(t, w.alice, "governance-phone")
	if err := w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	stopPhone := runAgent(t, phone)
	defer stopPhone()
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
	groupGovernanceAwait(t, p, phone)
	alice, _, _ := w.alice.store.selfPerson(w.alice.Address)
	bob, _, _ := w.bob.store.selfPerson(w.bob.Address)
	cp, _, _ := carol.store.selfPerson(carol.Address)
	if _, err = carol.RenameGroup(tctx(t), p.State.Conv, "unauthorized"); err == nil {
		t.Fatal("ordinary member renamed")
	}
	if _, err = carol.PromoteGroupMember(tctx(t), p.State.Conv, cp.roster.Person); err == nil {
		t.Fatal("ordinary member promoted itself")
	}
	original := p.Root
	p, err = phone.RenameGroup(tctx(t), p.State.Conv, "Linked administrator rename")
	if err != nil {
		t.Fatal(err)
	}
	if !sameGroupRoot(original, p.Root) || p.State.By != phone.Self().Fingerprint() {
		t.Fatal("rename replaced original root or did not use linked key")
	}
	groupGovernanceAwait(t, p, w.alice, w.bob, carol)
	p, err = phone.PromoteGroupMember(tctx(t), p.State.Conv, bob.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.alice, w.bob, carol)
	p, err = phone.PromoteGroupMember(tctx(t), p.State.Conv, cp.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.bob, carol)
	p, err = w.bob.DemoteGroupMember(tctx(t), p.State.Conv, cp.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, phone, w.alice, carol)
	p, err = phone.DemoteGroupMember(tctx(t), p.State.Conv, alice.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.alice, w.bob, carol)
	if _, err = phone.RenameGroup(tctx(t), p.State.Conv, "stale administrator"); err == nil {
		t.Fatal("demoted linked device retained administrator authority")
	}
	for _, action := range []func() error{
		func() error { _, e := w.bob.DemoteGroupMember(tctx(t), p.State.Conv, bob.roster.Person); return e },
		func() error { _, e := w.bob.RemoveGroupMember(tctx(t), p.State.Conv, bob.roster.Person); return e },
		func() error { _, e := w.bob.LeaveGroup(tctx(t), p.State.Conv); return e },
	} {
		if e := action(); !errors.Is(e, ErrGroupLastAdmin) {
			t.Fatalf("last administrator refusal: %v", e)
		}
	}
	p, err = w.bob.PromoteGroupMember(tctx(t), p.State.Conv, alice.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.alice, phone, carol)
	result, err := w.alice.LeaveGroup(tctx(t), p.State.Conv)
	if err != nil || result.Context == nil || result.Withdrawal != nil || result.Queued {
		t.Fatalf("administrator departure not exact CAS: %+v %v", result, err)
	}
	p = *result.Context
	groupGovernanceAwait(t, p, w.bob, carol)
	if _, ok := p.State.Member(alice.roster.Person); ok {
		t.Fatal("administrator self-removal did not remove whole person")
	}
	stops[w.bob]()
	stops[carol]()
	path, _ := writeFile(t, t.TempDir(), "queued-after-transfer.bin", 4096)
	sent, err := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "queued only for then-member Carol", Files: []OutgoingFile{{Path: path, Name: "queued-after-transfer.bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	old := groupTurnEnvelope(t, w.bob, sent.ID)
	p, err = w.bob.RemoveGroupMember(tctx(t), p.State.Conv, cp.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, e := w.bob.mayDeliver(old); allowed || e != nil {
		t.Fatalf("removed ordinary member regained queued file: %v %v", allowed, e)
	}

}

func TestGroupGovernanceOfflineLeaveRestartExactFanout(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	stops[w.bob]()
	path, _ := writeFile(t, t.TempDir(), "pre-leave-queued.bin", 2048)
	// SendConv attempts direct delivery even when its daemon is stopped. Stop
	// custody only after the required roster reads, before the queued write.
	beforeOutbox = func() { w.hub.Stop() }
	t.Cleanup(func() { beforeOutbox = func() {} })
	pending, err := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "unsent before own leave", Files: []OutgoingFile{{Path: path, Name: "pre-leave-queued.bin"}}})
	beforeOutbox = func() {}
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != stateQueued {
		t.Fatalf("pre-leave turn reached custody: %s", pending.State)
	}
	oldTurn := groupTurnEnvelope(t, w.bob, pending.ID)
	result, err := w.bob.LeaveGroup(tctx(t), p.State.Conv)
	if err != nil || !result.Queued || result.Withdrawal == nil {
		t.Fatalf("offline atomic leave: %+v %v", result, err)
	}
	original := *result.Withdrawal
	if allowed, e := w.bob.mayDeliver(oldTurn); allowed || e != nil {
		t.Fatalf("departure exception allowed ordinary queued file: %v %v", allowed, e)
	}
	var n int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n); err != nil || n != 2 {
		t.Fatalf("offline fanout not atomic: %d %v", n, err)
	}
	pins, err := w.bob.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 1 || !sameGroupWithdrawal(pins[0], original) {
		t.Fatal("offline pin not persisted with fanout")
	}
	if _, err = w.bob.GroupMembers(p.State.Conv); err == nil {
		t.Fatal("local departure not immediate")
	}
	home := w.bob.home
	w.bob.Close()
	b, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	w.bob = b
	again, err := b.LeaveGroup(tctx(t), p.State.Conv)
	if err != nil || again.Withdrawal == nil || !sameGroupWithdrawal(*again.Withdrawal, original) {
		t.Fatalf("restart changed departure signature: %v", err)
	}
	b.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n)
	if n != 2 {
		t.Fatal("restart duplicated semantic recipient fanout")
	}
	w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
	runAgent(t, b)
	// All three daemons reconnect to a fresh fixture Hub; only their current
	// sessions receive fixture grp1, while production capability remains absent.
	for _, a := range []*Agent{w.alice, b, carol} {
		publishGroupFixtureCaps(t, a, true)
	}
	eventually(t, "both peers persist departure overlay", func() bool {
		for _, a := range []*Agent{w.alice, carol} {
			pins, e := a.groupWithdrawals(p.State.Conv)
			if e != nil || !groupWithdrawalPinned(pins, original) {
				return false
			}
		}
		return true
	})
	for _, a := range []*Agent{w.alice, carol} {
		rows, e := a.ConversationMessages(p.State.Conv)
		if e != nil || len(rows) != 0 {
			t.Fatalf("quiet leave appeared as ordinary turn %v", e)
		}
	}
	next, err := w.alice.RenameGroup(tctx(t), p.State.Conv, "Leave folded into CAS")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.State.Member(original.Person); ok {
		t.Fatal("next CAS did not fold ordinary withdrawal")
	}
	groupGovernanceAwait(t, next, carol)
	sent, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "after departure"})
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range sent.Copies {
		if copy.To == b.Address {
			t.Fatal("departed person receives future turn")
		}
	}
	if _, err = b.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "silent rejoin"}); err == nil {
		t.Fatal("departure revived without fresh consent")
	}
	if err = b.RecoverGroupWithdrawals(tctx(t)); err != nil {
		t.Fatal(err)
	}
	b.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n)
	if n != 2 {
		t.Fatal("recovery rebroadcast duplicate departure")
	}
}

func TestGroupGovernanceLeaveReorderAndPromotionConflict(t *testing.T) {
	w, p, wdraw := ordinaryWithdrawalFixture(t)
	// Existing SignGroupWithdrawal now persists exact carrier bytes as well as pin.
	env := groupGovernanceDeparture(t, w.bob, w.alice)
	if _, err := w.alice.store.db.Exec(`DELETE FROM group_context WHERE conv=?`, p.State.Conv); err != nil {
		t.Fatal(err)
	}
	groupGovernanceDeliver(t, w.bob, w.alice, env)
	var reason string
	if err := w.alice.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, env.ID).Scan(&reason); err != nil || reason != reasonProof {
		t.Fatalf("missing context departure not pending: %q %v", reason, err)
	}
	c, err := groupProofRecord(w.alice.store.db, p.State.Conv, p.Root.Creator.Fingerprint, p.State.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AcceptGroupCommit(tctx(t), c); err != nil {
		t.Fatal(err)
	}
	w.alice.retryProof(tctx(t))
	pins, err := w.alice.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 1 || !sameGroupWithdrawal(pins[0], wdraw) {
		t.Fatal("reordered departure not admitted exactly once")
	}
	if err = w.alice.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n)
	if n != 1 {
		t.Fatal("quiet carrier duplicate timeline/receipt")
	}
	// Separate valid world: promotion wins the public CAS before ordinary leave
	// arrives. The old departure cannot remove or demote that current admin.
	w2, p2, w2draw := ordinaryWithdrawalFixture(t)
	promoted, err := w2.alice.PromoteGroupMember(tctx(t), p2.State.Conv, w2draw.Person)
	if err != nil {
		t.Fatal(err)
	}
	old := groupGovernanceDeparture(t, w2.bob, w2.alice)
	groupGovernanceDeliver(t, w2.bob, w2.alice, old)
	current, err := w2.alice.GroupContext(p2.State.Conv)
	if err != nil || current.State.Hash() != promoted.State.Hash() {
		t.Fatal("departure overwrote promotion CAS")
	}
	m, ok := current.State.Member(w2draw.Person)
	if !ok || !m.Admin {
		t.Fatal("ordinary departure removed promoted administrator")
	}
	pins, err = w2.alice.groupWithdrawals(p2.State.Conv)
	if err != nil || len(pins) != 0 {
		t.Fatal("promotion conflict installed withdrawal")
	}
	pending, err := groupWithdrawalRows(w2.alice.store.db, "group_pending_withdrawals", p2.State.Conv)
	if err != nil || len(pending) != 1 {
		t.Fatal("promotion conflict not retained for explicit resolution")
	}
}

func TestGroupGovernanceCASResponseLossExactRecovery(t *testing.T) {
	w, p := groupOrdinaryPublication(t)
	injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
	_, err := w.alice.RenameGroup(tctx(t), p.State.Conv, "Exact response-loss rename")
	if err == nil {
		t.Fatal("injected lost response hidden")
	}
	var original []byte
	if err = w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=? AND published=0`, p.State.Conv, p.State.Seq+1).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var recovered []byte
	w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=?`, p.State.Conv, p.State.Seq+1).Scan(&recovered)
	if !bytes.Equal(original, recovered) {
		t.Fatal("governance recovery re-signed/re-encrypted")
	}
	current, err := w.alice.GroupContext(p.State.Conv)
	if err != nil || current.State.Title != "Exact response-loss rename" {
		t.Fatal("committed governance not recovered")
	}
	got, _ := json.Marshal(current.State.Members)
	want, _ := json.Marshal(p.State.Members)
	if !bytes.Equal(got, want) {
		t.Fatal("rename mutated existing admissions")
	}
}

func TestGroupGovernanceDepartingAdminResponseLossRestart(t *testing.T) {
	w, p := groupOrdinaryPublication(t)
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	p, err = w.alice.PromoteGroupMember(tctx(t), p.State.Conv, bob.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
	_, err = w.alice.LeaveGroup(tctx(t), p.State.Conv)
	if err == nil {
		t.Fatal("lost departing-administrator custody hidden")
	}
	var original []byte
	if err = w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=? AND published=0`, p.State.Conv, p.State.Seq+1).Scan(&original); err != nil {
		t.Fatal(err)
	}
	home := w.alice.home
	w.alice.Close()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w.alice = a
	if err = a.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatalf("exact departing-administrator POST replay did not recover: %v", err)
	}
	var recovered []byte
	if err = a.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=? AND published=1`, p.State.Conv, p.State.Seq+1).Scan(&recovered); err != nil || !bytes.Equal(original, recovered) {
		t.Fatalf("departure recovery changed original bytes: %v", err)
	}
	current, err := a.GroupContext(p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	own, _, _ := a.store.selfPerson(a.Address)
	if _, ok := current.State.Member(own.roster.Person); ok {
		t.Fatal("exact committed departure did not install")
	}
	if err = a.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND sub='group-context' AND json_extract(body,'$.seq')=?`, p.State.Conv, p.State.Seq+1).Scan(&n); err != nil || n != 1 {
		t.Fatalf("departure restart duplicated/skipped fanout: %d %v", n, err)
	}
}
