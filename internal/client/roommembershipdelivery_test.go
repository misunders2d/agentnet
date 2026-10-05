package client

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestRoomDismissalReachesLateJoiner(t *testing.T) {
	w, carol, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	outside := proofReader(t, w, "late-end-outside")
	runAgent(t, outside)
	publishGroupFixtureCaps(t, outside, true)
	visitor := p6Member(t, w.alice, outside, conv)
	stops[w.alice]() // the relay cannot deliver the original end to the publisher yet
	if _, err := outside.DismissParticipation(tctx(t), visitor.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "existing members know host removal", func() bool {
		return stateAt(t, w.bob, visitor.PID).State == PartDismissed && stateAt(t, carol, visitor.PID).State == PartDismissed
	})
	stops[w.bob]()
	stops[carol]() // only the stale publisher will learn the late join during this probe
	dave := proofReader(t, w, "late-end-dave")
	runAgent(t, dave)
	publishGroupFixtureCaps(t, dave, true)
	host := protocol.ParticipationHost{Person: visitor.Host.Person, Address: visitor.Host.Address, Fingerprint: visitor.Host.Fingerprint, AgentID: visitor.AgentID}
	if _, err := dave.externalHostProof(tctx(t), &host); err != nil {
		t.Fatal(err)
	}
	if !stateAt(t, w.alice, visitor.PID).Following() {
		t.Fatal("offline publisher already knew the end")
	}
	next := groupInteractionRejoin(t, w.alice, dave, packet)
	// PublishGroup enqueues; with its daemon deliberately stopped, the
	// stale publisher needs this explicit delivery of its captured carrier.
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "late member receives stale accepted membership", func() bool {
		p, err := dave.Participation(visitor.PID)
		return err == nil && p.Following()
	})
	stop := runAgent(t, w.alice)
	eventually(t, "delayed original host end reaches publisher", func() bool { return stateAt(t, w.alice, visitor.PID).State == PartDismissed })
	eventually(t, "counted end forwarded to late member without new publication", func() bool { return stateAt(t, dave, visitor.PID).State == PartDismissed })
	stop()
	proof, err := roomMembershipProof(w.alice.store.db, next)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(proof, func(ev protocol.ParticipationEvent) bool {
		return ev.PID == visitor.PID && ev.Type == protocol.EventDismiss
	}) {
		t.Fatal("new carrier omitted the counted dismissal")
	}
	// Exact signed ends survive current-context import and reject a witness
	// attempting to elevate a non-admin author, wrong parent, or changed bytes.
	carried := next
	carried.Memberships = proof
	for _, mode := range []string{"host", "inviter", "non-admin", "wrong-parent", "bad-signature"} {
		t.Run(mode, func(t *testing.T) {
			candidate := carried
			candidate.Memberships = slices.Clone(proof)
			for i, ev := range candidate.Memberships {
				if ev.Type != protocol.EventDismiss {
					continue
				}
				signer := outside
				if mode == "inviter" || mode == "wrong-parent" {
					signer = w.alice
				}
				if mode == "non-admin" {
					signer = w.bob
				}
				p, _, _ := signer.store.selfPerson(signer.Address)
				ev.Author = protocol.EventAuthor{Person: p.info.Person, Roster: p.info.Roster, Address: signer.Address, Fingerprint: signer.Self().Fingerprint()}
				if member, ok := next.State.Member(p.info.Person); ok {
					ev.Author.GroupAdmission = member.Admission.Hash()
				}
				if mode == "wrong-parent" {
					ev.Prev = strings.Repeat("f", 64)
				}
				ev.Sign(signer.id.Sign)
				if mode == "bad-signature" {
					ev.Sig = slices.Clone(ev.Sig)
					ev.Sig[0] ^= 1
				}
				candidate.Memberships[i] = ev
			}
			tx, err := dave.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = admitRoomMembershipProof(tx, candidate)
			if want := mode == "host" || mode == "inviter"; (err == nil) != want {
				t.Fatalf("carried %s removal: %v", mode, err)
			}
		})
	}
	// Retrying the same evidence creates no extra recipient copies.
	var before, after int
	count := func(n *int) {
		t.Helper()
		if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=? AND recipient=?`, conv, visitor.PID, envelope.SubEvent, dave.Address).Scan(n); err != nil {
			t.Fatal(err)
		}
	}
	count(&before)
	features, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = w.alice.discloseRoomConv(tctx(t), conv, features); err != nil {
			t.Fatal(err)
		}
	}
	count(&after)
	if before != 1 || after != before {
		t.Fatalf("end copies before/after retry: %d/%d", before, after)
	}
	turn, err := dave.SendConv(tctx(t), conv, ConvOutgoing{Body: "AFTER_COUNTED_LATE_REMOVAL"})
	if err != nil {
		t.Fatal(err)
	}
	var countOutside int
	if err = dave.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=?`, turn.LID, outside.Address).Scan(&countOutside); err != nil || countOutside != 0 {
		t.Fatalf("removed host got future plaintext copy: %d %v", countOutside, err)
	}
	// A member joining after the publisher already knows the end imports it
	// in its first carrier, without depending on event fan-out or past bodies.
	erin := proofReader(t, w, "late-end-erin")
	runAgent(t, erin)
	publishGroupFixtureCaps(t, erin, true)
	groupInteractionRejoin(t, w.alice, erin, next)
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "new member imports already dismissed membership", func() bool {
		p, err := erin.Participation(visitor.PID)
		return err == nil && p.State == PartDismissed && !p.Following()
	})
}

