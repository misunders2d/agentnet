package ui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestLiveGroupManageRefusesUnrelatedAuthorityBeforeClient(t *testing.T) {
	l := &Live{timeout: time.Second}
	for _, change := range []GroupChange{{Action: "approve", Conv: "room"}, {Action: "rename", Person: "other", Title: "title"}, {Action: "promote", Title: "injected", Person: "exact"}, {Action: "leave", Person: "other"}, {Action: "remove"}} {
		if _, err := l.ManageGroup(context.Background(), change); err == nil {
			t.Fatalf("unrelated group authority accepted: %+v", change)
		}
	}
}

// Two exact recipient devices must converge through SendDM's existing bounded
// receipt waits. The test never refreshes a conversation or queries Status.
func TestLiveConversationAllAudienceReceiptsConvergeWithoutRefresh(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	if _, err = alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err = bob.CreatePerson(ctx, "Bob"); err != nil {
		t.Fatal(err)
	}
	offer, err := bob.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := client.JoinAndLink(ctx, t.TempDir(), offer.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	linked := make(chan error, 1)
	go func() { _, e := phone.AwaitLink(ctx); linked <- e }()
	wait := func(label string, fn func() bool) {
		t.Helper()
		for end := time.Now().Add(15 * time.Second); !fn(); time.Sleep(25 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("timed out: " + label)
			}
		}
	}
	var request string
	wait("pending device link", func() bool {
		rows, e := bob.PendingLinks()
		if e != nil || len(rows) == 0 {
			return false
		}
		request = rows[0].ID
		return true
	})
	if err = bob.DecideLink(ctx, request, true); err != nil {
		t.Fatal(err)
	}
	if err = <-linked; err != nil {
		t.Fatal(err)
	}
	runDaemon(t, phone)
	l := NewLive(alice)
	var conv string
	wait("recipient capabilities", func() bool {
		id, e := l.NewDM(bob.Address)
		if e != nil {
			return false
		}
		conv = id
		return true
	})
	if _, err = l.SendDM(DMDraft{Conv: conv, Body: "all exact audience copies"}); err != nil {
		t.Fatal(err)
	}
	wait("both audience copies automatically delivered", func() bool {
		msgs, e := alice.ConversationMessages(conv)
		if e != nil || len(msgs) != 1 || len(msgs[0].Copies) != 2 {
			return false
		}
		for _, copy := range msgs[0].Copies {
			if copy.State != protocol.StateDelivered {
				return false
			}
		}
		return true
	})
}

// Opening a DM asks about every copy the Hub still holds. A copy to the
// sender's own other device that arrives after the send's bounded receipt
// wait was shown as held forever, because only the first copy (the other
// person's, already delivered) was asked about.
func TestLiveRefreshAsksAboutEveryHeldCopy(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	if _, err = alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err = bob.CreatePerson(ctx, "Bob"); err != nil {
		t.Fatal(err)
	}
	wait := func(label string, fn func() bool) {
		t.Helper()
		for end := time.Now().Add(20 * time.Second); !fn(); time.Sleep(25 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("timed out: " + label)
			}
		}
	}
	offer, err := alice.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := client.JoinAndLink(ctx, t.TempDir(), offer.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	linked := make(chan error, 1)
	go func() { _, e := phone.AwaitLink(ctx); linked <- e }()
	var request string
	wait("pending device link", func() bool {
		rows, e := alice.PendingLinks()
		if e != nil || len(rows) == 0 {
			return false
		}
		request = rows[0].ID
		return true
	})
	if err = alice.DecideLink(ctx, request, true); err != nil {
		t.Fatal(err)
	}
	if err = <-linked; err != nil {
		t.Fatal(err)
	}
	l := NewLive(alice)
	var conv string
	wait("recipient capabilities", func() bool {
		id, e := l.NewDM(bob.Address)
		if e != nil {
			return false
		}
		conv = id
		return true
	})
	// The phone is offline while the message is sent and while the send's
	// own receipt waits run.
	if _, err = l.SendDM(DMDraft{Conv: conv, Body: "reaches the phone later"}); err != nil {
		t.Fatal(err)
	}
	copies := func() []client.ConvCopy {
		msgs, e := alice.ConversationMessages(conv)
		if e != nil || len(msgs) != 1 {
			return nil
		}
		return msgs[0].Copies
	}
	stateOf := func(to string) string {
		for _, c := range copies() {
			if c.To == to {
				return c.State
			}
		}
		return ""
	}
	wait("the other person's copy delivered", func() bool { return stateOf(bob.Address) == protocol.StateDelivered })
	time.Sleep(receiptWait + time.Second) // the send's own bounded receipt waits are over
	// The phone's copy is held by the Hub, or kept here until the phone's
	// capabilities are known (it is released once the phone connects).
	if got := stateOf(phone.Address); got != protocol.StateCustody && got != "waiting" {
		t.Fatalf("phone copy before it connected = %q", got)
	}
	runDaemon(t, phone)
	wait("the phone stored its copy", func() bool {
		msgs, e := phone.ConversationMessages(conv)
		return e == nil && len(msgs) == 1
	})
	wait("the copy shown as held", func() bool { return stateOf(phone.Address) == protocol.StateCustody })
	if _, err = l.Refresh(conv); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(phone.Address); got != protocol.StateDelivered {
		t.Fatalf("phone copy after opening the DM = %q, want delivered", got)
	}
	msgs, err := alice.ConversationMessages(conv)
	if err != nil || len(msgs) != 1 || msgs[0].State != protocol.StateDelivered {
		t.Fatalf("message state after refresh = %+v, %v", msgs, err)
	}
}
