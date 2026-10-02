package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func teamWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	return w
}
func mustTeamChange(t *testing.T, a *Agent, c TeamChange) protocol.TeamState {
	t.Helper()
	state, err := a.ChangeTeam(tctx(t), c)
	if err != nil {
		t.Fatalf("team %s: %v", c.Op, err)
	}
	return state
}

func TestTeamsClientManagementSnapshotsAndNoGrants(t *testing.T) {
	w := teamWorld(t)
	addRealmHistoryAndGrants(t, w.bob)
	before := realmRows(t, w.bob.store.db)
	one := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Support"})
	two := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Support"})
	if one.ID == two.ID {
		t.Fatal("same labels merged teams")
	}
	mustTeamChange(t, w.bob, TeamChange{Op: protocol.TeamJoin, Team: one.ID})
	mustTeamChange(t, w.bob, TeamChange{Op: protocol.TeamJoin, Team: two.ID})
	snap, err := w.bob.TeamSnapshot(tctx(t), []string{one.ID, two.ID, one.ID})
	if err != nil || len(snap.Sources) != 2 || len(snap.Persons) != 2 {
		t.Fatalf("deduped verified snapshot: %+v %v", snap, err)
	}
	for _, ref := range snap.Persons {
		r, ok, err := w.bob.store.chainStep(ref.ID, ref.Hash)
		if err != nil || !ok || r.Seq != ref.Seq {
			t.Fatalf("snapshot contains unverified roster: %+v %v", ref, err)
		}
	}
	immutable, _ := json.Marshal(snap)
	mustTeamChange(t, w.bob, TeamChange{Op: protocol.TeamLeave, Team: one.ID})
	view, err := w.bob.Teams(tctx(t))
	if err != nil || !view.Current || len(view.Teams) != 2 {
		t.Fatalf("directory: %+v %v", view, err)
	}
	for _, team := range view.Teams {
		if (team.ID == one.ID && team.Member) || (team.ID == two.ID && !team.Member) {
			t.Fatal("leave changed another team")
		}
	}
	if after, _ := json.Marshal(snap); string(after) != string(immutable) {
		t.Fatal("later membership edited old invitation snapshot")
	}
	if _, err := w.alice.ChangeTeam(tctx(t), TeamChange{Op: protocol.TeamLeave, Team: one.ID}); !errors.Is(err, protocol.ErrTeamLastManager) {
		t.Fatalf("last manager silently left: %v", err)
	}
	mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamArchive, Team: one.ID})
	if _, err := w.bob.ChangeTeam(tctx(t), TeamChange{Op: protocol.TeamJoin, Team: one.ID}); !errors.Is(err, protocol.ErrTeamArchived) {
		t.Fatalf("archived team joined: %v", err)
	}
	mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamRestore, Team: one.ID})
	mustTeamChange(t, w.bob, TeamChange{Op: protocol.TeamJoin, Team: one.ID})
	p, _, _ := w.bob.Person()
	mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamManagerAdd, Team: one.ID, Target: p.Person})
	mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamManagerRemove, Team: one.ID, Target: p.Person})
	mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamRemove, Team: one.ID, Target: p.Person})
	mustTeamChange(t, w.bob, TeamChange{Op: protocol.TeamJoin, Team: one.ID})
	after := realmRows(t, w.bob.store.db)
	for _, table := range []string{"peers", "approvals", "task_grants", "inbox", "outbox"} {
		if !reflect.DeepEqual(before[table], after[table]) {
			t.Fatalf("team granted or created %s", table)
		}
	}
	for _, table := range []string{"conversations", "participation_events", "history_jobs", "operators", "reported", "attachments"} {
		var n int
		if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("team created conversation/history/job/file state in %s: %d %v", table, n, err)
		}
	}
}

type teamUnsupportedTransport struct{ base http.RoundTripper }

