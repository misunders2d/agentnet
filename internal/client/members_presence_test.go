package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A member list that changes only presence still looks again at held
// messages, but does not sweep deferred own-device history again: a device
// flapping between connected and offline made each push re-check every
// group context (about a second of CPU per push on a large store). A list
// that changes anything else still sweeps.
func TestPresenceOnlyMembersKeepHistoryDeferral(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	list := func(bob string) []byte {
		raw, _ := json.Marshal(protocol.Members{Members: []protocol.Member{
			{Address: a.Address, Presence: protocol.PresenceConnected, Joined: 5},
			{Address: w.bob.Address, Presence: bob, Joined: 5},
		}})
		return raw
	}
	a.onMembers(list(protocol.PresenceConnected))
	a.convWork.take()
	deferred := func() bool {
		a.convWork.mu.Lock()
		defer a.convWork.mu.Unlock()
		_, ok := a.convWork.historyDeferred["sentinel"]
		return ok
	}
	a.convWork.mu.Lock()
	a.convWork.historyDeferred = map[string]historyDeferredScan{"sentinel": {done: true}}
	a.convWork.mu.Unlock()

	a.onMembers(list(protocol.PresenceOffline))
	if bits := a.convWork.bits.Load(); bits&convRetry != 0 || bits&convRetryHeld == 0 {
		t.Fatalf("presence-only list: work %b, want the held look without a history sweep", bits)
	}
	a.convSync(tctx(t))
	if !deferred() {
		t.Fatal("presence-only list swept deferred own-device history again")
	}

	raw, _ := json.Marshal(protocol.Members{Members: []protocol.Member{
		{Address: a.Address, Presence: protocol.PresenceConnected, Joined: 5},
		{Address: w.bob.Address, Presence: protocol.PresenceOffline, Joined: 6},
	}})
	a.onMembers(raw)
	if bits := a.convWork.bits.Load(); bits&convRetry == 0 {
		t.Fatalf("changed list: work %b, want the full look again", bits)
	}
	a.convSync(tctx(t))
	if deferred() {
		t.Fatal("changed list kept the old history deferral")
	}
}
