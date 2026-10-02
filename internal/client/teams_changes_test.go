package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTeamsReadsDoNotNotifyUnchangedDirectory(t *testing.T) {
	w := teamWorld(t)
	team := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	if _, err := w.bob.Teams(tctx(t)); err != nil {
		t.Fatal(err)
	}
	quietReads := func() {
		t.Helper()
		before, changed := w.bob.Changed()
		for range 10 {
			v, err := w.bob.Teams(tctx(t))
			if err != nil || !v.Current || len(v.Teams) != 1 {
				t.Fatalf("read: %+v, %v", v, err)
			}
		}
		if after, _ := w.bob.Changed(); after != before {
			t.Fatalf("unchanged reads emitted %d change events", after-before)
		}
		select {
		case <-changed:
			t.Fatal("unchanged directory woke the view to read again")
		default:
		}
	}
	quietReads()

	before, _ := w.bob.Changed()
	mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamRename, Team: team.ID, Name: "Operations"})
	v, err := w.bob.Teams(tctx(t))
	if err != nil || len(v.Teams) != 1 || v.Teams[0].Name != "Operations" {
		t.Fatalf("changed directory: %+v, %v", v, err)
	}
	if after, _ := w.bob.Changed(); after <= before {
		t.Fatal("real directory change did not notify the view")
	}
	quietReads()

	w.bob.teamsDisconnected()
	before, _ = w.bob.Changed()
	v, err = w.bob.Teams(tctx(t))
	if err != nil || !v.Current {
		t.Fatalf("same directory recovery: %+v, %v", v, err)
	}
	if after, _ := w.bob.Changed(); after <= before {
		t.Fatal("recovered current status did not notify the view")
	}
	quietReads()

	w.hub.Stop()
	before, _ = w.bob.Changed()
	v, err = w.bob.Teams(tctx(t))
	if err == nil || v.Current {
		t.Fatalf("offline: %+v, %v", v, err)
	}
	if after, _ := w.bob.Changed(); after <= before {
		t.Fatal("loss of current status did not notify the view")
	}
	before, _ = w.bob.Changed()
	_, _ = w.bob.Teams(tctx(t))
	if after, _ := w.bob.Changed(); after != before {
		t.Fatal("repeated unavailable read notified again")
	}
}
