package ui

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// BUG-25: opening a conversation asks about every copy the Hub may still
// hold, also of a message shown by a less advanced copy (one that failed,
// or is kept here); before, only messages shown as held were looked at, so
// the other person's copy stayed "held" for good.
func TestLiveRefreshAsksAboutHeldCopiesBehindOthers(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	home := t.TempDir()
	alice, err := client.Join(ctx, home, testhub.BootstrapCode(t, dir), "laptop")
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
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(label string, fn func() bool) {
		t.Helper()
		for end := time.Now().Add(20 * time.Second); !fn(); time.Sleep(25 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("timed out: " + label)
			}
		}
	}
	l := NewLive(alice)
	var conv string
	wait("a DM", func() bool { id, e := l.NewDM(bob.Address); conv = id; return e == nil })
	sent, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "behind a failed copy"})
	if err != nil {
		t.Fatal(err)
	}
	wait("bob to store it", func() bool { msgs, e := bob.ConversationMessages(conv); return e == nil && len(msgs) == 1 })
	// A second copy that failed (as one to a device the Hub refused): the
	// message is shown by it, not by bob's copy, still recorded as held.
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, error, created_at, conv, lid, kind, created_ms)
		VALUES(?, 'admin/gone', 'behind a failed copy', '{}', 'failed', 'hub: recipient has been revoked (403)', ?, ?, ?, 'message', ?)`,
		strings.Repeat("f", 32), time.Now().Unix(), conv, sent.LID, time.Now().UnixMilli()+1); err != nil {
		t.Fatal(err)
	}
	stateOf := func(to string) string {
		msgs, e := alice.ConversationMessages(conv)
		if e != nil || len(msgs) != 1 {
			return ""
		}
		for _, c := range msgs[0].Copies {
			if c.To == to {
				return c.State
			}
		}
		return ""
	}
	wait("bob receipt pushed", func() bool { return stateOf(bob.Address) == protocol.StateDelivered })
	// Simulate the durable custody row left by an older relay, behind a failed copy.
	if _, err = db.Exec("UPDATE outbox SET state='custody' WHERE conv=? AND recipient=?", conv, bob.Address); err != nil {
		t.Fatal(err)
	}
	wait("bob's copy refreshed", func() bool {
		if _, e := l.Refresh(conv); e != nil {
			t.Fatal(e)
		}
		return stateOf(bob.Address) == protocol.StateDelivered
	})
}
