package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestOwnResolutionReadOnlyCapabilityCheck(t *testing.T) {
	w := newWorld(t, "")
	if _, e := w.alice.CreatePerson(tctx(t), "Alice"); e != nil {
		t.Fatal(e)
	}
	runAgent(t, w.alice)
	phone := linkedVia(t, w.alice, "phone", func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) })
	if _, e := phone.sendKey(tctx(t), w.alice.Address); e != nil {
		t.Fatal(e)
	} // normal trusted host before opening its report
	id := protocol.NewID()
	check := func() error {
		return phone.CheckOwnResolution(tctx(t), w.alice.Address, id, phone.Self().Fingerprint(), 1)
	}
	if e := check(); e != nil {
		t.Fatal(e)
	}
	if e := phone.CheckOwnResolution(tctx(t), w.bob.Address, id, phone.Self().Fingerprint(), 1); e == nil {
		t.Fatal("another host offered as own")
	}
	if _, e := phone.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	if e := check(); e == nil {
		t.Fatal("pending host key accepted")
	}
	if _, e := phone.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	dropCapSuccessor(t, w.alice, protocol.CapOwnSyncV3)
	if e := check(); !errors.Is(e, ErrNoControls) || !strings.Contains(e.Error(), "own-device history and decisions") || strings.Contains(e.Error(), "reactions") {
		t.Fatalf("wrong capability explanation: %v", e)
	}
	// Passing a check is never permission to skip the real send's current check.
	if _, e := phone.Decide(WithQueuedSend(tctx(t), protocol.NewID()), w.alice.Address, id, phone.Self().Fingerprint(), "resolve", "needs_human", 1, "", ""); !errors.Is(e, ErrNoControls) || strings.Contains(e.Error(), "reactions") {
		t.Fatalf("send did not recheck exact host: %v", e)
	}
	var n int
	if e := phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub='decision'`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("check or refusal queued decision: %d %v", n, e)
	}
}
