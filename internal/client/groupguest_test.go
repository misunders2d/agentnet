package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
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

// Public APIs only: captured turns must keep their existing audience on edits.
func TestGroupGuestCapturedMessageControls(t *testing.T) {
	oldWrap := wrapTransport
	wrapTransport = func(base http.RoundTripper) http.RoundTripper {
		if oldWrap != nil {
			base = oldWrap(base)
		}
		return &humanEditOfflineTransport{base: base}
	}
	t.Cleanup(func() { wrapTransport = oldWrap })
	w, member, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	guest := proofReader(t, w, "cedar-guest")
	runAgent(t, guest)
	publishGroupFixtureCaps(t, guest, true)
	for _, a := range []*Agent{w.alice, w.bob, member, guest} {
		roomReader(t, a)
	}
	invite, e := member.InviteHuman(tctx(t), conv, guest.Address, nil, "")
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "guest invited", func() bool { p, e := guest.Participation(invite.PID); return e == nil && p.State == PartInvited })
	if _, e = guest.AcceptParticipation(tctx(t), invite.PID); e != nil {
		t.Fatal(e)
	}
	eventually(t, "guest active", func() bool { p, e := member.Participation(invite.PID); return e == nil && p.HumanActive() })
	sent, e := member.SendConv(tctx(t), conv, ConvOutgoing{Body: "Captured member turn"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "member turn received by guest", func() bool {
		for _, m := range groupTurns(t, guest, conv) {
			if m.LID == sent.LID {
				return true
			}
		}
		return false
	})
	ref := ControlRef{Conv: conv, ID: sent.LID, Fingerprint: member.Self().Fingerprint()}
	if _, e = member.Revise(tctx(t), ref, "Revised member turn"); e != nil {
		t.Fatal(e)
	}
	checkShown := func(reader *Agent, lid, text string, deleted bool) bool {
		eventually(t, "rendered captured control at "+reader.Address, func() bool {
			msgs, e := reader.ConversationMessages(conv)
			if e != nil {
				t.Fatal(e)
			}
			for _, m := range msgs {
				if m.LID == lid && ((deleted && m.Deleted) || (!deleted && m.Edited && m.Controls.Shown(m.Body) == text)) {
					return true
				}
			}
			return false
		})
		return true
	}

	if !checkShown(guest, sent.LID, "Revised member turn", false) {
		t.Error("public member Revise did not reach captured group guest")
	}
	if _, e = member.Retract(tctx(t), ref, ""); e != nil {
		t.Fatal(e)
	}
	if !checkShown(guest, sent.LID, "", true) {
		t.Error("public member Retract did not reach captured group guest")
	}
	own, e := guest.SendConv(tctx(t), conv, ConvOutgoing{PID: invite.PID, Body: "Guest owns this turn"})
	if e != nil {
		t.Fatal(e)
	}
	ownRef := ControlRef{Conv: conv, ID: own.LID, Fingerprint: guest.Self().Fingerprint()}
	for _, m := range groupTurns(t, guest, conv) {
		if m.LID == own.LID && (!slices.Contains(m.Can, CanEdit) || !slices.Contains(m.Can, CanDelete)) {
			t.Fatal("active guest own turn lacks truthful edit/delete actions")
		}
	}
	if _, e = guest.Revise(tctx(t), ownRef, "Guest revises its own turn"); e != nil {
		t.Errorf("public guest Revise refused own captured turn: %v", e)
	} else if !checkShown(member, own.LID, "Guest revises its own turn", false) {
		t.Error("guest revision did not render at current member")
	}
	if _, e = guest.Retract(tctx(t), ownRef, ""); e != nil {
		t.Errorf("public guest Retract refused own captured turn: %v", e)
	} else if !checkShown(member, own.LID, "", true) {
		t.Error("guest retraction did not render at current member")
	}
	// Later guests never become readers of controls about the earlier turn.
	anchored, e := member.SendConv(tctx(t), conv, ConvOutgoing{Body: "Earlier captured audience"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "earlier audience received", func() bool {
		for _, m := range groupTurns(t, guest, conv) {
			if m.LID == anchored.LID {
				return true
			}
		}
		return false
	})
	later := proofReader(t, w, "birch-guest")
	runAgent(t, later)
	publishGroupFixtureCaps(t, later, true)
	roomReader(t, later)
	next, e := member.InviteHuman(tctx(t), conv, later.Address, nil, "")
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "later guest invited", func() bool { p, e := later.Participation(next.PID); return e == nil && p.State == PartInvited })
	if _, e = later.AcceptParticipation(tctx(t), next.PID); e != nil {
		t.Fatal(e)
	}
	eventually(t, "later guest active", func() bool { p, e := member.Participation(next.PID); return e == nil && p.HumanActive() })
	eventually(t, "earlier guest verifies later participation", func() bool { p, e := guest.Participation(next.PID); return e == nil && p.HumanActive() })
	eventually(t, "earlier guest keeps verified group context", func() bool { _, e := guest.GroupContext(conv); return e == nil })
	anchoredRef := ControlRef{Conv: conv, ID: anchored.LID, Fingerprint: member.Self().Fingerprint()}
	if _, e = member.Revise(tctx(t), anchoredRef, "Only original captured guest"); e != nil {
		t.Fatal(e)
	}
	if !checkShown(guest, anchored.LID, "Only original captured guest", false) {
		t.Fatal("original guest did not receive revision")
	}
	var count int
	if e = member.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND ref_id=? AND recipient=?`, conv, anchored.LID, later.Address).Scan(&count); e != nil || count != 0 {
		t.Fatalf("later guest received earlier control: %d %v", count, e)
	}
	// Produce a real V3 control through Revise, hold its actual network POST,
	// then change the reader's signed sessions before retrying that same copy.
	transport := member.hub.http.Transport.(*humanEditOfflineTransport)
	transport.target.Store(guest.Address)
	transport.blocked.Store(true)
	if _, e = member.Revise(tctx(t), anchoredRef, "Queued captured revision"); e != nil {
		t.Fatal(e)
	}
	var queuedID, queuedRaw string
	if e = member.store.db.QueryRow(`SELECT id,envelope FROM outbox WHERE conv=? AND ref_id=? AND recipient=? AND sub=? ORDER BY rowid DESC LIMIT 1`, conv, anchored.LID, guest.Address, envelope.SubRevision).Scan(&queuedID, &queuedRaw); e != nil {
		t.Fatal(e)
	}
	oldCaps := slices.DeleteFunc(slices.Clone(ownCaps), func(c string) bool { return c == protocol.CapGroupHumanParticipation })
	signCapsAfter(t, guest, oldCaps)
	transport.blocked.Store(false)
	if e = member.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	var state, unchanged string
	if e = member.store.db.QueryRow(`SELECT state,envelope FROM outbox WHERE id=?`, queuedID).Scan(&state, &unchanged); e != nil || state != stateConvWaiting || unchanged != queuedRaw {
		t.Fatalf("V3 old reader crossed final gate: %s %v immutable=%t", state, e, unchanged == queuedRaw)
	}
	roomReader(t, guest)
	if e = member.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	if !checkShown(guest, anchored.LID, "Queued captured revision", false) {
		t.Fatal("same V3 copy did not resume after reader update")
	}
	// Ended guests stay out; ordinary current members can still revise.
	if _, e = member.DismissParticipation(tctx(t), invite.PID); e != nil {
		t.Fatal(e)
	}
	eventually(t, "captured guest ended", func() bool { p, e := guest.Participation(invite.PID); return e == nil && p.State == PartDismissed })
	if _, e = member.Revise(tctx(t), anchoredRef, "Current members only"); e != nil {
		t.Fatal(e)
	}
	var latestID string
	if e = member.store.db.QueryRow(`SELECT lid FROM outbox WHERE conv=? AND ref_id=? AND sub=? ORDER BY rowid DESC LIMIT 1`, conv, anchored.LID, envelope.SubRevision).Scan(&latestID); e != nil {
		t.Fatal(e)
	}
	if e = member.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND lid=? AND recipient IN (?,?)`, conv, latestID, guest.Address, later.Address).Scan(&count); e != nil || count != 0 {
		t.Fatalf("ended or later guest received member-only edit: %d %v", count, e)
	}
	if !checkShown(w.bob, anchored.LID, "Current members only", false) {
		t.Fatal("member-only revision no longer works")
	}

}

type humanEditOfflineTransport struct {
	base    http.RoundTripper
	target  atomic.Value
	blocked atomic.Bool
}

func (x *humanEditOfflineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var env envelope.Envelope
		target, _ := x.target.Load().(string)
		if json.Unmarshal(raw, &env) == nil && env.V == envelope.Version3 && env.To == target && x.blocked.Load() {
			return nil, errors.New("synthetic offline control delivery")
		}
	}
	return x.base.RoundTrip(r)
}
