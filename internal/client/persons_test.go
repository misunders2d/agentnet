package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// forkedStep is owner's current roster step relabelled and signed again by
// owner's key at the same seq: a verified fork, as a Hub serving a
// different chain would show it.
func forkedStep(t *testing.T, owner *Agent, label string) (person string, raw []byte) {
	t.Helper()
	me, ok, err := owner.store.selfPerson(owner.Address)
	if err != nil || !ok {
		t.Fatalf("no person on %s: %v", owner.Address, err)
	}
	r := me.roster
	r.Label = label
	r.Sign(owner.id.Sign)
	raw, _ = json.Marshal(r)
	return me.info.Person, raw
}

// freeze shows at a fork of owner's person, which at has pinned: it
// freezes it there.
func freeze(t *testing.T, at, owner *Agent) {
	t.Helper()
	person, raw := forkedStep(t, owner, "someone else")
	if _, err := at.store.pinChain(person, [][]byte{raw}, at.Self(), false); !errors.Is(err, errPersonConflict) {
		t.Fatalf("freeze: %v", err)
	}
}

// linkPhone makes a device link code on at and joins a new device "phone"
// with it; the phone waits for approval (AwaitLink) in the background.
func linkPhone(t *testing.T, at *Agent, name string) (phone *Agent, awaited chan linkOutcome, code string) {
	t.Helper()
	o, err := at.NewDeviceLink(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	phone, err = JoinAndLink(tctx(t), t.TempDir(), o.Code, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	if s := phone.LinkState(); s.State != LinkPending || s.Approver != at.Address {
		t.Fatalf("link state %+v", s)
	}
	awaited = make(chan linkOutcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	go func() { s, err := phone.AwaitLink(ctx); awaited <- linkOutcome{s, err} }()
	return phone, awaited, o.Code
}

type linkOutcome struct {
	s   LinkStatus
	err error
}

func pendingLink(t *testing.T, at *Agent) LinkRequest {
	t.Helper()
	var got LinkRequest
	eventually(t, "the link request at "+at.Address, func() bool {
		links, _ := at.PendingLinks()
		for _, l := range links {
			if l.State == LinkPending {
				got = l
				return true
			}
		}
		return false
	})
	return got
}

// T1/T2: alice links her phone to her person from her laptop: one QR code,
// her approval there, then the same person on both devices; bob's DM
// reaches both, the phone answers in it, and the laptop shows that answer
// as hers.
func TestDeviceLinkJourney(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	phone, awaited, code := linkPhone(t, w.alice, "phone")
	req := pendingLink(t, w.alice)
	if req.Address != phone.Address || req.Fingerprint != phone.Self().Fingerprint() || req.Name != "phone" {
		t.Fatalf("request %+v", req)
	}
	// Pending: no member, nothing to send or read.
	if _, err := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "too early"}); err == nil {
		t.Fatal("a pending device sent a message")
	}
	if err := w.alice.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	var out linkOutcome
	select {
	case out = <-awaited:
	case <-time.After(30 * time.Second):
		t.Fatal("the phone never heard it was linked")
	}
	if out.err != nil || out.s.State != LinkLinked {
		t.Fatalf("phone: %+v %v", out.s, out.err)
	}
	me, _, _ := w.alice.Person()
	p, ok, err := phone.Person()
	if err != nil || !ok || p.Person != me.Person || len(p.Devices) != 2 || p.Address != phone.Address {
		t.Fatalf("phone person %+v %v", p, err)
	}
	if role, _ := phone.Role(); role != "person" {
		t.Fatalf("role %q", role)
	}
	// The code is used up.
	if _, err := JoinAndLink(tctx(t), t.TempDir(), code, "tablet"); err == nil {
		t.Fatal("a used code joined another device")
	}
	runAgent(t, phone)
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "to both of you"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob's message on the phone", func() bool { return strings.Join(convBodies(t, phone, conv), "|") == "in:to both of you" })
	eventually(t, "and on the laptop", func() bool { return strings.Join(convBodies(t, w.alice, conv), "|") == "in:to both of you" })
	sent, err := phone.SendConv(tctx(t), conv, ConvOutgoing{Body: "from my phone"})
	if err != nil || len(sent.Copies) != 2 {
		t.Fatalf("phone reply: %+v %v", sent, err)
	}
	eventually(t, "bob has the phone's answer once", func() bool {
		return strings.Join(convBodies(t, w.bob, conv), "|") == "out:to both of you|in:from my phone"
	})
	eventually(t, "the laptop shows it as alice's own", func() bool {
		msgs, _ := w.alice.ConversationMessages(conv)
		return len(msgs) == 2 && msgs[1].Dir == "out" && msgs[1].Via == phone.Address && msgs[1].Body == "from my phone"
	})
	// Removing the phone from the person revokes it (a link admitted it).
	if err := w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if me, _, _ := w.alice.Person(); len(me.Devices) != 1 || me.Seq != 2 {
		t.Fatalf("after removal: %+v", me)
	}
	if err := phone.hub.do(tctx(t), "GET", "/v1/agents", nil, nil); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the removed phone: %v", err)
	}
	if err := w.alice.RemoveDevice(tctx(t), w.alice.Address); err == nil {
		t.Fatal("the last device was removed")
	}
}

