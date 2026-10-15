package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Availability test: real encrypted inert children; no admission/model claim.
func TestTopicSnapshotReaderRequirementSurvivesArchiveRepack(t *testing.T) {
	_, a, phone, conv := archiveFixture(t, 1)
	_, root, _, err := a.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	me, _, e := a.store.selfPerson(a.Address)
	if e != nil {
		t.Fatal(e)
	}
	topic := protocol.NewID()
	ref := protocol.GrantRef{LID: protocol.NewID(), Fingerprint: a.Self().Fingerprint()}
	event := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(), Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Host: &protocol.ParticipationHost{Person: me.info.Person, Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Audience: protocol.AudienceConversation, Topic: &topic, Grant: []protocol.GrantRef{ref}, Snapshots: []protocol.GrantSnapshot{{LID: ref.LID, Fingerprint: ref.Fingerprint, Hash: strings.Repeat("a", 64)}}}
	event.Sign(a.id.Sign)
	body, _ := json.Marshal(event)
	snapshot := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), Conv: conv, From: a.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: event.PID, Body: string(body)}
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
	if err != nil || !needs.snapshotCap {
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
	if err != nil || !needs.snapshotCap || needs.required != protocol.CapTopicContext {
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
	for _, missing := range []string{protocol.CapTopicContext, protocol.CapHistoryArchive} {
		for session, record := range sessionRecords(t, phone) {
			var caps []string
			for _, cap := range record.Caps {
				if cap != missing && !(missing==protocol.CapTopicContext && cap==protocol.CapGuestAgent) {
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
