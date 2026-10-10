package client

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryArchiveRosterLagRecoversWithoutReflood(t *testing.T) {
	_, source, phone, conv := archiveFixture(t, 3)
	for _, a := range []*Agent{source, phone} {
		a.stopBackgroundPosts()
		a.stopArchivePosts()
	}
	// A legitimate signed roster step arrives while the phone is offline.
	// Its device keys and grants do not change.
	me, err := source.RenamePerson(tctx(t), "New signed label")
	if err != nil {
		t.Fatal(err)
	}
	if bound, err := inChainIn(phone.store.db, me.Person, me.Roster); err != nil || bound {
		t.Fatalf("fixture is not behind the source roster: %v %v", bound, err)
	}
	if _, err := source.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if _, err := source.archiveStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := source.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	descriptor := archiveDescriptor(t, source)
	// Dispatch must retain the missing-proof record without fetching person
	// chains or blobs on the live stream. Recovery owns that network work.
	base := phone.hub.http.Transport
	requests := 0
	phone.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("unexpected network during descriptor admission: %s", r.URL.Path)
	})
	err = phone.storeReceived(tctx(t), descriptor)
	phone.hub.http.Transport = base
	if err != nil || requests != 0 {
		t.Fatalf("descriptor blocked the receive stream: requests=%d error=%v", requests, err)
	}
	var reason string
	if err := phone.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, descriptor.ID).Scan(&reason); err != nil || reason != reasonProof {
		t.Fatalf("recoverable roster lag became a permanent refusal: %q %v", reason, err)
	}
	if err := source.store.applyReceipt(protocol.ReceiptEvent{Seq: 1, ID: descriptor.ID, State: protocol.StateQuarantined}); err != nil {
		t.Fatal(err)
	}
	// The existing background proof recovery refreshes the signed own roster.
	phone.retryProof(tctx(t))
	var jobs int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM history_archive_jobs WHERE id=? AND retained=0`, descriptor.ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("proof recovery did not retain the original descriptor job: %d %v", jobs, err)
	}
	if state, err := phone.store.disposition(descriptor.ID); err != nil || state != protocol.StateQuarantined {
		t.Fatalf("descriptor acknowledged before ciphertext retention: %s %v", state, err)
	}
	if _, err := phone.archiveImportStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if state, err := phone.store.disposition(descriptor.ID); err != nil || state != protocol.StateDelivered {
		t.Fatalf("retained archive has no delivered disposition: %s %v", state, err)
	}
	var held int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, descriptor.ID).Scan(&held); err != nil || held != 0 {
		t.Fatalf("recovered archive left a stale held record: %d %v", held, err)
	}
	messages, err := phone.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var imported int
	for _, message := range messages {
		if message.Body == "row-000" || message.Body == "row-001" || message.Body == "row-002" {
			imported++
		}
	}
	if imported != 3 {
		t.Fatalf("recovery lost or duplicated history: %d", imported)
	}
	if err := source.store.applyReceipt(protocol.ReceiptEvent{Seq: 2, ID: descriptor.ID, State: protocol.StateDelivered}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := source.historyCatchupPage(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
		if err := source.archivePack(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	var descriptors, retained int
	if err := source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=?`, phone.Address, envelope.SubHistoryArchive).Scan(&descriptors); err != nil || descriptors != 1 {
		t.Fatalf("recovery re-sealed the same bootstrap: %d %v", descriptors, err)
	}
	if err := source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history' AND state=?`, phone.Address, archiveAccepted).Scan(&retained); err != nil || retained != 3 {
		t.Fatalf("source window did not settle its original children: %d %v", retained, err)
	}
}