// A code whose secret is changed, a refusal, an expired code and a person
// whose devices changed meanwhile never link a device.
func TestDeviceLinkRefusals(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	// A damaged secret: the request does not match the offer and uses
	// nothing up.
	o, err := w.alice.NewDeviceLink(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := protocol.DecodeLinkOffer(o.Code)
	forged.Secret[0] ^= 1
	if _, err := JoinAndLink(tctx(t), t.TempDir(), forged.Encode(), "forged"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if links, _ := w.alice.PendingLinks(); len(links) != 0 {
		t.Fatalf("a forged request was taken: %+v", links)
	}
	// Refused: the device hears it.
	phone, awaited, _ := linkPhone(t, w.alice, "refused")
	req := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), req.ID, false); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; !errors.Is(out.err, ErrLinkRefused) || out.s.State != LinkRefused {
		t.Fatalf("refused phone: %+v %v", out.s, out.err)
	}
	if _, ok, _ := phone.Person(); ok {
		t.Fatal("a refused device has a person")
	}
	// Expired before the decision.
	_, _, _ = linkPhone(t, w.alice, "late")
	req = pendingLink(t, w.alice)
	w.alice.store.db.Exec(`UPDATE device_links SET expires = ? WHERE offer = ?`, time.Now().Unix()-1, req.ID)
	if err := w.alice.DecideLink(tctx(t), req.ID, true); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("an expired request: %v", err)
	}
	// The person's devices changed meanwhile: stale, never a competing step.
	_, _, _ = linkPhone(t, w.alice, "stale")
	req = pendingLink(t, w.alice)
	relabel(t, w.alice, "Alice again")
	if err := w.alice.DecideLink(tctx(t), req.ID, true); !errors.Is(err, ErrLinkStale) {
		t.Fatalf("a stale request: %v", err)
	}
	me, _, _ := w.alice.Person()
	if len(me.Devices) != 1 {
		t.Fatalf("devices %+v", me.Devices)
	}
}

// T4: a standalone service is enrolled without a person; it never becomes
// one by itself, and a DM with it is refused (it is reached as itself).
func TestServiceStaysStandalone(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice)
	if role, err := w.bob.Role(); role != "" || err != nil {
		t.Fatalf("role before a choice %q %v", role, err)
	}
	if err := w.bob.SetService(); err != nil {
		t.Fatal(err)
	}
	if role, err := w.bob.Role(); role != "service" || err != nil {
		t.Fatalf("role %q %v", role, err)
	}
	if _, err := w.bob.CreatePerson(tctx(t), "Bob"); !errors.Is(err, ErrService) {
		t.Fatalf("a service made a person: %v", err)
	}
	if _, err := w.alice.CreateDM(tctx(t), w.bob.Address); !errors.Is(err, ErrNoPerson) {
		t.Fatalf("a DM with a service: %v", err)
	}
	if err := w.alice.SetService(); err == nil {
		t.Fatal("a person's device became a service")
	}
}
