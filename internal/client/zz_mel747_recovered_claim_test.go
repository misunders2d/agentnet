// Kept last in discovery order to preserve main's client shard composition.
package client

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// mel747FreezeOldOnRecovered stores the old person on the recovered device
// as an earlier client did: pinned without asking the relay, so the email
// its own person holds froze it as a claim (conflict NULL).
func mel747FreezeOldOnRecovered(t *testing.T, w *googleReviewWorld, recovered *Agent, old protocol.PersonRoster) {
	t.Helper()
	raws, e := recovered.fetchChain(w.ctx, old.Person, -1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = recovered.store.pinChain(old.Person, raws, recovered.Self(), false); !errors.Is(e, errPersonConflict) {
		t.Fatal("fixture: old person not frozen as an email claim", e)
	}
	if claim, e := recovered.store.emailClaimOnly(old.Person); e != nil || !claim {
		t.Fatal("fixture: not an email-claim freeze", claim, e)
	}
	if known, e := revokedDeviceKnown(recovered.store.db, old.Devices[0].Address, old.Devices[0].Fingerprint()); e != nil || known {
		t.Fatal("fixture: recovered device already knows the revocation", known, e)
	}
}

type mel747Fixture struct {
	w     *googleReviewWorld
	peer  *Agent
	old   protocol.PersonRoster
	root  protocol.ConvRoot
	fresh *Agent
	lost  *Agent // the old person's device, revoked
}

// mel747Recovered builds Bohdan's production state: a group naming his old
// person, that person's only device revoked, his new device joined with the
// same email and, as under v0.10.7, the old person frozen there as a claim.
func mel747Recovered(t *testing.T) mel747Fixture {
	t.Helper()
	w := newMEL736OneWorld(t)
	oldDevice := mel736OneJoin(t, w, "lost")
	peer := w.admin
	if _, e := peer.CreatePerson(w.ctx, "Colleague"); e != nil {
		t.Fatal(e)
	}
	stopOld := runAgent(t, oldDevice)
	runAgent(t, peer)
	waitNamedAgentCaps(t, oldDevice)
	waitNamedAgentCaps(t, peer)
	old, _, _ := oldDevice.store.selfPerson(oldDevice.Address)
	me, _, _ := peer.store.selfPerson(peer.Address)
	realm, _ := peer.RealmID()
	root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: protocol.ConvCreator{Person: me.roster.Person, Roster: me.roster.Hash(), Address: peer.Address, Fingerprint: peer.Self().Fingerprint()}, Members: []protocol.ConvMember{{Person: old.roster.Person, Roster: old.roster.Hash()}, {Person: me.roster.Person, Roster: me.roster.Hash()}}, Nonce: protocol.NewID(), Created: time.Now().Unix(), Realm: realm, Title: "Recovered claim group", Admins: []string{me.roster.Person}}
	slices.SortFunc(root.Members, func(a, b protocol.ConvMember) int { return strings.Compare(a.Person, b.Person) })
	root.Sign(peer.id.Sign)
	state := protocol.GroupState{V: 1, Conv: root.ID(), Realm: realm, Title: root.Title}
	for _, m := range root.Members {
		by := peer
		if m.Person == old.roster.Person {
			by = oldDevice
		}
		ad, e := by.SignGroupAdmission(root, 0, "", nil)
		if e != nil {
			t.Fatal(e)
		}
		state.Members = append(state.Members, protocol.GroupMember{ConvMember: m, Admin: by == peer, Admission: ad})
	}
	packet, e := peer.SignGroupState(w.ctx, GroupContext{Root: root, State: state})
	if e != nil {
		t.Fatal(e)
	}
	commit, e := peer.BuildGroupCommit(w.ctx, packet)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = peer.PublishGroup(w.ctx, commit, packet); e != nil {
		t.Fatal(e)
	}
	eventually(t, "old group context", func() bool { _, e := oldDevice.GroupContext(root.ID()); return e == nil })
	stopOld()
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.roster.Email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.roster.Email}); e != nil {
		t.Fatal(e)
	}
	fresh := mel736OneJoin(t, w, "new")
	mel747FreezeOldOnRecovered(t, w, fresh, old.roster)
	return mel747Fixture{w: w, peer: peer, old: old.roster, root: root, fresh: fresh, lost: oldDevice}
}

