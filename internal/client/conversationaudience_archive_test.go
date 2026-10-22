package client

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestConversationAudienceLiveDelivery(t *testing.T) {
	w, host, conv, _, _, records, _ := externalAgentWorld(t)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, nil, nil, "live reader")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside invitation", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "current outside reader", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	stage := func(body string) (ConvSent, envelope.Envelope, string) {
		t.Helper()
		// Live delivery wakes the daemon to publish its actual readers. Keep
		// the simulated older reader dominant until resume signs its successor.
		for session, latest := range sessionRecords(t, host) {
			record := protocol.CapsRecord{Address: host.Address, Session: session, Caps: without(ownCaps, protocol.CapConversationAudience), TS: max(latest.TS+1, time.Now().Unix()+200)}
			record.Sign(host.id.Sign)
			if err := record.ValidateAdvertisement(); err != nil {
				t.Fatal(err)
			}
			if err := host.hub.do(tctx(t), "PUT", "/v1/caps", record, nil); err != nil {
				t.Fatal(err)
			}
		}
		eventually(t, "sender to observe the older live-audience reader", func() bool {
			return errors.Is(w.alice.requireParticipationCaps(tctx(t), host.Self(), protocol.CapConversationAudience), errAgentIdentityUnsupported)
		})
		sent, e := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: body})
		if e != nil {
			t.Fatal(e)
		}
		var id, state, required, raw string
		e = w.alice.store.db.QueryRow(`SELECT id,state,required_cap,envelope FROM outbox WHERE lid=? AND recipient=?`, sent.LID, host.Address).Scan(&id, &state, &required, &raw)
		if e != nil || state != stateConvWaiting || required != protocol.CapHumanParticipation+"+"+protocol.CapConversationAudience {
			t.Fatalf("unsupported live reader: %s %s %v", state, required, e)
		}
		var env envelope.Envelope
		if e = json.Unmarshal([]byte(raw), &env); e != nil || env.ID != id {
			t.Fatal("stored live envelope", e)
		}
		return sent, env, raw
	}
	resume := func(env envelope.Envelope) SendResult {
		t.Helper()
		addCapSuccessor(t, host, protocol.CapConversationAudience)
		eventually(t, "sender to observe signed live-audience reader support", func() bool {
			return w.alice.requireParticipationCaps(tctx(t), host.Self(), protocol.CapConversationAudience) == nil
		})
		features, e := w.alice.relayFeatures(tctx(t))
		if e != nil {
			t.Fatal(e)
		}
		w.alice.releaseConv(tctx(t), features)
		result, e := w.alice.deliver(tctx(t), env, nil)
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	sent, env, before := stage("CURRENT_LIVE_CONVERSATION_AUDIENCE")
	result := resume(env)
	if result.State != protocol.StateCustody && result.State != protocol.StateDelivered {
		t.Fatalf("current live scope did not deliver: %+v", result)
	}
	eventually(t, "exact live turn stored once", func() bool {
		_, count := convMsg(t, host, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
		return count == 1
	})
	var after string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&after); err != nil || after != before {
		t.Fatal("signed support changed the queued ciphertext", err)
	}
	blocked, blockedEnv, _ := stage("ENDED_LIVE_CONVERSATION_AUDIENCE")
	if _, err = w.alice.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	if result = resume(blockedEnv); result.State != stateNotDelivered {
		t.Fatalf("ended scope delivered after reader update: %+v", result)
	}
	_, count := convMsg(t, host, conv, func(m ConvMessage) bool { return m.LID == blocked.LID })
	if count != 0 {
		t.Fatal("ended helper received queued live context")
	}
}