func TestRoomDismissalFormerAdminDoesNotPoisonCarrier(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	outside := proofReader(t, w, "former-admin-outside")
	runAgent(t, outside)
	publishGroupFixtureCaps(t, outside, true)
	visitor := p6Member(t, w.alice, outside, conv)
	bob, _, _ := w.bob.store.selfPerson(w.bob.Address)
	promoted, err := w.alice.PromoteGroupMember(tctx(t), conv, bob.info.Person)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "second administrator current", func() bool { p, e := w.bob.GroupContext(conv); return e == nil && p.State.Seq == promoted.State.Seq })
	if _, err = w.bob.DismissParticipation(tctx(t), visitor.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "publisher counts another admin removal", func() bool { return stateAt(t, w.alice, visitor.PID).State == PartDismissed })
	demoted, err := w.alice.DemoteGroupMember(tctx(t), conv, bob.info.Person)
	if err != nil {
		t.Fatal(err)
	}
	declined, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member receives invitation to decline", func() bool { return stateAt(t, w.bob, declined.PID).State == PartInvited })
	if _, err = w.bob.DeclineParticipation(tctx(t), declined.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "publisher knows declined membership", func() bool { return stateAt(t, w.alice, declined.PID).State == PartDeclined })
	if _, err = w.alice.DismissParticipation(tctx(t), declined.PID); err != nil {
		t.Fatal(err)
	}
	proof, err := roomMembershipProof(w.alice.store.db, demoted)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof) != 0 {
		t.Fatal("carrier includes historical admin end, bare acceptance, or a never-accepted membership")
	}
	dave := proofReader(t, w, "former-admin-dave")
	runAgent(t, dave)
	publishGroupFixtureCaps(t, dave, true)
	next := groupInteractionRejoin(t, w.alice, dave, demoted)
	eventually(t, "valid new context survives former admin removal", func() bool { p, e := dave.GroupContext(conv); return e == nil && p.State.Seq == next.State.Seq })
	if _, err := dave.Participation(visitor.PID); !errors.Is(err, ErrNoParticipation) {
		t.Fatalf("new reader imported unverifiable membership: %v", err)
	}
	turn, err := dave.SendConv(tctx(t), conv, ConvOutgoing{Body: "AFTER_FORMER_ADMIN_END"})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := dave.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=?`, turn.LID, outside.Address).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fresh reader disclosed to formerly removed host: %d %v", n, err)
	}
}
