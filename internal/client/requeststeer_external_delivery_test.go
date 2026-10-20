package client

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Exercise a real outside-host DM participation without starting a harness.
// The producer retains the original and strict signed correction; this test
// isolates its eventual delivery from the independent current-attempt guard.
func TestExternalStrictSteeringDeliveryReaderAndRecipientGuards(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	stopSender := runAgent(t, a)
	runAgent(t, w.bob)
	persons(t, a, w.bob)
	conv := newDM(t, a, w.bob)
	host := mustJoin(t, filepath.Join(t.TempDir(), "outside"), w.aliceInvites("outside"), "host")
	stopHost := runAgent(t, host)
	persons(t, host)
	for _, peer := range []*Agent{a, w.bob, host} {
		fakeNotify(peer)
		waitNamedAgentCaps(t, peer)
	}
	record, err := host.CreateLocalAgent("Selected Pi", Responder{Harness: "pi", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := a.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "delivery fixture")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside host invitation", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside participation accepted", func() bool { return stateAt(t, a, p.PID).Claimable() })
	stopSender()
	a.stopBackgroundPosts() // this test owns every subsequent sender handover
	stopHost()              // no native executable or model receives the fixture

	session := protocol.NewID()
	caps := protocol.CapsRecord{Address: host.Address, Session: session, TS: time.Now().Unix(), Caps: []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapAgentIdentity, protocol.CapExternalParticipation, protocol.CapRequestFollowup}}
	slices.Sort(caps.Caps)
	caps.Sign(host.id.Sign)
	if err = caps.ValidateAdvertisement(); err != nil {
		t.Fatal(err)
	}
	rawCaps, _ := json.Marshal(caps)
	transport := &invitationCapsTransport{base: a.hub.http.Transport, profile: protocol.Profile{Live: true, Sessions: []string{session}, Caps: []json.RawMessage{rawCaps}}}
	a.hub.http.Transport = transport
	defer func() { a.hub.http.Transport = transport.base }()
	original, err := a.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), p.PID, envelope.KindQuestion, "Prepare the original comparison")
	if err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	ref := &envelope.FollowupRef{Ref: envelope.Ref{ID: original.LID, Fingerprint: a.Self().Fingerprint()}, Attempt: 1}
	target := &envelope.Target{Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: record.ID}
	if _, err = a.SendConv(WithQueuedSend(tctx(t), id), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "Compare the mobile clients too", ReplyTo: original.LID, PID: p.PID, Target: target, Followup: ref}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var required string
	if err = a.store.db.QueryRow(`SELECT envelope,required_cap FROM outbox WHERE lid=? AND recipient=?`, id, host.Address).Scan(&raw, &required); err != nil {
		t.Fatal(err)
	}
	if required != protocol.CapExternalParticipation+"+"+protocol.CapRequestSteer {
		t.Fatalf("strict external requirement lost: %q", required)
	}
	var env envelope.Envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, err := a.mayDeliverExternal(env); err != nil || !handled || !allowed {
		var state, detail string
		_ = a.store.db.QueryRow(`SELECT state,coalesce(error,'') FROM outbox WHERE id=?`, env.ID).Scan(&state, &detail)
		t.Fatalf("strict extension hid authorized external copy: %t %t %v state=%s detail=%s", handled, allowed, err, state, detail)
	}
	result, err := a.deliver(tctx(t), env, nil)
	if err != nil || result.State != stateConvWaiting || transport.posts != 0 {
		t.Fatalf("reader without flw2 received strict correction: %+v %v posts=%d", result, err, transport.posts)
	}
	features, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	caps.Caps = append(caps.Caps, protocol.CapRequestSteer)
	slices.Sort(caps.Caps)
	caps.Sign(w.bob.id.Sign) // another member's signature cannot upgrade this host
	rawCaps, _ = json.Marshal(caps)
	transport.profile.Caps = []json.RawMessage{rawCaps}
	a.releaseConv(tctx(t), features)
	result, err = a.deliver(tctx(t), env, nil)
	if err != nil || result.State != stateConvWaiting || transport.posts != 0 {
		t.Fatalf("wrong key granted strict reader support: %+v %v posts=%d", result, err, transport.posts)
	}
	caps.Sign(host.id.Sign)
	rawCaps, _ = json.Marshal(caps)
	transport.profile.Caps = []json.RawMessage{rawCaps}
	a.releaseConv(tctx(t), features)
	result, err = a.deliver(tctx(t), env, nil)
	if err != nil || result.State != protocol.StateCustody || transport.posts != 1 {
		t.Fatalf("exact signed reader did not release strict correction: %+v %v posts=%d", result, err, transport.posts)
	}
	var retained []byte
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&retained); err != nil || !bytes.Equal(raw, retained) {
		t.Fatal("reader upgrade rewrote signed correction", err)
	}
	inner, err := envelope.Open(env, host.id, host.Address, a.Self())
	if err != nil || inner.Followup == nil || *inner.Followup != *ref || inner.Target == nil || *inner.Target != *target {
		t.Fatalf("released correction changed exact request binding: %+v %v", inner, err)
	}
	// A retained copy addressed to neither original member nor selected host
	// still fails the final audience guard, even with the normalized extension.
	outsider := mustJoin(t, filepath.Join(t.TempDir(), "uninvited"), w.aliceInvites("uninvited"), "reader")
	inner.ID, inner.To = protocol.NewID(), outsider.Address
	recipient, err := outsider.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	uninvited, err := envelope.Seal(inner, a.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.addConvOutbox([]outCopy{{in: inner, env: uninvited, state: stateQueued, required: protocol.CapExternalParticipation, recipientFP: outsider.Self().Fingerprint()}}, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, err := a.mayDeliverExternal(uninvited); err != nil || !handled || allowed {
		t.Fatalf("strict extension widened external recipient authority: %t %t %v", handled, allowed, err)
	}
	if state, _, _, err := a.store.outboxState(uninvited.ID); err != nil || state != stateNotDelivered || transport.posts != 1 {
		t.Fatalf("unauthorized external copy escaped final refusal: %s %v posts=%d", state, err, transport.posts)
	}
}

func TestGroupStrictSteeringStillRequiresCapturedRecipientKey(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	a := w.alice
	p := p6Member(t, a, a, packet.State.Conv)
	stops[a]()
	a.stopBackgroundPosts()
	original, err := a.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), p.PID, envelope.KindQuestion, "Original group work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,last_attempt_at=unixepoch() WHERE id=?`, stateRunning, original.LID); err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	if _, err = a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: packet.State.Conv, Ref: envelope.Ref{ID: original.LID, Fingerprint: a.Self().Fingerprint()}, Attempt: 1, ID: id, Body: "Current attempt correction"}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? ORDER BY rowid LIMIT 1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []struct {
		name  string
		check func(envelope.Envelope) (bool, bool, error)
	}{{"group", a.mayDeliverGroupTurn}, {"participation", a.mayDeliverGroupParticipation}} {
		if _, err = a.store.db.Exec(`UPDATE outbox SET state=?,recipient_fp=? WHERE id=?`, stateQueued, a.Self().Fingerprint(), env.ID); err != nil {
			t.Fatal(err)
		}
		if handled, allowed, err := gate.check(env); err != nil || !handled || allowed {
			t.Fatalf("%s accepted wrong captured reader key: %t %t %v", gate.name, handled, allowed, err)
		}
		if state, _, _, err := a.store.outboxState(env.ID); err != nil || state != stateNotDelivered {
			t.Fatalf("%s wrong-key copy lacked durable refusal: %s %v", gate.name, state, err)
		}
	}
}
