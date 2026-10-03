package client

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func typingTargetAddresses(t *testing.T, a *Agent, scope protocol.TypingScope) []string {
	t.Helper()
	keys, err := a.typingTargets(scope)
	if errors.Is(err, ErrTypingScope) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range keys {
		out = append(out, k.Address)
	}
	return out
}

func typingFrom(t *testing.T, a *Agent, scope protocol.TypingScope, from string) bool {
	t.Helper()
	v, err := a.Typing(scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range v.Entries {
		if e.Address == from {
			return true
		}
	}
	return false
}

// Accepted human guests exchange typing with the original members and each
// other, by exact current host key only; pending, ended, linked or changed
// keys get none, an end removes a shown indicator, and nothing is stored.
func TestTypingHumanGuestsExactAcceptedHosts(t *testing.T) {
	w, carol, dana, conv, pc, pd, _, stub := twoGuests(t)
	scope := protocol.TypingScope{Conv: conv}
	all := []*Agent{w.alice, w.bob, carol, dana}
	for _, a := range all {
		eventually(t, "typing connection of "+a.Address, func() bool {
			a.typing.mu.Lock()
			defer a.typing.mu.Unlock()
			return a.typing.connected && a.typing.supported
		})
	}
	// Carol is still only invited: no typing either way.
	if got := typingTargetAddresses(t, carol, scope); got != nil {
		t.Fatalf("invited guest typing targets %v", got)
	}
	applyTyping(w.alice, typingWire(t, carol, w.alice, scope, true, time.Now().UnixMilli()))
	if typingFrom(t, w.alice, scope, carol.Address) {
		t.Fatal("invited guest typing shown")
	}
	if got := typingTargetAddresses(t, dana, scope); !slices.Contains(got, w.alice.Address) || !slices.Contains(got, w.bob.Address) || slices.Contains(got, carol.Address) {
		t.Fatalf("accepted guest targets %v", got)
	}
	if got := typingTargetAddresses(t, w.alice, scope); !slices.Contains(got, dana.Address) || slices.Contains(got, carol.Address) {
		t.Fatalf("original targets %v", got)
	}

	// Real fanout through the Hub: accepted guest to an original, and back.
	before := count(t, w.alice, "inbox") + count(t, w.alice, "outbox")
	if res, err := dana.SendTyping(tctx(t), scope, true); err != nil || res.Submitted == 0 {
		t.Fatalf("guest typing send %+v %v", res, err)
	}
	eventually(t, "original sees guest typing", func() bool { return typingFrom(t, w.alice, scope, dana.Address) })
	if res, err := w.bob.SendTyping(tctx(t), scope, true); err != nil || res.Submitted == 0 {
		t.Fatalf("original typing send %+v %v", res, err)
	}
	eventually(t, "guest sees original typing", func() bool { return typingFrom(t, dana, scope, w.bob.Address) })
	if count(t, w.alice, "inbox")+count(t, w.alice, "outbox") != before {
		t.Fatal("typing stored a message")
	}

	if _, err := carol.AcceptParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guests know each other", func() bool {
		return stateAt(t, dana, pc.PID).HumanActive() && stateAt(t, carol, pd.PID).HumanActive()
	})
	if got := typingTargetAddresses(t, carol, scope); !slices.Contains(got, dana.Address) || !slices.Contains(got, w.alice.Address) || !slices.Contains(got, w.bob.Address) {
		t.Fatalf("accepted guest targets %v", got)
	}
	if res, err := carol.SendTyping(tctx(t), scope, true); err != nil || res.Submitted == 0 {
		t.Fatalf("second guest typing send %+v %v", res, err)
	}
	eventually(t, "guest sees the other guest typing", func() bool { return typingFrom(t, dana, scope, carol.Address) })

	// Another device of Carol's person, and a changed key, get nothing.
	phone := humanLinked(t, carol)
	applyTyping(w.alice, typingWire(t, phone, w.alice, scope, true, time.Now().UnixMilli()))
	if typingFrom(t, w.alice, scope, phone.Address) || slices.Contains(typingTargetAddresses(t, w.alice, scope), phone.Address) {
		t.Fatal("guest's linked device shares typing")
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.store.setPending(id.Public(carol.Address)); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(typingTargetAddresses(t, w.bob, scope), carol.Address) || typingFrom(t, w.bob, scope, carol.Address) {
		t.Fatal("changed guest key kept typing")
	}
	if _, err = w.bob.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, carol.Address); err != nil {
		t.Fatal(err)
	}

	// The end removes Carol's shown indicator and fresh typing both ways.
	applyTyping(dana, typingWire(t, carol, dana, scope, true, time.Now().UnixMilli()+1))
	if !typingFrom(t, dana, scope, carol.Address) {
		t.Fatal("fixture: Carol not typing at Dana")
	}
	if _, err = w.alice.DismissParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "end applied at Dana and Carol", func() bool {
		return stateAt(t, dana, pc.PID).State == PartDismissed && stateAt(t, carol, pc.PID).State == PartDismissed
	})
	if typingFrom(t, dana, scope, carol.Address) {
		t.Fatal("ended guest indicator stayed")
	}
	applyTyping(dana, typingWire(t, carol, dana, scope, true, time.Now().UnixMilli()+2))
	if typingFrom(t, dana, scope, carol.Address) {
		t.Fatal("ended guest typing shown")
	}
	if got := typingTargetAddresses(t, carol, scope); got != nil {
		t.Fatalf("ended guest targets %v", got)
	}
	if slices.Contains(typingTargetAddresses(t, dana, scope), carol.Address) || slices.Contains(typingTargetAddresses(t, w.alice, scope), carol.Address) {
		t.Fatal("typing still targets ended guest")
	}
	if stub.runs() != 0 {
		t.Fatal("typing triggered a model")
	}
}
