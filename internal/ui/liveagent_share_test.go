package ui

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

// Owner, 2026-11-11: bringing an agent in must be able to share your own
// agents' and your other devices' messages, which this device holds as
// copies. The page offers them (room_shareable_test.go); the host takes them.
func TestShareableMessageIncludesOwnCopies(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    client.ConvMessage
		want bool
	}{
		{"own message", client.ConvMessage{}, true},
		{"own agent's reply held as a copy", client.ConvMessage{Replica: true, VerifiedAgent: true}, true},
		{"own other device's message", client.ConvMessage{Replica: true}, true},
		{"excerpt shared with another agent", client.ConvMessage{Replica: true, ExcerptPID: "p"}, false},
		{"participation event", client.ConvMessage{Sub: "event"}, false},
		{"deleted", client.ConvMessage{Controls: client.Controls{Deleted: true}}, false},
	} {
		if got := shareableMessage(tc.m); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
