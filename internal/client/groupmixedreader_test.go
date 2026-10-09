package client

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupMixedReaderKeepsHumanMentionQueued(t *testing.T) {
	for _, missing := range []string{protocol.CapGroupHumanParticipation, protocol.CapGroup} {
		t.Run(missing, func(t *testing.T) { groupMixedReaderKeepsHumanMentionQueued(t, missing) })
	}
}

func groupMixedReaderKeepsHumanMentionQueued(t *testing.T, missing string) {
	w, carol, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	guest := proofReader(t, w, "mixed-reader-guest")
	runAgent(t, guest)
	publishGroupFixtureCaps(t, guest, true)
	for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
		roomReader(t, a)
	}
	signCapsAfter(t, carol, without(ownCaps, protocol.CapGroupHumanParticipation))
	plain, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "ordinary before guest"})
	if err != nil || plain.LID == "" {
		t.Fatalf("ordinary group: %+v %v", plain, err)
	}
	roomReader(t, carol)
	invite, err := w.alice.InviteHuman(tctx(t), conv, guest.Address, nil, "selected live context")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest invitation", func() bool { p, e := guest.Participation(invite.PID); return e == nil && p.State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), invite.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "accepted guest at sender", func() bool { p, e := w.alice.Participation(invite.PID); return e == nil && p.HumanActive() })
	stops[w.alice]() // inspect the durable copies before the forwarder races them
	person, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[@Bob](agentnet:person/%s) тут?", person.roster.Person)
	oldCaps := without(ownCaps, missing)
	if missing == protocol.CapGroup {
		oldCaps = withCap(without(oldCaps, protocol.CapRoom), protocol.CapHumanParticipation)
	}
	signCapsAfter(t, carol, oldCaps)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: body})
	if err != nil {
		t.Fatalf("one older reader blocked admitted members' mention: %v", err)
	}
	var id, raw, state, why string
	if err = w.alice.store.db.QueryRow(`SELECT id,envelope,state,coalesce(error,'') FROM outbox WHERE lid=? AND recipient=?`, sent.LID, carol.Address).Scan(&id, &raw, &state, &why); err != nil {
		t.Fatal(err)
	}
	if state != stateConvWaiting || !strings.Contains(why, carol.Address) {
		t.Fatalf("older recipient not individually waiting: %s %s", state, why)
	}
	var sealed envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &sealed); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.bob, guest} {
		eventually(t, "compatible audience receives exact mention", func() bool {
			n := 0
			for _, m := range groupTurns(t, a, conv) {
				if m.LID == sent.LID && m.Body == body {
					n++
				}
			}
			return n == 1
		})
	}
	for _, m := range groupTurns(t, carol, conv) {
		if m.LID == sent.LID {
			t.Fatal("unsupported recipient received context")
		}
	}
	// A fresh daemon releases the same persisted copy only after signed capability recovery.
	runAgent(t, w.alice)
	roomReader(t, carol)
	eventually(t, "updated recipient receives original once", func() bool {
		n := 0
		for _, m := range groupTurns(t, carol, conv) {
			if m.LID == sent.LID && m.Body == body {
				n++
			}
		}
		return n == 1
	})
	var after string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&after); err != nil || after != raw {
		t.Fatalf("recovery rewrote signed copy: %v", err)
	}
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.bob, carol, guest} {
		n := 0
		for _, m := range groupTurns(t, a, conv) {
			if m.LID == sent.LID {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("duplicate logical turn at %s: %d", a.Address, n)
		}
	}
}

// A reader's incompatible copy is independent of the selected executor.
func TestGroupMixedReaderDoesNotBlockSelectedExecutors(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(fmt.Sprint("local=", local), func(t *testing.T) {
			stub := installAgentStub(t)
			w, old, packet, _ := groupTurnsFixture(t)
			conv := packet.State.Conv
			for _, a := range []*Agent{w.alice, w.bob, old} {
				roomReader(t, a)
			}
			host := w.bob
			if local {
				host = w.alice
			}
			if err := host.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
				t.Fatal(err)
			}
			if err := host.Approve(w.alice.Address); err != nil {
				t.Fatal(err)
			}
			part := p6Member(t, w.alice, host, conv)
			signCapsAfter(t, old, without(ownCaps, protocol.CapGroupHumanParticipation))
			request, err := w.alice.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "selected executor stays independent")
			if err != nil {
				t.Fatalf("unrelated reader blocked selected target: %v", err)
			}
			reply := replyAt(t, w.alice, conv, request.LID)
			if !reply.VerifiedAgent || reply.PID != part.PID || reply.From != host.Address {
				t.Fatalf("wrong execution outcome: %+v", reply)
			}
			var id, raw, state, detail string
			if err = w.alice.store.db.QueryRow(`SELECT id,envelope,state,coalesce(error,'') FROM outbox WHERE lid=? AND recipient=?`, request.LID, old.Address).Scan(&id, &raw, &state, &detail); err != nil {
				t.Fatal(err)
			}
			if state != stateConvWaiting || !strings.Contains(detail, old.Address) {
				t.Fatalf("reader copy status confused with execution: %s %s", state, detail)
			}
			var attempts int
			if err = host.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, request.LID).Scan(&attempts); err != nil || attempts != 1 {
				t.Fatalf("exact target attempts %d: %v", attempts, err)
			}
			roomReader(t, old)
			eventually(t, "same reader copy delivered after capability recovery", func() bool {
				var s string
				_ = w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id=?`, id).Scan(&s)
				return s == protocol.StateDelivered
			})
			var after string
			if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&after); err != nil || after != raw {
				t.Fatal("reader recovery changed original envelope")
			}
			var replica int
			if err = old.store.db.QueryRow(`SELECT replica,attempts FROM inbox WHERE lid=? AND kind=?`, request.LID, envelope.KindQuestion).Scan(&replica, &attempts); err != nil || replica != 1 || attempts != 0 {
				t.Fatalf("reader copy gained execution: %d %d %v", replica, attempts, err)
			}
		})
	}
}
