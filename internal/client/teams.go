package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

const TeamSchema = `
CREATE TABLE teams(
 realm_id TEXT NOT NULL, team TEXT NOT NULL, seq INTEGER NOT NULL,
 hash TEXT NOT NULL, state TEXT NOT NULL, conflict INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(realm_id, team));
CREATE TABLE team_chain(
 realm_id TEXT NOT NULL, team TEXT NOT NULL, seq INTEGER NOT NULL,
 hash TEXT NOT NULL, record TEXT NOT NULL, state TEXT NOT NULL,
 PRIMARY KEY(realm_id, team, seq), UNIQUE(realm_id, team, hash));
`

type TeamChange struct {
	Team   string `json:"team,omitempty"`
	Op     string `json:"op"`
	Name   string `json:"name,omitempty"`
	Target string `json:"target,omitempty"`
}
type TeamView struct {
	protocol.TeamState
	Member   bool `json:"member"`
	Manager  bool `json:"manager"`
	Conflict bool `json:"conflict"`
	Listed   bool `json:"listed"`
}
type TeamsView struct {
	RealmID   string     `json:"realm_id,omitempty"`
	Status    string     `json:"status"` // available, unknown, unsupported, unavailable, conflict
	Current   bool       `json:"current"`
	Reason    string     `json:"reason,omitempty"`
	At        time.Time  `json:"at"`
	Truncated bool       `json:"truncated"`
	Teams     []TeamView `json:"teams"`
}
type teamRuntime struct {
	mu         sync.Mutex
	supported  string
	current    bool
	at         time.Time
	directory  protocol.TeamDirectory
	pending    *protocol.TeamDirectory
	generation uint64
}

var ErrTeamsUnsupported = errors.New("this Hub does not support teams")
var ErrTeamConflict = errors.New("this team's verified chain conflicts with the one pinned here; changes and selection are blocked")
var ErrTeamStale = errors.New("team directory changed meanwhile; refresh and review it again")

func (a *Agent) teamRealm(ctx context.Context) (string, error) {
	id, err := a.RealmID()
	if errors.Is(err, ErrRealmUnknown) {
		return a.CheckRealm(ctx)
	}
	return id, err
}

func (s *store) teamHead(realm, team string) (protocol.TeamState, bool, bool, error) {
	var state protocol.TeamState
	var raw string
	var conflict bool
	err := s.db.QueryRow(`SELECT state,conflict FROM teams WHERE realm_id=? AND team=?`, realm, team).Scan(&raw, &conflict)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, false, nil
	}
	if err != nil {
		return state, false, false, err
	}
	err = json.Unmarshal([]byte(raw), &state)
	return state, true, conflict, err
}

func (s *store) teamIDs(realm string) ([]string, error) {
	rows, err := s.db.Query(`SELECT team FROM teams WHERE realm_id=?`, realm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TeamView reads retained, verified directory state; offline does not erase
// membership, and an unverified/older pushed directory never makes it current.
func (a *Agent) TeamView() (TeamsView, error) {
	v := TeamsView{Status: "unknown", Teams: []TeamView{}}
	realm, err := a.RealmID()
	if errors.Is(err, ErrRealmUnknown) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.RealmID = realm
	a.teams.mu.Lock()
	v.Status = a.teams.supported
	v.Current = a.teams.current
	v.At = a.teams.at
	d := a.teams.directory
	a.teams.mu.Unlock()
	if v.Status == "" {
		v.Status = "unknown"
	}
	if !v.Current {
		switch v.Status {
		case "unsupported":
			v.Reason = ErrTeamsUnsupported.Error()
		case "conflict":
			v.Reason = "A verified directory chain conflicts with a local pin; team actions are blocked."
		default:
			v.Reason = "The current team directory has not been verified; retained membership is unchanged."
		}
	}
	v.Truncated = d.Truncated
	listed := map[string]bool{}
	for _, r := range d.Teams {
		listed[r.ID] = true
	}
	me, _, err := a.Person()
	if err != nil {
		return v, err
	}
	rows, err := a.store.db.Query(`SELECT state,conflict FROM teams WHERE realm_id=?
 AND NOT EXISTS(SELECT 1 FROM config WHERE k='team-deleted/' || teams.realm_id || '/' || teams.team)
 ORDER BY team`, realm)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var conflict bool
		if err := rows.Scan(&raw, &conflict); err != nil {
			return v, err
		}
		var state protocol.TeamState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return v, err
		}
		v.Teams = append(v.Teams, TeamView{TeamState: state, Member: state.Member(me.Person), Manager: state.Manager(me.Person), Conflict: conflict, Listed: listed[state.ID]})
	}
	return v, rows.Err()
}