// mel747GroupWorks checks both directions of group traffic for the
// recovered device after it healed its own old identity.
func mel747GroupWorks(t *testing.T, f mel747Fixture, recovered *Agent) {
	t.Helper()
	next, _, _ := recovered.Person()
	eventually(t, "recovered device heals its own old identity", func() bool {
		p, ok, e := recovered.store.personByID(f.old.Person)
		to, e2 := recovered.store.logicalPerson(f.old.Person)
		return e == nil && e2 == nil && ok && p.info.State == personPinned && to == next.Person
	})
	sent, e := f.peer.SendConv(f.w.ctx, f.root.ID(), ConvOutgoing{Body: "Group member continues"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "recovered group member receives", func() bool {
		ms, _ := recovered.ConversationMessages(f.root.ID())
		for _, m := range ms {
			if m.LID == sent.LID {
				return true
			}
		}
		return false
	})
	reply, e := recovered.SendConv(f.w.ctx, f.root.ID(), ConvOutgoing{Body: "Recovered member replies", ReplyTo: sent.LID})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "recovered author admitted", func() bool {
		ms, _ := f.peer.ConversationMessages(f.root.ID())
		for _, m := range ms {
			if m.LID == reply.LID {
				return true
			}
		}
		return false
	})
}

// MEL-747: a person recovered under an earlier client froze its own old
// identity (same email, every device revoked) as an email claim. The group
// still names the old identity, so every group carrier was refused. After
// upgrade the recovered device must heal on its own, with no new roster
// push and no clicks: the old identity becomes historical, the group
// arrives through continuity and both directions of group traffic work.
func TestMEL747RecoveredDeviceHealsOwnOldClaim(t *testing.T) {
	f := mel747Recovered(t)
	runAgent(t, f.fresh)
	waitNamedAgentCaps(t, f.fresh)
	next, _, _ := f.fresh.Person()
	if _, e := f.peer.refreshPerson(f.w.ctx, next.Person, false); e != nil {
		t.Fatal(e)
	}
	mel747GroupWorks(t, f, f.fresh)
}

// mel747HeldSubs opens what the device holds from sender, by exact id.
func mel747HeldSubs(t *testing.T, a *Agent, sender string) map[string]string {
	t.Helper()
	rows, e := a.store.db.Query(`SELECT id,envelope FROM quarantine WHERE sender=?`, sender)
	if e != nil {
		t.Fatal(e)
	}
	raws := map[string]string{}
	for rows.Next() {
		var id, raw string
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			t.Fatal(e)
		}
		raws[id] = raw
	}
	rows.Close()
	out := map[string]string{}
	for id, raw := range raws {
		var env envelope.Envelope
		if json.Unmarshal([]byte(raw), &env) != nil {
			continue
		}
		key, ok, e := pinnedKey(a.store.db, env.From)
		if e != nil || !ok {
			continue
		}
		if in, e := envelope.Open(env, a.id, a.Address, key); e == nil {
			out[id] = in.Sub
		}
	}
	return out
}

func mel747Reason(t *testing.T, a *Agent, id string) (reason, code string, archived bool) {
	t.Helper()
	if e := a.store.db.QueryRow(`SELECT reason,detail_code,notice_archived FROM quarantine WHERE id=?`, id).Scan(&reason, &code, &archived); e != nil {
		t.Fatal(id, e)
	}
	return
}

// The device ran v0.10.8 before this fix: the frozen record refused the
// continuity carriers and an admin's invitation for good (invalid,
// admission_failed). The upgrade looks at those inert carriers once more,
// leaves a notice its reader archived as it was, and the group arrives with
// no new copy from anyone.
func TestMEL747UpgradeRecoversCarriersRefusedEarlier(t *testing.T) {
	t.Run("invitation notice archived", func(t *testing.T) { mel747Upgrade(t, true) })
	t.Run("invitation notice open", func(t *testing.T) { mel747Upgrade(t, false) })
}

