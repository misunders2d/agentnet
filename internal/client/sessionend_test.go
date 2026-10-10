package client

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// A copy waits for a capability the recipient's running program lacks (as
// one a suspended device's session never published). The recipient
// updates: its new session publishes the capability while the old one
// lingers in its grace period (every live session counts). When the old
// session ends, the Hub sends the member list again though it reads the
// same, so the sender looks again on its own and the copy goes, with no
// other membership change in the workspace.
func TestWaitingCopyReleasedWhenOldSessionEnds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	const grace = 3 * time.Second
	w := &world{hub: testhub.StartConfig(t, hub.Config{DataDir: dir, SessionGrace: grace}, "127.0.0.1:0")}
	w.alice = mustJoin(t, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "alice")
	code, err := w.alice.Invite(tctx(t), "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	w.bob = mustJoin(t, filepath.Join(t.TempDir(), "bob"), code, "laptop")
	stopBob := runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	runAgent(t, w.alice)
	conv := newDM(t, w.alice, w.bob)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "to be deleted"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob has it", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapControl)) // an older program
	if _, err := w.alice.Retract(tctx(t), ControlRef{Conv: conv, ID: sent.LID, Fingerprint: w.alice.Self().Fingerprint()}, ""); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := w.alice.store.db.QueryRow(`SELECT id FROM outbox WHERE sub=? AND recipient=?`, envelope.SubRetraction, w.bob.Address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if st := outboxState(t, w.alice, id); st != stateConvWaiting {
		t.Fatalf("retraction to bob: %s, want waiting", st)
	}
	label, name, _ := protocol.SplitAddress(w.bob.Address)
	profile := func() protocol.Profile {
		var p protocol.Profile
		if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := profile().Sessions
	if len(old) != 1 {
		t.Fatalf("bob's sessions before the update: %v", old)
	}
	stopBob()
	runAgent(t, w.bob) // it updates
	eventually(t, "bob's new session publishes control", func() bool {
		for _, raw := range profile().Caps {
			if r, err := protocol.ParseCapsRecord(raw); err == nil && r.Session != old[0] && r.Reads(protocol.CapControl) {
				return true
			}
		}
		return false
	})
	eventually(t, "bob's old session ends", func() bool {
		p := profile()
		return len(p.Sessions) == 1 && p.Sessions[0] != old[0] && p.Supports(w.bob.Address, w.bob.Self().SignKey, protocol.CapControl)
	})
	ended := time.Now()
	for outboxState(t, w.alice, id) == stateConvWaiting && time.Since(ended) < 10*time.Second {
		time.Sleep(50 * time.Millisecond)
	}
	if st := outboxState(t, w.alice, id); st == stateConvWaiting {
		t.Fatalf("retraction to bob still waiting %s after bob's old session ended (bob reads it now)", time.Since(ended).Round(time.Second))
	}
}