// Availability test: real encrypted inert children; no admission/model claim.
func TestConversationAudienceReaderRequirementSurvivesArchiveRepack(t *testing.T) {
	_, a, phone, conv := archiveFixture(t, 1)
	_, root, _, err := a.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	me, _, e := a.store.selfPerson(a.Address)
	if e != nil {
		t.Fatal(e)
	}
	inv := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(), Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Host: &protocol.ParticipationHost{Person: me.info.Person, Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Audience: protocol.AudienceConversation}
	inv.Sign(a.id.Sign)
	scope := protocol.ScopeOf(inv, time.Now().Unix())
	scope.Sign(a.id.Sign)
	accept := protocol.ParticipationEvent{V: 1, Conv: conv, PID: inv.PID, Type: protocol.EventAccept, Prev: inv.Hash(), TS: time.Now().Unix(), Author: inv.Author}
	accept.Sign(a.id.Sign)
	human := &envelope.HumanTurn{Audience: []envelope.HumanScope{{PID: inv.PID, Invite: inv.Hash(), Decision: accept.Hash()}}, Proof: []protocol.ParticipationEvent{scope, accept}}
	if err := human.Validate(conv); err != nil {
		t.Fatal(err)
	}
	snapshot := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), Conv: conv, From: a.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "Captured live discussion", Human: human}
	var copies []outCopy
	for _, in := range []envelope.Inner{snapshot} {
		copy, e := a.historyCopy(phone.Self(), conv, root, itemOf(in, a.Self().Fingerprint(), time.Now().Unix()))
		if e != nil {
			t.Fatal(e)
		}
		copy.state = archiveStaged
		// The native catch-up staging path pins every child to this reader.
		copy.recipientFP = phone.Self().Fingerprint()
		copies = append(copies, copy)
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = insertCopies(tx, copies); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = a.archivePack(tctx(t)); err != nil {
		t.Fatal(err)
	}
	first := archiveDescriptor(t, a)
	needs, err := copyNeedsOf(a.store.db, first.ID, first.V)
	if err != nil || !needs.conversationAudienceCap {
		t.Fatalf("independent mixed archive flags: %+v %v", needs, err)
	}
	// Repack actual retained ciphertext after the descriptor expires and its
	// classification plaintext is erased. Every requirement must survive.
	if _, err = a.store.db.Exec(`UPDATE outbox SET state='queued' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.store.applyReceipt(protocol.ReceiptEvent{ID: first.ID, State: protocol.StateExpired, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	for _, copy := range copies {
		if _, err = a.store.db.Exec(`UPDATE outbox SET body='' WHERE id=?`, copy.env.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.archivePack(tctx(t)); err != nil {
		t.Fatal(err)
	}
	second := archiveDescriptor(t, a)
	if first.ID == second.ID {
		t.Fatal("expired archive was not repacked")
	}
	needs, err = copyNeedsOf(a.store.db, second.ID, second.V)
	if err != nil || !needs.conversationAudienceCap || needs.required != protocol.CapHistoryArchive {
		t.Fatal("repack lost a reader requirement", err)
	}
	// Upload/promote through the real archive path, then exercise deliver's
	// rootless sealed-descriptor gate, the v24 escape boundary.
	if _, err = a.archiveStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var sealedBefore string
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, second.ID).Scan(&sealedBefore); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{protocol.CapConversationAudience, protocol.CapHumanParticipation, protocol.CapHistoryArchive} {
		for session, record := range sessionRecords(t, phone) {
			var caps []string
			for _, cap := range record.Caps {
				if cap != missing && !(missing == protocol.CapHumanParticipation && cap == protocol.CapRoom) {
					caps = append(caps, cap)
				}
			}
			record.Caps = caps
			record.TS++
			record.Sign(phone.id.Sign)
			if err = phone.hub.do(tctx(t), "PUT", "/v1/caps", record, nil); err != nil {
				t.Fatal(session, err)
			}
		}
		err = a.readerCheck(tctx(t), phone.Address, phone.Self(), needs)
		var update *NeedsUpdateError
		if missing != protocol.CapHistoryArchive && (!errors.As(err, &update) || update.Cap != missing) || missing == protocol.CapHistoryArchive && err == nil {
			t.Fatalf("missing %s hidden by base archive requirement: %v", missing, err)
		}
		result, deliveryErr := a.deliver(tctx(t), second, nil)
		if deliveryErr != nil || result.State != stateConvWaiting {
			t.Fatalf("actual deliver missing %s: %+v %v", missing, result, deliveryErr)
		}
		addCapSuccessor(t, phone, missing)
	}
	if err = a.readerCheck(tctx(t), phone.Address, phone.Self(), needs); err != nil {
		t.Fatal(err)
	}
	features, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	a.releaseConv(tctx(t), features) // existing signed-capability wake resumes waiting copies
	result, err := a.deliver(tctx(t), second, nil)
	if err != nil || result.State != protocol.StateCustody {
		t.Fatalf("signed support did not release retained ciphertext: %+v %v", result, err)
	}
	var raw string
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, second.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != sealedBefore {
		t.Fatal("reader updates resealed the retained descriptor")
	}
	var env envelope.Envelope
	if json.Unmarshal([]byte(raw), &env) != nil || env.V != envelope.Version2 || len(env.CT) == 0 || env.To != phone.Address {
		t.Fatal("descriptor lost exact sealed envelope")
	}
}
