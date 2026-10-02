package hub

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A session that ends while another keeps the device connected still moves
// the directory on: the device's capabilities are the intersection of its
// live sessions (profile.go), so senders waiting on the ended one's smaller
// list must look again.
func TestSessionEndWhileConnectedNotifies(t *testing.T) {
	changed := make(chan string, 8)
	p := presence{grace: 30 * time.Millisecond, onEnd: func(string, string) {}, onChange: func(agent string) { changed <- agent }}
	old, cur := protocol.NewID(), protocol.NewID()
	p.connect("bob/x", protocol.SessionAd{Session: old})
	<-changed // offline -> connected
	p.connect("bob/x", protocol.SessionAd{Session: cur})
	p.disconnect("bob/x", old)
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("the old session's end did not move the directory on")
	}
	if p.live("bob/x", old) || !p.live("bob/x", cur) || p.state("bob/x") != protocol.PresenceConnected {
		t.Fatal("wrong session ended, or the device is no longer connected")
	}
}
