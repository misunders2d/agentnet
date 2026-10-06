package client

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Public guest operations preserve separate current membership and exact
// device consent. Carol is an ordinary member, not an administrator.
func TestGroupGuestPublicJourney(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	guest := proofReader(t, w, "guest")
	runAgent(t, guest)
	publishGroupFixtureCaps(t, guest, true)
	for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
		roomReader(t, a)
	}
	// rm1 in v0.8.1 does not promise public group guest lifecycle support.
	oldCaps := slices.DeleteFunc(slices.Clone(ownCaps), func(c string) bool { return c == protocol.CapGroupHumanParticipation })
	for _, legacy := range []*Agent{guest, w.bob} {
		signCapsAfter(t, legacy, oldCaps)
		support, err := carol.HumanInviteSupport(tctx(t), conv, guest.Address)
		if err != nil {
			t.Fatal(err)
		}
		update := false
		for _, s := range support {
			update = update || s.State == "update"
		}
		if !update {
			t.Fatal("legacy rm1 peer was marked group-guest ready")
		}
		if _, err := carol.InviteHuman(tctx(t), conv, guest.Address, nil, "legacy denied"); err == nil {
			t.Fatal("legacy group guest invitation succeeded")
		}
		roomReader(t, legacy)
	}
	old, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "selected earlier context"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "private earlier context"}); err != nil {
		t.Fatal(err)
	}
	if _, err = carol.HumanInviteSupport(tctx(t), conv, guest.Address); err != nil {
		t.Fatal(err)
	}
	p, err := carol.InviteHuman(tctx(t), conv, guest.Address, []string{old.LID}, "help without membership")
	if err != nil {
		t.Fatal(err)
	}
	if p.Audience != protocol.AudienceRoom || p.Member {
		t.Fatalf("guest acquired membership: %+v", p)
	}
	eventually(t, "public guest invitation", func() bool { p, e := guest.Participation(p.PID); return e == nil && p.State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "public guest accepted at inviter", func() bool { x, e := carol.Participation(p.PID); return e == nil && x.HumanActive() })
	eventually(t, "selected context only", func() bool {
		rows, e := guest.ConversationMessages(conv)
		if e != nil {
			return false
		}
		found := false
		for _, m := range rows {
			if m.Body == "private earlier context" {
				t.Fatal("unselected history disclosed")
			}
			found = found || m.Body == "selected earlier context"
		}
		return found
	})
	if _, err = carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "new member turn"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest receives live member turn", func() bool {
		for _, m := range groupTurns(t, guest, conv) {
			if m.Body == "new member turn" {
				return true
			}
		}
		return false
	})
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Body: "new guest turn"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "members receive guest turn", func() bool {
		for _, m := range groupTurns(t, w.bob, conv) {
			if m.Body == "new guest turn" {
				return true
			}
		}
		return false
	})
	other := proofReader(t, w, "otherguest")
	runAgent(t, other)
	publishGroupFixtureCaps(t, other, true)
	roomReader(t, other)
	second, err := w.bob.InviteHuman(tctx(t), conv, other.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "other guest invitation", func() bool { x, e := other.Participation(second.PID); return e == nil && x.State == PartInvited })
	if _, err = other.AcceptParticipation(tctx(t), second.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "other guest receives public scope", func() bool { x, e := other.Participation(p.PID); return e == nil && x.HumanActive() })
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{Body: "no guest scope"}); err == nil {
		t.Fatal("guest sent without exact scope")
	}
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Kind: envelope.KindTask, Body: "execute"}); err == nil {
		t.Fatal("human guest gained executor")
	}
	originalKey, _, _, err := carol.store.peer(guest.Address)
	if err != nil {
		t.Fatal(err)
	}
	changedIdentity, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = carol.store.pin(changedIdentity.Public(guest.Address)); err != nil {
		t.Fatal(err)
	}
	changed, sendErr := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "changed guest key denied"})
	if sendErr == nil {
		for _, copy := range changed.Copies {
			if copy.To == guest.Address {
				t.Fatal("changed guest key received disclosure")
			}
		}
	}
	if err = carol.store.pin(originalKey); err != nil {
		t.Fatal(err)
	}
	var capturedID, capturedRaw string
	if err = carol.store.db.QueryRow(`SELECT id,envelope FROM outbox WHERE conv=? AND recipient=? AND human IS NOT NULL LIMIT 1`, conv, guest.Address).Scan(&capturedID, &capturedRaw); err != nil {
		t.Fatal(err)
	}
	var captured envelope.Envelope
	if err = json.Unmarshal([]byte(capturedRaw), &captured); err != nil {
		t.Fatal(err)
	}
	// A late legacy session holds the exact signed end until its reader updates.
	signCapsAfter(t, guest, oldCaps)
	if _, err = w.bob.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	var lateID, lateRaw string
	if err = w.bob.store.db.QueryRow(`SELECT id,envelope FROM outbox WHERE recipient=? AND pid=? AND sub='event' AND json_extract(body,'$.type')='dismiss'`, guest.Address, p.PID).Scan(&lateID, &lateRaw); err != nil {
		t.Fatal(err)
	}
	if state, _, _, e := w.bob.store.outboxState(lateID); e != nil || state != stateConvWaiting {
		t.Fatalf("late legacy guest end must wait: %q %v", state, e)
	}
	if x, e := guest.Participation(p.PID); e != nil || x.State != PartActive {
		t.Fatalf("unsupported guest received end: %+v %v", x, e)
	}
	roomReader(t, guest)
	features, e := w.bob.relayFeatures(tctx(t))
	if e != nil {
		t.Fatal(e)
	}
	w.bob.releaseConv(tctx(t), features)
	if err = w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var restoredRaw string
	if err = w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, lateID).Scan(&restoredRaw); err != nil {
		t.Fatal(err)
	}
	if restoredRaw != lateRaw {
		t.Fatal("reader update replaced immutable end copy")
	}
	eventually(t, "guest ended", func() bool { x, e := guest.Participation(p.PID); return e == nil && x.State == PartDismissed })
	eventually(t, "all members apply guest dismissal", func() bool { x, e := carol.Participation(p.PID); return e == nil && x.State == PartDismissed })
	if _, err = carol.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, capturedID); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, e := carol.mayDeliverExternal(captured); e != nil || !handled || allowed {
		t.Fatalf("offline captured guest copy escaped dismissal: %v %v %v", handled, allowed, e)
	}
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{PID: p.PID, Body: "after end"}); err == nil {
		t.Fatal("ended guest sent")
	}
	left, err := w.bob.InviteHuman(tctx(t), conv, guest.Address, nil, "guest may leave")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "second guest invitation", func() bool { x, e := guest.Participation(left.PID); return e == nil && x.State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), left.PID); err != nil {
		t.Fatal(err)
	}
	if _, err = guest.DismissParticipation(tctx(t), left.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest leave reaches member", func() bool { x, e := w.bob.Participation(left.PID); return e == nil && x.State == PartDismissed })
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{PID: left.PID, Body: "after leave"}); err == nil {
		t.Fatal("departed guest sent")
	}

}
