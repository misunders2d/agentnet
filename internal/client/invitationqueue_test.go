package client

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An older linked reader holds its invitation view independently of actual
// direct questions. Those questions still share their normal delivery FIFO.
func TestQueuedInvitationSyncDoesNotBlockDirectQuestions(t *testing.T) {
	w, group := groupLifecycleFixture(t, false)
	stop := runAgent(t, w.alice)
	phone := linked(t, w.alice)
	stop()
	signCapsAfter(t, phone, without(ownCaps, protocol.CapOwnSyncV2))
	inv := groupLifecycleInvite(t, w, group, nil)
	if _, err := w.alice.syncInvitations(); err != nil {
		t.Fatal(err)
	}
	var carrier string
	if err := w.alice.store.db.QueryRow(`SELECT id FROM outbox WHERE recipient=? AND sub='invitation-sync' AND coalesce(conv,'')=''`, phone.Address).Scan(&carrier); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if got := outboxState(t, w.alice, carrier); got != stateConvWaiting {
		t.Fatalf("old reader invitation state = %s", got)
	}
	posts := pausePosts(w.alice)
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
		_, err := w.alice.SendMessage(WithQueuedSend(tctx(t), id), Outgoing{
			To: phone.Address, Kind: envelope.KindQuestion, Body: body,
			Target: &envelope.Target{Address: phone.Address, Fingerprint: phone.Self().Fingerprint(), AgentID: protocol.NewID()},
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := send("first direct topic")
	select {
	case id := <-posts.entered:
		if id != first {
			t.Fatalf("first post = %s, want %s", id, first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting invitation view blocked a direct question")
	}
	second := send("second direct topic, different named agent")
	select {
	case id := <-posts.entered:
		t.Fatalf("question %s overtook the first question", id)
	case <-time.After(80 * time.Millisecond):
	}
	close(posts.release)
	eventually(t, "both exact questions received", func() bool {
		return inboxCount(t, phone, `id IN (?,?)`, first, second) == 2
	})
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	posts.mu.Lock()
	if len(posts.ids) != 2 || posts.ids[0] != first || posts.ids[1] != second {
		t.Errorf("question post order = %v", posts.ids)
	}
	posts.mu.Unlock()
	if got := outboxState(t, w.alice, carrier); got != stateConvWaiting {
		t.Fatalf("questions changed the invitation's capability gate: %s", got)
	}

	// A restart and signed capability upgrade retry the original stored carrier.
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	signCapsAfter(t, phone, ownCaps)
	features, err := again.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	again.releaseConv(tctx(t), features)
	if err := again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "same invitation view received after upgrade", func() bool {
		views, err := phone.GroupInvitations()
		if err != nil {
			return false
		}
		for _, view := range views {
			if view.ID == inv.ID && view.State == "pending" && view.Direction == "out" {
				return true
			}
		}
		return false
	})
	if _, err := again.syncInvitations(); err != nil {
		t.Fatal(err)
	}
	if err := again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if got := outboxState(t, again, carrier); got != protocol.StateCustody && got != protocol.StateDelivered {
		t.Fatalf("original invitation carrier was not handed over: %s", got)
	}
	if n := count(t, again, `outbox WHERE sub='invitation-sync'`); n != 1 {
		t.Fatalf("restart created %d invitation carriers", n)
	}
	if n := inboxCount(t, phone, `id IN (?,?)`, first, second); n != 2 {
		t.Fatalf("restart changed received question count: %d", n)
	}
}
