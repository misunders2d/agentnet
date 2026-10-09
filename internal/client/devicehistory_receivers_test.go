package client

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Native reply routing is private setup, even though its encrypted envelope
// uses the ordinary message kind. Mirroring it as a visible original can
// collide with the host's real, already-stored setup request.
func TestDeviceHistorySkipsPrivateReceiverSetup(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	host := ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}
	if _, err := phone.ReplyReceiverCatalog(tctx(t), host); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := phone.store.db.QueryRow(`SELECT id FROM outbox WHERE required_cap=? AND recipient=?`, protocol.CapReplyReceiver, host.Address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.deviceHistorySource(phone.store.db, "out", id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private receiver catalog became visible history: %v", err)
	}
	for range 3 {
		if _, err := phone.deviceHistoryPage(w.alice.Self()); err != nil {
			t.Fatal(err)
		}
	}
	var copies int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM device_history_copies WHERE id=?`, id).Scan(&copies); err != nil || copies != 0 {
		t.Fatalf("private setup history copies: %d %v", copies, err)
	}
	// A real visible direct request remains eligible for own-device history.
	sent, err := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "visible direct request"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phone.deviceHistorySource(phone.store.db, "out", sent.ID); err != nil {
		t.Fatalf("visible direct request was suppressed: %v", err)
	}
}

func TestDeviceHistoryKeepsVisibleReceiverRequest(t *testing.T) {
	w, phone, _, _ := remoteReceiverWorld(t)
	sent, setup := remotePrepared(t, w, phone, envelope.KindQuestion)
	r, err := phone.deviceHistorySource(phone.store.db, "out", sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.item.ID != sent.ID || r.item.ReceiverRoute == nil || r.item.ReceiverRoute.Op != "request" || r.item.ReceiverRoute.DelegationID != setup {
		t.Fatal("visible request lost its exact receiver provenance")
	}
	if _, err = phone.deviceHistorySource(phone.store.db, "out", setup); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private delegation became visible history: %v", err)
	}
	// A delegation for a human-scoped original uses hgp1 while the setup
	// itself still has no visible human tuple (prepareReceiverRemote).
	if _, err = phone.store.db.Exec(`UPDATE outbox SET required_cap=? WHERE id=?`, protocol.CapHumanParticipation, setup); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.deviceHistorySource(phone.store.db, "out", setup); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private human delegation became visible history: %v", err)
	}
}

func TestQueuedPrivateHumanReceiverSetupDoesNotBlockDirectQuestions(t *testing.T) {
	w, phone, stopPhone, _ := remoteReceiverWorld(t)
	_, setup := remotePrepared(t, w, phone, envelope.KindQuestion)
	stopPhone()
	if err := phone.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	// Retain the real private delegation in the capability-waiting state
	// used for an older host and a human-scoped original. Its setup has no
	// Human tuple or visible reply choice of its own.
	if _, err := phone.store.db.Exec(`UPDATE outbox SET required_cap=?,state=? WHERE id=?`, protocol.CapHumanParticipation, stateConvWaiting, setup); err != nil {
		t.Fatal(err)
	}
	posts := pausePosts(phone)
	t.Cleanup(func() {
		select {
		case <-posts.release:
		default:
			close(posts.release)
		}
	})
	send := func(body string) string {
		t.Helper()
		id := protocol.NewID()
		_, err := phone.SendMessage(WithQueuedSend(tctx(t), id), Outgoing{
			To: w.alice.Address, Kind: envelope.KindQuestion, Body: body,
			Target: &envelope.Target{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint(), AgentID: protocol.NewID()},
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := send("first readable question after private setup")
	select {
	case id := <-posts.entered:
		if id != first {
			t.Fatalf("first post = %s, want %s", id, first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting private human receiver setup blocked a direct question")
	}
	second := send("second readable question, different named agent")
	select {
	case id := <-posts.entered:
		t.Fatalf("question %s overtook the first question", id)
	case <-time.After(80 * time.Millisecond):
	}
	close(posts.release)
	eventually(t, "both exact questions received", func() bool {
		return inboxCount(t, w.alice, `id IN (?,?)`, first, second) == 2
	})
	if err := phone.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	posts.mu.Lock()
	if len(posts.ids) != 2 || posts.ids[0] != first || posts.ids[1] != second {
		t.Errorf("question post order = %v", posts.ids)
	}
	posts.mu.Unlock()
	if got := outboxState(t, phone, setup); got != stateConvWaiting {
		t.Fatalf("questions changed private setup gate: %s", got)
	}
	if _, err := phone.deviceHistorySource(phone.store.db, "out", setup); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private human setup became visible history: %v", err)
	}
	if _, err := phone.deviceHistorySource(phone.store.db, "out", first); err != nil {
		t.Fatalf("readable direct question lost history eligibility: %v", err)
	}
	for range 3 {
		if _, err := phone.deviceHistoryPage(w.alice.Self()); err != nil {
			t.Fatal(err)
		}
	}
	var copies int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM device_history_copies WHERE id=?`, setup).Scan(&copies); err != nil || copies != 0 {
		t.Fatalf("private human setup history copies: %d %v", copies, err)
	}
}
