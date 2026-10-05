package client

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func p6SecurityContextTo(t *testing.T, from, to *Agent, packet GroupContext) string {
	t.Helper()
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload := groupDeliveryPayload{envelope.SubGroupContext, protocol.GroupCarrier{V: 1, Seq: packet.State.Seq, Hash: packet.State.Hash()}, raw}
	copies, err := from.groupDeliveryTo(tctx(t), packet, []groupDeliveryPayload{payload}, to.Self())
	if err != nil {
		t.Fatal(err)
	}
	if err = from.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = from.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	id := copies[0].env.ID
	eventually(t, "signed membership carrier stored", func() bool { return inboxHas(t, to, id) })
	return id
}

func TestP6SecurityBackdatedOutsideInvite(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	outside := proofReader(t, w, "backdate-outside")
	runAgent(t, outside)
	publishGroupFixtureCaps(t, outside, true)
	visitor := p6Member(t, w.alice, outside, conv)
	events, err := w.alice.store.participationEvents(conv, visitor.PID)
	if err != nil {
		t.Fatal(err)
	}
	var invite, accept protocol.ParticipationEvent
	for _, ev := range events {
		if ev.Type == protocol.EventInvite {
			invite = ev
		}
		if ev.Type == protocol.EventAccept {
			accept = ev
		}
	}
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	old, err := groupProofRecord(carol.store.db, conv, packet.Root.Creator.Fingerprint, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(old.Admins, bob.info.Person) {
		t.Fatal("fixture Bob was not original admin")
	}
	current, _ := packet.State.Member(bob.info.Person)
	if current.Admin {
		t.Fatal("fixture Bob was not demoted")
	}
	invite.PID = protocol.NewID()
	invite.Author = protocol.EventAuthor{Person: bob.info.Person, Roster: bob.info.Roster, Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), GroupAdmission: current.Admission.Hash()}
	invite.Group = &protocol.ParticipationGroup{Seq: 0, Hash: old.Hash, HostRole: "visitor"}
	invite.Sign(w.bob.id.Sign)
	scope := protocol.ScopeOf(invite, invite.TS)
	scope.Sign(w.bob.id.Sign)
	accept.PID, accept.Prev = invite.PID, invite.Hash()
	accept.Sign(outside.id.Sign)
	for _, reader := range []*Agent{w.alice, carol} {
		m, e := reader.dmMembers(conv)
		if e != nil {
			t.Fatal(e)
		}
		for _, ev := range []protocol.ParticipationEvent{invite, scope} {
			if ok, e := m.verifyInviteEpoch(reader.store.db, ev); e != nil || ok {
				t.Fatalf("backdated %s counted at %s: %v %v", ev.Type, reader.Address, ok, e)
			}
		}
	}
	// Even a correctly vouched carrier cannot turn an old admin slot into authority.
	carried := packet
	carried.Memberships = []protocol.ParticipationEvent{invite, scope, accept}
	tx, err := carol.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = admitRoomMembershipProof(tx, carried); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("backdated carrier admitted: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = carol.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE pid=?`, invite.PID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rejected carrier installed events")
	}
}