func mel747Upgrade(t *testing.T, archive bool) {
	f := mel747Recovered(t)
	// v0.10.8 refused what named the frozen record as for a conflicting
	// step: hold the freeze as one while this device collects those refusals.
	if _, e := f.fresh.store.db.Exec(`UPDATE persons SET conflict='mel747-fixture' WHERE person=?`, f.old.Person); e != nil {
		t.Fatal(e)
	}
	stopFresh := runAgent(t, f.fresh)
	waitNamedAgentCaps(t, f.fresh)
	next, _, _ := f.fresh.Person()
	if _, e := f.peer.refreshPerson(f.w.ctx, next.Person, false); e != nil {
		t.Fatal(e)
	}
	if _, e := f.peer.InviteGroup(f.w.ctx, f.root.ID(), next.Person, nil); e != nil {
		t.Fatal(e)
	}
	var held map[string]string
	eventually(t, "carriers and invitation refused for the frozen record", func() bool {
		held = mel747HeldSubs(t, f.fresh, f.peer.Address)
		subs := map[string]bool{}
		for id, sub := range held {
			var reason string
			f.fresh.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, id).Scan(&reason)
			subs[sub+"/"+reason] = true
		}
		return subs[envelope.SubGroupContext+"/"+reasonInvalid] && subs[envelope.SubGroupInvite+"/"+reasonInvalid]
	})
	stopFresh()
	if _, e := f.fresh.store.db.Exec(`UPDATE persons SET conflict=NULL WHERE person=?`, f.old.Person); e != nil {
		t.Fatal(e)
	}
	var invite string
	for id, sub := range held {
		if sub == envelope.SubGroupInvite {
			invite = id
			if !archive {
				continue
			}
			if _, e := f.fresh.store.db.Exec(`UPDATE quarantine SET notice_archived=1 WHERE id=?`, id); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e := f.fresh.store.db.Exec(`DELETE FROM config WHERE k=?`, frozenMemberRecoveryScan); e != nil {
		t.Fatal(e)
	}
	upgraded, e := Open(f.fresh.home)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { upgraded.Close() })
	for id, sub := range held {
		reason, _, archived := mel747Reason(t, upgraded, id)
		switch {
		case id == invite && archive:
			if reason != reasonInvalid || !archived {
				t.Fatal("archived notice changed", reason, archived)
			}
		case id == invite:
			if reason != reasonProof {
				t.Fatal("open invitation not reconsidered", reason)
			}
		case sub == envelope.SubGroupContext:
			if reason != reasonProof {
				t.Fatal("refused carrier not reconsidered", sub, reason)
			}
		}
	}
	if inboxCount(t, upgraded, `state='running'`) > 0 {
		t.Fatal("inert carrier recovery ran work")
	}
	runAgent(t, upgraded)
	mel747GroupWorks(t, f, upgraded)
	var invitations int
	count := func() int {
		if e := upgraded.store.db.QueryRow(`SELECT count(*) FROM group_invitations WHERE direction='in'`).Scan(&invitations); e != nil {
			t.Fatal(e)
		}
		return invitations
	}
	if archive {
		if reason, _, archived := mel747Reason(t, upgraded, invite); reason != reasonInvalid || !archived {
			t.Fatal("archived invitation resurfaced", reason, archived)
		}
		if count() != 0 {
			t.Fatal("archived invitation admitted", invitations)
		}
		return
	}
	// The open invitation is admitted in full as received: no longer held.
	eventually(t, "open invitation admitted", func() bool {
		var held int
		upgraded.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, invite).Scan(&held)
		return held == 0 && count() == 1
	})
	var state string
	if e := upgraded.store.db.QueryRow(`SELECT state FROM group_invitations WHERE direction='in'`).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state != "pending" {
		t.Fatal("open invitation not pending for its reader", state)
	}
}

