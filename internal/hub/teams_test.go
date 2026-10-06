package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type hubTeamPerson struct {
	member member
	roster protocol.PersonRoster
}

func hubTeamFixture(t *testing.T) (*Hub, hubTeamPerson, hubTeamPerson) {
	t.Helper()
	h, _, _ := testHub(t)
	var n int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='teams'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		if _, err := h.store.db.Exec(TeamSchema); err != nil {
			t.Fatal(err)
		}
	}
	makePerson := func(label string) hubTeamPerson {
		m := enroll(t, h, label)
		r := protocol.PersonRoster{Person: protocol.NewID(), Label: label, Devices: []identity.Public{m.id.Public(m.addr)}}
		r.Sign(m.id.Sign)
		raw, _ := json.Marshal(r)
		if _, err := h.store.putPersonStep(m.id.Public(m.addr), raw, r, time.Now()); err != nil {
			t.Fatal(err)
		}
		return hubTeamPerson{m, r}
	}
	return h, makePerson("alice"), makePerson("bob")
}
func hubTeamStep(h *Hub, prev *protocol.TeamState, who hubTeamPerson, op, target, name string) protocol.TeamStep {
	s := protocol.TeamStep{V: 1, RealmID: h.RealmID(), Team: protocol.NewID(), Author: protocol.EventAuthor{Person: who.roster.Person, Roster: who.roster.Hash(), Address: who.member.addr, Fingerprint: who.member.id.Public(who.member.addr).Fingerprint()}, Op: op, Target: target, Name: name, TS: time.Now().Unix()}
	if prev != nil {
		s.Team = prev.ID
		s.Seq = prev.Seq + 1
		s.Prev = prev.Hash
	}
	s.Sign(who.member.id.Sign)
	return s
}
func putTeamHTTP(t *testing.T, h *Hub, publisher member, s protocol.TeamStep) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(s)
	w := httptest.NewRecorder()
	h.handlePutTeam(w, signed(t, publisher.id, publisher.addr, http.MethodPut, "/v1/team", raw))
	return w
}

func TestTeamsHubRemovedRosterKeyCannotUseOldAuthority(t *testing.T) {
	h, a, _ := hubTeamFixture(t)
	create := hubTeamStep(h, nil, a, protocol.TeamCreate, "", "Ops")
	if w := putTeamHTTP(t, h, a.member, create); w.Code != 204 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	state, _, _ := teamHeadIn(h.store.db, h.RealmID(), create.Team)
	oldClaim := hubTeamStep(h, &state, a, protocol.TeamRename, "", "Old key")
	offer := protocol.NewID()
	secret := deviceInvite(t, h, a.member, offer)
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	person, _, _ := protocol.SplitAddress(a.member.addr)
	phone := member{id, person + "/phone"}
	if status, e := joinLinked(t, h, secret, phone.addr, offer, a.roster, id); status != http.StatusCreated {
		t.Fatalf("link: %d %+v", status, e)
	}
	next := step(a.roster, a.member, &phone, a.member.id.Public(a.member.addr), phone.id.Public(phone.addr))
	if status, e := put(t, h, a.member, next); status != 204 {
		t.Fatalf("add device: %d %+v", status, e)
	}
	removed := step(next, phone, nil, phone.id.Public(phone.addr))
	if status, e := put(t, h, phone, removed); status != 204 {
		t.Fatalf("remove old key: %d %+v", status, e)
	}
	if w := deleteTeamHTTP(t, h, a.member, create.Team); w.Code != 403 {
		t.Fatalf("removed key deleted list: %d %s", w.Code, w.Body)
	}
	if w := putTeamHTTP(t, h, a.member, oldClaim); w.Code != 403 {
		t.Fatalf("removed key used historical manager authority: %d %s", w.Code, w.Body)
	}
	newClaim := hubTeamStep(h, &state, hubTeamPerson{phone, removed}, protocol.TeamRename, "", "Current device")
	if w := putTeamHTTP(t, h, phone, newClaim); w.Code != 204 {
		t.Fatalf("remaining person device lost manager role: %d %s", w.Code, w.Body)
	}
	state, _, _ = teamHeadIn(h.store.db, h.RealmID(), create.Team)
	if state.Seq != 1 || state.Name != "Current device" || !state.Manager(a.roster.Person) {
		t.Fatalf("person authority changed with device key: %+v", state)
	}
}