func (rt teamUnsupportedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/teams" {
		return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"older Hub"}`)), Request: r}, nil
	}
	return rt.base.RoundTrip(r)
}
func TestTeamsClientUnsupportedHubRetainsMembershipWithoutCurrentClaim(t *testing.T) {
	w := teamWorld(t)
	state := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	mustTeamChange(t, w.bob, TeamChange{Op: protocol.TeamJoin, Team: state.ID})
	w.bob.hub.http.Transport = teamUnsupportedTransport{w.bob.hub.http.Transport}
	v, err := w.bob.Teams(tctx(t))
	if !errors.Is(err, ErrTeamsUnsupported) || v.Current || v.Status != "unsupported" || len(v.Teams) != 1 || !v.Teams[0].Member {
		t.Fatalf("older Hub discarded/fabricated membership: %+v %v", v, err)
	}
	if _, err := w.bob.TeamSnapshot(tctx(t), []string{state.ID}); !errors.Is(err, ErrTeamsUnsupported) {
		t.Fatalf("older Hub selected current people: %v", err)
	}
}

func TestTeamsClientUnconfirmedMutationKeepsMembership(t *testing.T) {
	w := teamWorld(t)
	team := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	if _, err := w.bob.Teams(tctx(t)); err != nil {
		t.Fatal(err)
	}
	before, _, _, _ := w.bob.store.teamHead(team.RealmID, team.ID)
	f := injectFaults(w.bob)
	f.add(http.MethodPut, "/v1/team", 1, true)
	if _, err := w.bob.ChangeTeam(tctx(t), TeamChange{Op: protocol.TeamJoin, Team: team.ID}); err == nil {
		t.Fatal("lost acceptance claimed success")
	}
	after, _, _, _ := w.bob.store.teamHead(team.RealmID, team.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unknown operation optimistically changed local membership")
	}
	view, err := w.bob.Teams(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Teams) != 1 || !view.Teams[0].Member {
		t.Fatal("explicit authenticated refresh did not reconcile published membership")
	}
	if cached, err := w.bob.TeamView(); err != nil || !cached.Current {
		t.Fatalf("verified query was lost in cached status: %+v %v", cached, err)
	}
	w.hub.Stop()
	view, err = w.bob.Teams(tctx(t))
	if err == nil || view.Current || view.Status != "unavailable" || len(view.Teams) != 1 || !view.Teams[0].Member {
		t.Fatalf("offline erased/claimed current membership: %+v %v", view, err)
	}
	if cached, err := w.bob.TeamView(); err != nil || cached.Current || cached.Status != "unavailable" || !cached.Teams[0].Member {
		t.Fatalf("offline cached status still claimed current: %+v %v", cached, err)
	}
}

func TestTeamsClientMalformedPushInvalidatesPendingConfirmation(t *testing.T) {
	w := teamWorld(t)
	state := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	if _, err := w.bob.Teams(tctx(t)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.TeamDirectory{RealmID: state.RealmID, Teams: []protocol.TeamRef{{ID: state.ID, Seq: state.Seq, Hash: state.Hash}}})
	w.bob.onTeams(raw)
	w.bob.onTeams([]byte(`{"realm_id":"unverified","teams":[]}`))
	w.bob.syncTeams(tctx(t))
	view, err := w.bob.TeamView()
	if err != nil || view.Current || len(view.Teams) != 1 || view.Teams[0].Hash != state.Hash {
		t.Fatalf("malformed newer push confirmed older queued state or erased membership: %+v %v", view, err)
	}
}

// Force another legitimate signed update after the caller chose its
// predecessor, proving the actual client re-fetches/re-signs only once.
type teamRaceTransport struct {
	base   http.RoundTripper
	once   sync.Once
	before func()
}

func (rt *teamRaceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut && r.URL.Path == "/v1/team" {
		rt.once.Do(rt.before)
	}
	return rt.base.RoundTrip(r)
}
func TestTeamsClientCASRetryIsNotPinnedConflict(t *testing.T) {
	w := teamWorld(t)
	team := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	w.bob.hub.http.Transport = &teamRaceTransport{base: w.bob.hub.http.Transport, before: func() {
		mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamRename, Team: team.ID, Name: "Renamed"})
	}}
	state := mustTeamChange(t, w.bob, TeamChange{Op: protocol.TeamJoin, Team: team.ID})
	if state.Seq != 2 || state.Name != "Renamed" {
		t.Fatalf("did not rebase legitimate update: %+v", state)
	}
	_, _, conflict, err := w.bob.store.teamHead(team.RealmID, team.ID)
	if err != nil || conflict {
		t.Fatalf("normal CAS race froze team: %v", err)
	}
}

func TestTeamsClientVerifiedForkFreezesButSpoofDoesNot(t *testing.T) {
	w := teamWorld(t)
	state := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	if _, err := w.bob.Teams(tctx(t)); err != nil {
		t.Fatal(err)
	}
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	step := protocol.TeamStep{V: 1, RealmID: state.RealmID, Team: state.ID, Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}, Op: protocol.TeamCreate, Name: "Fork", TS: 1}
	step.Sign(w.bob.id.Sign)
	raw, _ := json.Marshal(step)
	if err := w.bob.pinTeamSteps(state.RealmID, state.ID, []protocol.TeamStep{step}, []json.RawMessage{raw}, map[string]protocol.PersonRoster{me.info.Roster: me.roster}); err == nil {
		t.Fatal("spoofed signature accepted")
	}
	_, _, conflict, _ := w.bob.store.teamHead(state.RealmID, state.ID)
	if conflict {
		t.Fatal("unverified forgery froze team")
	}
	step.Sign(w.alice.id.Sign)
	raw, _ = json.Marshal(step)
	if err := w.bob.pinTeamSteps(state.RealmID, state.ID, []protocol.TeamStep{step}, []json.RawMessage{raw}, map[string]protocol.PersonRoster{me.info.Roster: me.roster}); !errors.Is(err, ErrTeamConflict) {
		t.Fatalf("verified pinned conflict accepted: %v", err)
	}
	if _, err := w.bob.ChangeTeam(tctx(t), TeamChange{Op: protocol.TeamJoin, Team: state.ID}); !errors.Is(err, ErrTeamConflict) {
		t.Fatalf("frozen team changed: %v", err)
	}
}

func TestTeamsClientForeignRealmAndRosterlessServiceRefuse(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.SetService(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.ChangeTeam(tctx(t), TeamChange{Op: protocol.TeamCreate, Name: "Service"}); !errors.Is(err, ErrNoPerson) {
		t.Fatalf("rosterless service identity inferred: %v", err)
	}
	if _, ok, _ := w.bob.Person(); ok {
		t.Fatal("service silently received a person")
	}
	realm, _ := w.bob.RealmID()
	if err := w.bob.acceptTeamDirectory(context.Background(), realm, protocol.TeamDirectory{RealmID: protocol.NewID(), Teams: []protocol.TeamRef{}}); !errors.Is(err, ErrRealmInvalid) {
		t.Fatalf("foreign namespace directory accepted: %v", err)
	}
}

func TestTeamsClientLinkedDevicePushAndOfflineReopen(t *testing.T) {
	w := teamWorld(t)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	phone, awaited, _ := linkPhone(t, w.alice, "teamphone")
	req := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	stop := runAgent(t, phone)
	team := mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamCreate, Name: "Push"})
	eventually(t, "team pushed to linked device", func() bool {
		v, err := phone.TeamView()
		return err == nil && v.Current && len(v.Teams) == 1 && v.Teams[0].ID == team.ID && v.Teams[0].Member
	})
	stop()
	phone.Close()
	mustTeamChange(t, w.alice, TeamChange{Op: protocol.TeamRename, Team: team.ID, Name: "While offline"})
	again, err := Open(phone.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	v, err := again.TeamView()
	if err != nil || v.Current || len(v.Teams) != 1 {
		t.Fatalf("offline restart lost known state/current truth: %+v %v", v, err)
	}
	runAgent(t, again)
	eventually(t, "reconnect gets verified current team", func() bool {
		v, err := again.TeamView()
		return err == nil && v.Current && len(v.Teams) == 1 && v.Teams[0].Name == "While offline"
	})
}
