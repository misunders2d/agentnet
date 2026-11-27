package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Legacy agent history (no agent id) shares this device's host key whoever
// asked; "Delete chat" on one peer's row must not reach another peer's
// threads (review of a8855f88: bob/laptop and bob's phone each asked; one
// chat delete erased both).
func TestDeleteDeviceChatLegacyTouchesOnlyThatPeer(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.bob)
	ask, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "from the laptop?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "from the phone?"}); err != nil {
		t.Fatal(err)
	}
	legacy := func(peer string) int {
		ts, err := w.alice.Threads()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, s := range ts {
			if s.Peer == peer && s.AgentLegacy && s.AgentID == "" {
				n++
			}
		}
		return n
	}
	eventually(t, "a legacy agent thread from each peer", func() bool { return legacy(w.bob.Address) == 1 && legacy(phone.Address) == 1 })
	done, err := w.alice.DeleteDeviceChat(w.bob.Address, ask.ID)
	if err != nil || done.Erased != 1 {
		t.Fatalf("delete chat: %+v %v", done, err)
	}
	if legacy(w.bob.Address) != 0 || legacy(phone.Address) != 1 {
		t.Fatalf("after deleting bob/laptop's chat: laptop %d, phone %d", legacy(w.bob.Address), legacy(phone.Address))
	}
}
