package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An agent's chat holds every peer's topics with it (topicAgentKey: host,
// key and agent id, no peer). Deleting it erases all of them, and its note
// names nobody: with several peers' copies it says only that others keep
// theirs (review of 189a5371). A notice-only thread is no chat row to start
// from (as the engine).
func TestDeleteDeviceChatAgentOfSeveralPeers(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	agent := protocol.NewID()
	ask := func(from *Agent, body string) string {
		t.Helper()
		in := envelope.Inner{ID: protocol.NewID(), From: from.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: body,
			Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: agent}}
		if err := w.bob.store.addInbox(in, from.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	q1, q2 := ask(w.alice, "from the laptop"), ask(phone, "from the phone")
	if a, b := threadOf(t, w.bob, q1), threadOf(t, w.bob, q2); a.Peer == b.Peer || topicAgentKey(a) != topicAgentKey(b) {
		t.Fatalf("one agent chat of two peers: %+v %+v", a, b)
	}
	done, err := w.bob.DeleteDeviceChat(w.alice.Address, q1)
	if err != nil || done.Erased != 2 || done.Peers != 2 {
		t.Fatalf("delete the agent's chat: %+v %v", done, err)
	}
	note := w.bob.DeviceChatNote(done, w.alice.Address)
	if !strings.HasSuffix(note, " Others keep their copies.") || strings.Contains(note, "keeps their copy") || strings.Contains(note, "keeps its copy") {
		t.Fatalf("several peers' chat named one keeper: %q", note)
	}
	if ts, err := w.bob.Threads(); err != nil || len(ts) != 0 {
		t.Fatalf("left after the chat delete: %+v %v", ts, err)
	}
	// One peer's chat names that peer's keeper, never "others".
	one := w.bob.DeviceChatNote(ConversationDeleted{Peers: 1}, w.alice.Address)
	if strings.Contains(one, "Others") || !strings.HasSuffix(one, " copy.") {
		t.Fatalf("one peer's note: %q", one)
	}
	if _, err := w.bob.DeleteDeviceChat(w.alice.Address, "nothing-here"); err != ErrNoMessage {
		t.Fatalf("an unknown thread: %v", err)
	}
}
