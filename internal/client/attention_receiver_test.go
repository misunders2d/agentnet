package client

import (
	"fmt"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestAttentionSelectedInputHasOneReceiver(t *testing.T) {
	w := newWorld(t, "")
	owner, _ := nativeReceiverFixture(t, w.alice, "pi")
	handles := []string{owner.Handle, "other-native-handle", ""}
	for i, h := range handles {
		at, e := w.alice.Attention(HookEvent{Harness: "codex", Session: fmt.Sprintf("attention-%d", i), Event: "SessionStart", ReplySession: h})
		if e != nil {
			t.Fatal(e)
		}
		if e = at.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	binding, input := nativeInput(t, w.alice, w.bob, owner.Handle)
	for _, kind := range []string{"live_session", "managed_agent", "human"} {
		if kind != "live_session" {
			if _, e := w.alice.store.db.Exec(`UPDATE reply_receivers SET receiver=? WHERE id=?`, fmt.Sprintf(`{"kind":%q}`, kind), binding); e != nil {
				t.Fatal(e)
			}
		}
		for i, h := range handles {
			items, e := w.alice.store.arrivalsAfterReceiver(0, 100, h)
			if e != nil || len(items) != 0 {
				t.Fatalf("%s handle%q arrival %+v %v", kind, h, items, e)
			}
			n, e := w.alice.store.countArrivalsAfterReceiver(0, h)
			if e != nil || n != 0 {
				t.Fatalf("%s handle%q count%d %v", kind, h, n, e)
			}
			at, e := w.alice.Attention(HookEvent{Harness: "codex", Session: fmt.Sprintf("attention-%d", i), Event: "Stop", ReplySession: h})
			if e != nil || at.Text != "" {
				t.Fatalf("%s handle%q generic wake %q %v", kind, h, at.Text, e)
			}
		}
	}
	msg, e := w.alice.store.inboxMessage(input)
	if e != nil || msg == nil || msg.Body != "correlated clarification data" {
		t.Fatalf("explicit inbox read %+v %v", msg, e)
	}
	conv, e := w.alice.Conversation(input, 0, 0)
	if e != nil || conv.Total < 2 {
		t.Fatalf("explicit conversation read %+v %v", conv, e)
	}
	unbound := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Body: "ordinary unbound arrival"})
	if e = w.alice.verifyAndStore(tctx(t), unbound); e != nil {
		t.Fatal(e)
	}
	for _, h := range handles {
		items, e := w.alice.store.arrivalsAfterReceiver(0, 100, h)
		if e != nil || len(items) != 1 || items[0].id != unbound.ID {
			t.Fatalf("unbound hidden %+v %v", items, e)
		}
		n, e := w.alice.store.countArrivalsAfterReceiver(0, h)
		if e != nil || n != 1 {
			t.Fatalf("unbound count%d %v", n, e)
		}
	}
}
