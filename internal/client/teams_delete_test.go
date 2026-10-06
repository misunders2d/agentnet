package client

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTeamsDeletionCompleteSnapshotAndRestart(t *testing.T) {
	w := teamWorld(t)
	team := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	v, err := w.bob.Teams(tctx(t))
	if err != nil || len(v.Teams) != 1 {
		t.Fatal(v, err)
	}
	// Omission in a partial directory is never deletion.
	partial := protocol.TeamDirectory{RealmID: v.RealmID, Teams: []protocol.TeamRef{}, Truncated: true}
	if err := w.bob.acceptTeamDirectory(tctx(t), v.RealmID, partial); err != nil {
		t.Fatal(err)
	}
	if v, err := w.bob.TeamView(); err != nil || len(v.Teams) != 1 {
		t.Fatal("partial list deleted retained state", v, err)
	}
	mustTeamChange(t, w.alice, TeamChange{Op: "delete", Team: team.ID})
	v, err = w.bob.Teams(tctx(t))
	if err != nil || len(v.Teams) != 0 {
		t.Fatal("deleted list retained", v, err)
	}
	if _, err := w.bob.TeamSnapshot(tctx(t), []string{team.ID}); err == nil {
		t.Fatal("deleted list selected")
	}
	w.bob.Close()
	again, err := Open(w.bob.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if v, err := again.TeamView(); err != nil || len(v.Teams) != 0 {
		t.Fatal("restart resurrected deleted list", v, err)
	}
	// Retained signed history remains available for audit, independently of UI.
	if _, found, _, err := again.store.teamHead(v.RealmID, team.ID); err != nil || !found {
		t.Fatal("verified chain lost", found, err)
	}
}

// Delay one already-captured complete response while a new list is created.
// The old omission must not become a permanent local deletion tombstone.
type delayedTeamDirectoryTransport struct {
	base     http.RoundTripper
	captured chan struct{}
	release  chan struct{}
	held     atomic.Bool
}

func (rt *delayedTeamDirectoryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := rt.base.RoundTrip(r)
	if err == nil && r.Method == http.MethodGet && r.URL.Path == "/v1/teams" && rt.held.CompareAndSwap(false, true) {
		close(rt.captured)
		select {
		case <-rt.release:
		case <-r.Context().Done():
			response.Body.Close()
			return nil, r.Context().Err()
		}
	}
	return response, err
}
func TestTeamsDelayedSnapshotDoesNotDeleteConcurrentCreate(t *testing.T) {
	w := teamWorld(t)
	rt := &delayedTeamDirectoryTransport{base: w.alice.hub.http.Transport, captured: make(chan struct{}), release: make(chan struct{})}
	w.alice.hub.http.Transport = rt
	done := make(chan error, 1)
	go func() { _, err := w.alice.Teams(tctx(t)); done <- err }()
	<-rt.captured
	created := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Later"})
	close(rt.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	view, err := w.alice.Teams(tctx(t))
	if err != nil || len(view.Teams) != 1 || view.Teams[0].ID != created.ID {
		t.Fatal("delayed snapshot deleted new list", view, err)
	}
	var tombstones int
	if err := w.alice.store.db.QueryRow(`SELECT COUNT(*) FROM config WHERE k=?`, "team-deleted/"+created.RealmID+"/"+created.ID).Scan(&tombstones); err != nil || tombstones != 0 {
		t.Fatal("new list permanently tombstoned", tombstones, err)
	}
}
