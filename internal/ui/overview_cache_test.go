package ui

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestOverviewCacheFollowsResponderConfig(t *testing.T) {
	alice, _, _ := liveWorld(t)
	live := NewLive(alice) // no daemon or native job changes this fixture agent
	before, err := live.Overview()
	if err != nil || before.Me.Responder != "" {
		t.Fatalf("initial responder: %q %v", before.Me.Responder, err)
	}
	dir := t.TempDir()
	if err := alice.SetResponder(&client.Responder{Harness: "claude", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	saved, err := live.Overview()
	if err != nil || saved.Me.Responder != "claude" || saved.Me.ResponderDir != dir {
		t.Fatalf("saved responder stayed stale: %+v %v", saved.Me, err)
	}
	if err := alice.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	removed, err := live.Overview()
	if err != nil || removed.Me.Responder != "" {
		t.Fatalf("removed responder stayed cached: %+v %v", removed.Me, err)
	}
}