func (a *Agent) Teams(ctx context.Context) (TeamsView, error) {
	a.teams.mu.Lock()
	gen := a.teams.generation
	a.teams.mu.Unlock()
	realm, err := a.teamRealm(ctx)
	if err == nil {
		var known []string
		known, err = a.store.teamIDs(realm)
		if err == nil {
			var d protocol.TeamDirectory
			err = a.hub.do(ctx, http.MethodGet, "/v1/teams", nil, &d)
			if err == nil {
				err = a.pinTeamDirectory(ctx, realm, d, true, known)
			}
		}
	}
	if err != nil {
		v, localErr := a.TeamView()
		if localErr != nil {
			return v, localErr
		}
		v.Current = false
		v.Status = "unavailable"
		v.Reason = "The current team directory could not be verified; retained membership is unchanged."
		var he *HubError
		if errors.As(err, &he) && he.Status == 404 {
			v.Status = "unsupported"
			v.Reason = ErrTeamsUnsupported.Error()
			err = ErrTeamsUnsupported
		}
		if errors.Is(err, ErrTeamConflict) || errors.Is(err, errPersonConflict) {
			v.Status = "conflict"
			v.Reason = "A verified directory chain conflicts with a local pin; team actions are blocked."
		}
		a.teams.mu.Lock()
		changed := false
		if a.teams.generation == gen {
			changed = a.teams.current || a.teams.supported != v.Status
			a.teams.current = false
			a.teams.supported = v.Status
		}
		a.teams.mu.Unlock()
		if changed {
			a.changes.bump()
		}
		return v, err
	}
	a.teams.mu.Lock()
	changed := false
	if a.teams.generation == gen && a.teams.pending == nil {
		changed = !a.teams.current
		a.teams.current = true
	}
	a.teams.mu.Unlock()
	if changed {
		a.changes.bump()
	}
	return a.TeamView()
}

func (a *Agent) acceptTeamDirectory(ctx context.Context, realm string, d protocol.TeamDirectory) error {
	known, err := a.store.teamIDs(realm)
	if err != nil {
		return err
	}
	return a.pinTeamDirectory(ctx, realm, d, true, known)
}

func (a *Agent) pinTeamDirectory(ctx context.Context, realm string, d protocol.TeamDirectory, fresh bool, known []string) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.RealmID != realm {
		return ErrRealmInvalid
	}
	for _, ref := range d.Teams {
		state, found, conflict, err := a.store.teamHead(realm, ref.ID)
		if err != nil {
			return err
		}
		if conflict {
			return ErrTeamConflict
		}
		if !found || state.Seq < ref.Seq || state.Hash != ref.Hash {
			if err := a.fetchTeam(ctx, realm, ref.ID); err != nil {
				return err
			}
		}
		var hash string
		if err := a.store.db.QueryRow(`SELECT hash FROM team_chain WHERE realm_id=? AND team=? AND seq=?`, realm, ref.ID, ref.Seq).Scan(&hash); err != nil || hash != ref.Hash {
			return ErrTeamStale
		}
	}
	// Only a complete, authenticated directory can prove omission. Keep local
	// verified chains for audit, but persist deletion independently of signed state
	// so reconnects, restarts and stale pushes cannot resurrect a removed list.
	if fresh && !d.Truncated {
		listed := map[string]bool{}
		for _, ref := range d.Teams {
			listed[ref.ID] = true
		}
		// A delayed response cannot delete a list learned or created after its
		// request began: absence says nothing about those later IDs.
		var removed []string
		for _, id := range known {
			if !listed[id] {
				removed = append(removed, id)
			}
		}
		for _, id := range removed {
			if _, err := a.store.db.Exec(`INSERT OR IGNORE INTO config(k,v) VALUES(?, '1')`, "team-deleted/"+realm+"/"+id); err != nil {
				return err
			}
		}
	}
	a.teams.mu.Lock()
	previous := a.teams.directory
	changed := a.teams.supported != "available" || previous.RealmID != d.RealmID ||
		previous.Truncated != d.Truncated || !slices.Equal(previous.Teams, d.Teams)
	a.teams.directory = d
	a.teams.supported = "available"
	a.teams.at = time.Now()
	a.teams.mu.Unlock()
	// A view re-reads this directory after a change event. An identical read
	// must not emit another event and start a read/change feedback loop.
	if changed {
		a.changes.bump()
	}
	return nil
}