func TestP6SecurityStaleMembershipCarrier(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	outside := proofReader(t, w, "stale-outside")
	runAgent(t, outside)
	publishGroupFixtureCaps(t, outside, true)
	visitor := p6Member(t, w.alice, outside, conv)
	stale, err := roomMembershipProof(w.alice.store.db, packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) < 3 {
		t.Fatal("missing original signed membership")
	}
	if _, err = w.alice.DismissParticipation(tctx(t), visitor.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Bob knows outside removal", func() bool { return stateAt(t, w.bob, visitor.PID).State == PartDismissed })
	dave := proofReader(t, w, "stale-dave")
	runAgent(t, dave)
	publishGroupFixtureCaps(t, dave, true)
	next := groupInteractionRejoin(t, w.alice, dave, packet)
	eventually(t, "Dave current state", func() bool { p, e := dave.GroupContext(conv); return e == nil && p.State.Seq == next.State.Seq })
	eventually(t, "Bob current state", func() bool { p, e := w.bob.GroupContext(conv); return e == nil && p.State.Seq == next.State.Seq })
	host := protocol.ParticipationHost{Person: visitor.Host.Person, Address: visitor.Host.Address, Fingerprint: visitor.Host.Fingerprint, AgentID: visitor.AgentID}
	if _, err = dave.externalHostProof(tctx(t), &host); err != nil {
		t.Fatal(err)
	}
	forged := next
	forged.Memberships = stale
	p6SecurityContextTo(t, w.bob, dave, forged)
	if p, e := dave.Participation(visitor.PID); !errors.Is(e, ErrNoParticipation) {
		t.Fatalf("non-admin stale carrier revived membership: %+v %v", p, e)
	}
	targets, err := dave.groupVisitorTargets(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("removed host remains a disclosure target: %d", len(targets))
	}
	turn, err := dave.SendConv(tctx(t), conv, ConvOutgoing{Body: "AFTER_REMOVED_HOST"})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = dave.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=?`, turn.LID, outside.Address).Scan(&count); err != nil || count != 0 {
		t.Fatalf("plaintext sealed to removed host: %d %v", count, err)
	}
	// The original publisher and own linked devices remain valid witnesses;
	// a locally pinned withdrawal must invalidate that witness in the same tx.
	for _, test := range []struct {
		sender *Agent
		want   bool
	}{{w.alice, true}, {w.bob, false}, {outside, false}, {dave, true}} {
		got, e := roomMembershipSender(dave.store.db, next, test.sender.Self())
		if e != nil || got != test.want {
			t.Fatalf("witness %s: %v %v", test.sender.Address, got, e)
		}
	}
	me, _, _ := dave.store.selfPerson(dave.Address)
	member, _ := next.State.Member(me.info.Person)
	withdrawal := protocol.GroupWithdrawal{Conv: conv, Realm: next.Root.Realm, Person: me.info.Person, Admission: member.Admission.Hash(), Roster: me.info.Roster, By: dave.Self().Fingerprint()}
	withdrawal.Sign(dave.id.Sign)
	tx, err := dave.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = pinGroupWithdrawal(tx, withdrawal); err != nil {
		t.Fatal(err)
	}
	if allowed, e := roomMembershipSender(tx, next, dave.Self()); e != nil || allowed {
		t.Fatalf("withdrawn own witness counted: %v %v", allowed, e)
	}
}

func TestP6SecurityWithdrawalStripsPrivateMemberships(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	outside := proofReader(t, w, "withdrawal-outside")
	runAgent(t, outside)
	publishGroupFixtureCaps(t, outside, true)
	visitor, err := w.alice.InviteAgent(tctx(t), conv, outside.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "pending outside scope at Bob", func() bool { p, e := w.bob.Participation(visitor.PID); return e == nil && p.State == PartInvited })
	private, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, []string{w.alice.Self().Fingerprint()}, "PRIVATE_OTHER_AGENT_NOTE")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "private invitation", func() bool { return stateAt(t, w.bob, private.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), private.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "private member active", func() bool { return stateAt(t, w.alice, private.PID).Claimable() })
	packet.Memberships, err = roomMembershipProof(w.alice.store.db, packet)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(packet.Memberships)
	if !strings.Contains(string(raw), "PRIVATE_OTHER_AGENT_NOTE") {
		t.Fatal("fixture lacks private invitation")
	}
	// A newly installed snapshot retains its publisher's membership payload;
	// an equal-state replay deliberately leaves the previous payload unchanged.
	next, err := w.alice.RenameGroup(tctx(t), conv, "Private memberships before departure")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member stores private membership payload", func() bool {
		stored, e := w.bob.GroupContext(conv)
		return e == nil && stored.State.Seq == next.State.Seq && len(stored.Memberships) != 0
	})
	if _, err = w.bob.SignGroupWithdrawal(tctx(t), conv); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var id string
	if err = w.bob.store.db.QueryRow(`SELECT id FROM outbox WHERE conv=? AND recipient=? AND pid=? AND sub=? ORDER BY rowid DESC LIMIT 1`, conv, outside.Address, visitor.PID, envelope.SubGroupContext).Scan(&id); err != nil {
		t.Fatal(err)
	}
	env := groupTurnEnvelope(t, w.bob, id)
	in, err := envelope.Open(env, outside.id, outside.Address, w.bob.Self())
	if err != nil {
		t.Fatal(err)
	}
	data, err := outside.groupCarrierBytes(tctx(t), in.Attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	var got GroupContext
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Memberships) != 0 || strings.Contains(string(data), "PRIVATE_OTHER_AGENT_NOTE") {
		t.Fatal("departure discloses another agent's private invite")
	}
	if len(got.Withdrawals) == 0 {
		t.Fatal("departure context omitted its signed withdrawal")
	}
}
