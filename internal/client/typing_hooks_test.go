package client

import (
	"testing"
	"time"
)

func TestTypingHooksCloseStopsExpiryTimer(t *testing.T) {
	w, scope := typingFixture(t)
	applyTyping(w.bob, typingWire(t, w.alice, w.bob, scope, true, time.Now().UnixMilli()))
	w.bob.typing.mu.Lock()
	armed := w.bob.typing.timer != nil
	w.bob.typing.mu.Unlock()
	if !armed {
		t.Fatal("fixture has no active expiry timer")
	}
	if err := w.bob.Close(); err != nil {
		t.Fatal(err)
	}
	w.bob.typing.mu.Lock()
	defer w.bob.typing.mu.Unlock()
	if w.bob.typing.connected || w.bob.typing.timer != nil {
		t.Fatal("Close kept typing connected or its expiry timer armed")
	}
}