func (a *Agent) fetchTeam(ctx context.Context, realm, team string) error {
	after := int64(-1)
	for {
		var page protocol.TeamChain
		if err := a.hub.do(ctx, http.MethodGet, fmt.Sprintf("/v1/teams/%s/chain?after=%d", team, after), nil, &page); err != nil {
			return err
		}
		if page.RealmID != realm || page.Team != team || len(page.Records) > protocol.TeamChainPage {
			return ErrRealmInvalid
		}
		if len(page.Records) == 0 {
			if page.More {
				return errors.New("team chain did not advance")
			}
			return nil
		}
		steps := make([]protocol.TeamStep, 0, len(page.Records))
		rosters := map[string]protocol.PersonRoster{}
		for _, raw := range page.Records {
			step, err := protocol.ParseTeamStep(raw)
			if err != nil {
				return err
			}
			if step.RealmID != realm || step.Team != team || step.Seq != after+1 {
				return errors.New("team chain step missing or out of scope")
			}
			after = step.Seq
			p, ok, err := a.store.personByID(step.Author.Person)
			if err != nil {
				return err
			}
			if ok && p.info.State == personConflict {
				return errPersonConflict
			}
			r, found, err := a.store.chainStep(step.Author.Person, step.Author.Roster)
			if err != nil {
				return err
			}
			if !found {
				if _, err := a.refreshPerson(ctx, step.Author.Person, false); err != nil {
					return err
				}
				r, found, err = a.store.chainStep(step.Author.Person, step.Author.Roster)
				if err != nil {
					return err
				}
				if !found {
					return errPersonRecord
				}
			}
			rosters[step.Author.Roster] = r
			steps = append(steps, step)
		}
		if err := a.pinTeamSteps(realm, team, steps, page.Records, rosters); err != nil {
			return err
		}
		if !page.More {
			return nil
		}
	}
}