func TestTeamsHubCASIdempotenceAndAuthentication(t *testing.T) {
	h, a, b := hubTeamFixture(t)
	create := hubTeamStep(h, nil, a, protocol.TeamCreate, "", "Support")
	if w := putTeamHTTP(t, h, a.member, create); w.Code != 204 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	root, _, err := teamHeadIn(h.store.db, h.RealmID(), create.Team)
	if err != nil {
		t.Fatal(err)
	}
	join := hubTeamStep(h, &root, b, protocol.TeamJoin, "", "")
	rename := hubTeamStep(h, &root, a, protocol.TeamRename, "", "Ops")
	if w := putTeamHTTP(t, h, b.member, join); w.Code != 204 {
		t.Fatalf("join: %d %s", w.Code, w.Body)
	}
	if w := putTeamHTTP(t, h, a.member, rename); w.Code != 409 {
		t.Fatalf("concurrent rename did not CAS stale: %d %s", w.Code, w.Body)
	}
	state, _, _ := teamHeadIn(h.store.db, h.RealmID(), root.ID)
	rename = hubTeamStep(h, &state, a, protocol.TeamRename, "", "Ops")
	for range 2 {
		if w := putTeamHTTP(t, h, a.member, rename); w.Code != 204 {
			t.Fatalf("retry/replay: %d %s", w.Code, w.Body)
		}
	}
	var n int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM team_chain WHERE team=?`, root.ID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("replay duplicated step: %d %v", n, err)
	}
	state, _, _ = teamHeadIn(h.store.db, h.RealmID(), root.ID)
	spoof := hubTeamStep(h, &state, a, protocol.TeamArchive, "", "")
	if w := putTeamHTTP(t, h, b.member, spoof); w.Code != 403 {
		t.Fatalf("different publisher accepted author: %d %s", w.Code, w.Body)
	}
	spoof.Sig[0] ^= 1
	if w := putTeamHTTP(t, h, a.member, spoof); w.Code != 403 {
		t.Fatalf("forged step accepted: %d %s", w.Code, w.Body)
	}
	foreign := hubTeamStep(h, &state, a, protocol.TeamArchive, "", "")
	foreign.RealmID = protocol.NewID()
	foreign.Sign(a.member.id.Sign)
	if w := putTeamHTTP(t, h, a.member, foreign); w.Code != 403 {
		t.Fatalf("foreign realm accepted: %d %s", w.Code, w.Body)
	}
	w := httptest.NewRecorder()
	h.handleTeams(w, httptest.NewRequest(http.MethodGet, "/v1/teams", nil))
	if w.Code != 401 {
		t.Fatal("anonymous directory allowed")
	}
	d, err := h.teamDirectory()
	if err != nil || len(d.Teams) != 1 || d.Teams[0].Seq != 2 {
		t.Fatalf("refusals changed directory: %+v %v", d, err)
	}
}

func TestTeamsHubSelfServiceManagementAndNoMessageState(t *testing.T) {
	h, a, b := hubTeamFixture(t)
	change := func(prev *protocol.TeamState, who hubTeamPerson, op, target, name string, want int) protocol.TeamState {
		t.Helper()
		s := hubTeamStep(h, prev, who, op, target, name)
		w := putTeamHTTP(t, h, who.member, s)
		if w.Code != want {
			t.Fatalf("%s: %d %s", op, w.Code, w.Body)
		}
		state, _, err := teamHeadIn(h.store.db, h.RealmID(), s.Team)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	one := change(nil, a, protocol.TeamCreate, "", "same", 204)
	two := change(nil, a, protocol.TeamCreate, "", "same", 204)
	if one.ID == two.ID {
		t.Fatal("display name merged team identities")
	}
	one = change(&one, b, protocol.TeamJoin, "", "", 204)
	two = change(&two, b, protocol.TeamJoin, "", "", 204)
	one = change(&one, b, protocol.TeamLeave, "", "", 204)
	if one.Member(b.roster.Person) || !two.Member(b.roster.Person) {
		t.Fatal("many-to-many leave changed another team")
	}
	change(&one, a, protocol.TeamLeave, "", "", 403)
	one = change(&one, a, protocol.TeamArchive, "", "", 204)
	change(&one, b, protocol.TeamJoin, "", "", 403)
	change(&one, a, protocol.TeamManagerRemove, a.roster.Person, "", 403)
	one = change(&one, a, protocol.TeamRestore, "", "", 204)
	one = change(&one, b, protocol.TeamJoin, "", "", 204)
	one = change(&one, a, protocol.TeamManagerAdd, b.roster.Person, "", 204)
	change(&one, a, protocol.TeamRemove, b.roster.Person, "", 403)
	one = change(&one, a, protocol.TeamManagerRemove, b.roster.Person, "", 204)
	one = change(&one, a, protocol.TeamRemove, b.roster.Person, "", 204)
	one = change(&one, b, protocol.TeamJoin, "", "", 204)
	if !one.Member(b.roster.Person) {
		t.Fatal("manager removal acted as a ban")
	}
	for _, table := range []string{"messages", "blobs", "notify_pending"} {
		var n int
		if err := h.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("team created %s state: %d %v", table, n, err)
		}
	}
	// A removed/currently mismatched roster device may not publish under an
	// old person claim, even if its old signed roster remains historical.
	if _, err := h.store.db.Exec(`UPDATE agents SET person_id=NULL WHERE address=?`, b.member.addr); err != nil {
		t.Fatal(err)
	}
	if w := putTeamHTTP(t, h, b.member, hubTeamStep(h, &one, b, protocol.TeamLeave, "", "")); w.Code != 403 {
		t.Fatalf("no-longer-person key changed membership: %d %s", w.Code, w.Body)
	}
}
