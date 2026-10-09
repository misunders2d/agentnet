package client

import (
	"reflect"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTeamTagsSharedExactSelectionWithoutGrants(t *testing.T) {
	w := teamWorld(t)
	addRealmHistoryAndGrants(t, w.bob)
	before := realmRows(t, w.bob.store.db)
	team := mustTeamChange(t, w.alice, TeamChange{V: 2, Op: protocol.TeamCreate, Name: "Reviewers"})
	ref := protocol.TeamAgent{ID: protocol.NewID(), Host: w.bob.Address, HostKey: w.bob.Self().Fingerprint()}
	mustTeamChange(t, w.alice, TeamChange{Team: team.ID, Op: protocol.TeamAgentAdd, Agent: &ref})
	p, _, _ := w.bob.Person()
	mustTeamChange(t, w.alice, TeamChange{Team: team.ID, Op: protocol.TeamAdd, Target: p.Person})

	v, err := w.bob.Teams(tctx(t))
	if err != nil || !v.Current || !v.Tags || len(v.Teams) != 1 || len(v.Teams[0].Agents) != 1 || v.Teams[0].Agents[0] != ref || len(v.Teams[0].Members) != 1 {
		t.Fatalf("nonmanager cannot resolve the shared exact tag: %+v %v", v, err)
	}
	if _, err := w.bob.ChangeTeam(tctx(t), TeamChange{Team: team.ID, Op: protocol.TeamAgentRemove, Agent: &ref}); err == nil {
		t.Fatal("nonmanager edited targets")
	}
	snap, err := w.bob.TeamSnapshot(tctx(t), []string{team.ID})
	if err != nil || len(snap.Persons) != 1 || snap.Persons[0].ID != p.Person {
		t.Fatalf("human invite snapshot incorrectly expands agents/manager: %+v %v", snap, err)
	}
	after := realmRows(t, w.bob.store.db)
	for _, table := range []string{"approvals", "task_grants", "inbox", "outbox"} {
		if !reflect.DeepEqual(before[table], after[table]) {
			t.Fatalf("tag created %s", table)
		}
	}
}

func TestTeamTagsPushUpdatesExactTargets(t *testing.T) {
	w := teamWorld(t)
	runAgent(t, w.bob)
	team := mustTeamChange(t, w.alice, TeamChange{V: 2, Op: protocol.TeamCreate, Name: "Reviewers"})
	ref := protocol.TeamAgent{ID: protocol.NewID(), Host: w.bob.Address, HostKey: w.bob.Self().Fingerprint()}
	mustTeamChange(t, w.alice, TeamChange{Team: team.ID, Op: protocol.TeamAgentAdd, Agent: &ref})
	p, _, _ := w.bob.Person()
	mustTeamChange(t, w.alice, TeamChange{Team: team.ID, Op: protocol.TeamAdd, Target: p.Person})
	eventually(t, "v2 exact tag targets pushed to another person", func() bool {
		v, err := w.bob.TeamView()
		return err == nil && v.Current && v.Tags && len(v.Teams) == 1 && len(v.Teams[0].Agents) == 1 && v.Teams[0].Agents[0] == ref && len(v.Teams[0].Members) == 1
	})
}