func (a *Agent) pinTeamSteps(realm, team string, steps []protocol.TeamStep, raws []json.RawMessage, rosters map[string]protocol.PersonRoster) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	var conflict bool
	var cur *protocol.TeamState
	err = tx.QueryRow(`SELECT state,conflict FROM teams WHERE realm_id=? AND team=?`, realm, team).Scan(&raw, &conflict)
	if err == nil {
		var state protocol.TeamState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return err
		}
		cur = &state
		if conflict {
			return ErrTeamConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	changed := false
	for i, step := range steps {
		if cur != nil && step.Seq <= cur.Seq {
			var hash string
			if err := tx.QueryRow(`SELECT hash FROM team_chain WHERE realm_id=? AND team=? AND seq=?`, realm, team, step.Seq).Scan(&hash); err != nil {
				return err
			}
			if hash == step.Hash() {
				continue
			}
			var prev *protocol.TeamState
			if step.Seq > 0 {
				var before string
				if err := tx.QueryRow(`SELECT state FROM team_chain WHERE realm_id=? AND team=? AND seq=?`, realm, team, step.Seq-1).Scan(&before); err != nil {
					return err
				}
				var state protocol.TeamState
				if err := json.Unmarshal([]byte(before), &state); err != nil {
					return err
				}
				prev = &state
			}
			if _, err := step.Apply(prev, rosters[step.Author.Roster]); err != nil {
				return err
			}
			tx.Rollback()
			if _, err := a.store.db.Exec(`UPDATE teams SET conflict=1 WHERE realm_id=? AND team=?`, realm, team); err != nil {
				return err
			}
			a.changes.bump()
			return ErrTeamConflict
		}
		next, err := step.Apply(cur, rosters[step.Author.Roster])
		if err != nil {
			return err
		}
		if next.RealmID != realm || next.ID != team {
			return ErrRealmInvalid
		}
		data, _ := json.Marshal(next)
		if _, err := tx.Exec(`INSERT INTO team_chain(realm_id,team,seq,hash,record,state) VALUES(?,?,?,?,?,?)`, realm, team, next.Seq, next.Hash, string(raws[i]), string(data)); err != nil {
			return err
		}
		cur = &next
		changed = true
	}
	if changed {
		data, _ := json.Marshal(cur)
		if _, err := tx.Exec(`INSERT INTO teams(realm_id,team,seq,hash,state) VALUES(?,?,?,?,?) ON CONFLICT(realm_id,team) DO UPDATE SET seq=excluded.seq,hash=excluded.hash,state=excluded.state`, realm, team, cur.Seq, cur.Hash, string(data)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if changed {
		a.changes.bump()
	}
	return nil
}

func (a *Agent) ChangeTeam(ctx context.Context, c TeamChange) (protocol.TeamState, error) {
	var none protocol.TeamState
	if c.Op == "delete" {
		if !protocol.ValidID(c.Team) || c.Name != "" || c.Target != "" {
			return none, errors.New("invalid people list deletion")
		}
		v, err := a.Teams(ctx)
		if err != nil {
			return none, err
		}
		if !v.Current {
			return none, ErrTeamStale
		}
		var state protocol.TeamState
		found := false
		for _, t := range v.Teams {
			if t.ID == c.Team && t.Listed && !t.Conflict {
				state = t.TeamState
				found = true
			}
		}
		if !found {
			return none, errors.New("team not found")
		}
		if err := a.hub.do(ctx, http.MethodDelete, "/v1/teams/"+c.Team, nil, nil); err != nil {
			return none, err
		}
		// The explicit accepted deletion is also proof when the following refresh
		// is unavailable or truncated. It never deletes chat state.
		if _, err := a.store.db.Exec(`INSERT OR IGNORE INTO config(k,v) VALUES(?, '1')`, "team-deleted/"+v.RealmID+"/"+c.Team); err != nil {
			return none, err
		}
		a.changes.bump()
		_, _ = a.Teams(ctx)
		return state, nil
	}
	if c.Op == protocol.TeamCreate {
		if c.Team != "" {
			return none, errors.New("create chooses a new team identity")
		}
		c.Team = protocol.NewID()
	} else if !protocol.ValidID(c.Team) {
		return none, errors.New("invalid team id")
	}
	for attempt := 0; attempt < 2; attempt++ {
		realm, err := a.teamRealm(ctx)
		if err != nil {
			return none, err
		}
		me, ok, err := a.store.selfPerson(a.Address)
		if err != nil {
			return none, err
		}
		if !ok {
			return none, ErrNoPerson
		}
		if _, err := a.refreshPerson(ctx, me.info.Person, false); err != nil {
			return none, err
		}
		me, ok, err = a.store.selfPerson(a.Address)
		if err != nil {
			return none, err
		}
		if !ok || !me.has(a.Address, a.Self().Fingerprint()) {
			return none, ErrNoPerson
		}
		var prev *protocol.TeamState
		if c.Op != protocol.TeamCreate {
			if _, err := a.Teams(ctx); err != nil {
				return none, err
			}
			state, found, conflict, err := a.store.teamHead(realm, c.Team)
			if err != nil {
				return none, err
			}
			if conflict {
				return none, ErrTeamConflict
			}
			if !found {
				return none, errors.New("team not found")
			}
			prev = &state
		}
		step := protocol.TeamStep{V: 1, RealmID: realm, Team: c.Team, Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Op: c.Op, Name: c.Name, Target: c.Target, TS: time.Now().Unix()}
		if prev != nil {
			step.Seq = prev.Seq + 1
			step.Prev = prev.Hash
		}
		step.Sign(a.id.Sign)
		if _, err := step.Apply(prev, me.roster); err != nil {
			return none, err
		}
		err = a.hub.do(ctx, http.MethodPut, "/v1/team", step, nil)
		var he *HubError
		if attempt == 0 && c.Op != protocol.TeamCreate && errors.As(err, &he) && (he.Code == protocol.CodeTeamStale || he.Code == protocol.CodeRosterStale) {
			continue
		}
		if err != nil {
			return none, err
		}
		// Only authenticated acceptance commits the local operation. No
		// optimistic membership or automatic conversation/permission change.
		raw, _ := json.Marshal(step)
		if err := a.pinTeamSteps(realm, c.Team, []protocol.TeamStep{step}, []json.RawMessage{raw}, map[string]protocol.PersonRoster{me.info.Roster: me.roster}); err != nil {
			return none, err
		}
		state, _, conflict, err := a.store.teamHead(realm, c.Team)
		if conflict {
			return none, ErrTeamConflict
		}
		return state, err
	}
	return none, ErrTeamStale
}

func (a *Agent) TeamSnapshot(ctx context.Context, ids []string) (protocol.TeamSnapshot, error) {
	out := protocol.TeamSnapshot{Sources: []protocol.TeamRef{}, Persons: []protocol.PersonRef{}, At: time.Now().Unix()}
	if len(ids) == 0 || len(ids) > protocol.MaxTeams {
		return out, errors.New("select one or more teams")
	}
	v, err := a.Teams(ctx)
	if err != nil {
		return out, err
	}
	if !v.Current {
		return out, ErrTeamStale
	}
	out.RealmID = v.RealmID
	wanted := map[string]bool{}
	for _, id := range ids {
		if !protocol.ValidID(id) {
			return out, errors.New("invalid selected team")
		}
		wanted[id] = true
	}
	people := map[string]bool{}
	for _, t := range v.Teams {
		if !wanted[t.ID] {
			continue
		}
		if !t.Listed || t.Conflict {
			return out, ErrTeamStale
		}
		if t.Archived {
			return out, protocol.ErrTeamArchived
		}
		out.Sources = append(out.Sources, protocol.TeamRef{ID: t.ID, Seq: t.Seq, Hash: t.Hash})
		delete(wanted, t.ID)
		for _, person := range t.Members {
			people[person] = true
		}
	}
	if len(wanted) > 0 {
		return out, errors.New("selected team not in current directory")
	}
	for person := range people {
		if _, err := a.refreshPerson(ctx, person, false); err != nil {
			return out, err
		}
		p, ok, err := a.store.personByID(person)
		if err != nil {
			return out, err
		}
		if !ok || p.info.State == personConflict {
			return out, errPersonConflict
		}
		out.Persons = append(out.Persons, protocol.PersonRef{ID: person, Seq: p.info.Seq, Hash: p.info.Roster})
	}
	slices.SortFunc(out.Sources, func(a, b protocol.TeamRef) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	slices.SortFunc(out.Persons, func(a, b protocol.PersonRef) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out, nil
}

func (a *Agent) teamsConnected(headers http.Header) {
	a.teams.mu.Lock()
	a.teams.current = false
	a.teams.supported = "unsupported"
	if headers.Get(protocol.TeamsHeader) == "1" {
		a.teams.supported = "unknown"
	}
	a.teams.mu.Unlock()
	a.changes.bump()
}
func (a *Agent) teamsDisconnected() {
	a.teams.mu.Lock()
	a.teams.current = false
	a.teams.pending = nil
	a.teams.generation++
	a.teams.mu.Unlock()
	a.changes.bump()
}
func (a *Agent) onTeams(raw []byte) {
	var d protocol.TeamDirectory
	if json.Unmarshal(raw, &d) != nil || d.Validate() != nil {
		a.teams.mu.Lock()
		a.teams.current = false
		a.teams.pending = nil
		a.teams.generation++
		a.teams.mu.Unlock()
		a.changes.bump()
		return
	}
	a.teams.mu.Lock()
	a.teams.pending = &d
	a.teams.current = false
	a.teams.generation++
	a.teams.mu.Unlock()
	a.kickNow()
}

// Runs only on queued push work in the existing stream worker, never the
// SSE reader or a responder. Failures retain verified membership.
func (a *Agent) syncTeams(ctx context.Context) {
	a.teams.mu.Lock()
	pending := a.teams.pending
	gen := a.teams.generation
	a.teams.mu.Unlock()
	if pending == nil {
		return
	}
	realm, err := a.teamRealm(ctx)
	if err == nil {
		err = a.pinTeamDirectory(ctx, realm, *pending, false, nil)
	}
	a.teams.mu.Lock()
	if a.teams.generation == gen {
		a.teams.current = err == nil
		if err == nil {
			a.teams.pending = nil
		} else if errors.Is(err, ErrTeamConflict) || errors.Is(err, errPersonConflict) {
			a.teams.supported = "conflict"
		} else {
			a.teams.supported = "unavailable"
		}
	}
	a.teams.mu.Unlock()
	a.changes.bump()
}