// Only a claimant the relay shows left keeps nothing frozen: every exact
// device revoked, the pinned step still in its chain. Everything else stays
// frozen, a conflicting step for good; interrupted work converges; settled
// work does not spin.
func TestMEL747ClaimRecoveryFailsClosedAndSettles(t *testing.T) {
	f := mel747Recovered(t)
	ctx := f.w.ctx
	a := f.fresh
	// The relay decides left: a live device, another key at a revoked
	// address, an unknown address or no device keep the claimant.
	other, e := identity.Generate()
	if e != nil {
		t.Fatal(e)
	}
	for name, devices := range map[string][]identity.Public{
		"live":         {f.peer.Self()},
		"other key":    {other.Public(f.old.Devices[0].Address)},
		"unknown":      {other.Public("nobody/none")},
		"none":         nil,
		"one revoked+": {f.old.Devices[0], f.peer.Self()},
	} {
		r := f.old
		r.Devices = devices
		if left, e := a.claimantLeft(ctx, r); left {
			t.Fatal(name, "claimant counted as left", e)
		}
	}
	if left, e := a.claimantLeft(ctx, f.old); e != nil || !left {
		t.Fatal("revoked claimant not left", left, e)
	}
	// Not left, or left without its revocation known here, or looked at
	// from a stale freeze: frozen as it was, its chain kept.
	raws, e := a.fetchChain(ctx, f.old.Person, -1)
	if e != nil {
		t.Fatal(e)
	}
	chain := func() (n int) {
		t.Helper()
		if e := a.store.db.QueryRow(`SELECT count(*) FROM person_chain WHERE person=?`, f.old.Person).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	steps := chain()
	if steps == 0 {
		t.Fatal("claim kept no chain")
	}
	for name, try := range map[string]struct {
		claim string
		left  bool
	}{
		"not left":          {f.old.Hash(), false},
		"revocation unseen": {f.old.Hash(), true},
		"stale freeze":      {"stale", true},
	} {
		if _, e = a.store.pinLeftClaim(f.old.Person, raws, a.Self(), try.claim, try.left); !errors.Is(e, errPersonConflict) {
			t.Fatal(name, "pinned", e)
		}
		if claim, e := a.store.emailClaimOnly(f.old.Person); e != nil || !claim || chain() != steps {
			t.Fatal(name, "claim changed", claim, e)
		}
	}
	// A conflicting step stays frozen: no release, no claim error.
	if _, e = a.store.db.Exec(`UPDATE persons SET conflict='step' WHERE person=?`, f.old.Person); e != nil {
		t.Fatal(e)
	}
	if _, e = a.refreshPerson(ctx, f.old.Person, false); !errors.Is(e, errPersonConflict) || errors.Is(e, errPersonClaimFrozen) {
		t.Fatal("step conflict released or relabelled", e)
	}
	if reason, why := frozenRecordWaits(reasonInvalid, errPersonConflict.Error()); reason != reasonInvalid || why != errPersonConflict.Error() {
		t.Fatal("step conflict made to wait", reason, why)
	}
	if reason, _ := frozenRecordWaits(reasonInvalid, errPersonClaimFrozen.Error()); reason != reasonProof {
		t.Fatal("claim freeze refused for good", reason)
	}
	if _, e = a.store.db.Exec(`UPDATE persons SET conflict=NULL WHERE person=?`, f.old.Person); e != nil {
		t.Fatal(e)
	}
	next, _, _ := a.Person()
	healed := func(what string) {
		t.Helper()
		p, ok, e := a.store.personByID(f.old.Person)
		if e != nil || !ok || p.info.State != personPinned {
			t.Fatal(what, "not pinned", p.info, e)
		}
		if to, e := a.store.logicalPerson(f.old.Person); e != nil || to != next.Person {
			t.Fatal(what, "not one person", to, e)
		}
		if known, e := revokedDeviceKnown(a.store.db, f.old.Devices[0].Address, f.old.Devices[0].Fingerprint()); e != nil || !known {
			t.Fatal(what, "revocation not recorded", known, e)
		}
	}
	// Startup work alone heals, repeated work changes nothing more.
	for range 2 {
		if e = a.refreshContinuity(ctx); e != nil {
			t.Fatal(e)
		}
		healed("startup")
	}
	// Interrupted after the pin, before its checkpoint: repaired.
	if e = a.store.deleteConfig(continuityHeadPrefix + f.old.Person); e != nil {
		t.Fatal(e)
	}
	if e = a.refreshContinuity(ctx); e != nil {
		t.Fatal(e)
	}
	healed("checkpoint repair")
	// Never pinned here (or forgotten): the next lookup pins it as at
	// first sight, historical.
	for _, q := range []string{`DELETE FROM persons WHERE person = ?`, `DELETE FROM person_chain WHERE person = ?`, `DELETE FROM person_devices WHERE person = ?`} {
		if _, e = a.store.db.Exec(q, f.old.Person); e != nil {
			t.Fatal(e)
		}
	}
	if e = a.store.deleteConfig(continuityHeadPrefix + f.old.Person); e != nil {
		t.Fatal(e)
	}
	if _, e = a.refreshPerson(ctx, f.old.Person, false); e != nil {
		t.Fatal(e)
	}
	healed("released before pin")
	// Settled: continuity schedules nothing further on its own.
	for range 4 {
		a.convSync(ctx)
	}
	if a.store.continuityDirty.Load() || a.convWork.bits.Load()&(convPersons|convRecipients) != 0 {
		t.Fatalf("settled continuity spins: dirty=%v work=%d", a.store.continuityDirty.Load(), a.convWork.bits.Load())
	}
	// A different verified step at a pinned seq, served for a claim, freezes
	// it for good as a conflicting step: never looked at again, never
	// pinned, its carriers refused.
	if _, e = a.store.db.Exec(`UPDATE persons SET state=?, conflict=NULL WHERE person=?`, personConflict, f.old.Person); e != nil {
		t.Fatal(e)
	}
	forked := f.old
	forked.Label = "Forked"
	forked.Sign(f.lost.id.Sign)
	if e = forked.VerifyFirst(); e != nil {
		t.Fatal("fixture: fork does not verify", e)
	}
	fork, _ := json.Marshal(forked)
	if _, e = a.store.pinLeftClaim(f.old.Person, [][]byte{fork}, a.Self(), f.old.Hash(), true); !errors.Is(e, errPersonConflict) || errors.Is(e, errPersonClaimFrozen) {
		t.Fatal("fork not frozen for good", e)
	}
	if claim, e := a.store.emailClaimOnly(f.old.Person); e != nil || claim {
		t.Fatal("fork left as a claim", claim, e)
	}
	if _, e = a.refreshPerson(ctx, f.old.Person, false); !errors.Is(e, errPersonConflict) || errors.Is(e, errPersonClaimFrozen) {
		t.Fatal("fork looked at again", e)
	}
	if p, _, _ := a.store.personByID(f.old.Person); p.info.State != personConflict {
		t.Fatal("fork pinned", p.info)
	}
}
