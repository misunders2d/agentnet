package hub

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// TeamSchema is appended once by the store schema owner; no existing step
// changes. These directory records contain no conversation or worker grants.
const TeamSchema = `
CREATE TABLE teams(
 realm_id TEXT NOT NULL, team TEXT NOT NULL, seq INTEGER NOT NULL,
 hash TEXT NOT NULL, state TEXT NOT NULL, PRIMARY KEY(realm_id, team));
CREATE TABLE team_chain(
 realm_id TEXT NOT NULL, team TEXT NOT NULL, seq INTEGER NOT NULL,
 hash TEXT NOT NULL, record TEXT NOT NULL, PRIMARY KEY(realm_id, team, seq),
 UNIQUE(realm_id, team, hash));
`

var errTeamStale = errors.New("team changed meanwhile; fetch its current chain and retry")
var errTeamRefused = errors.New("team operation refused")

func (h *Hub) teamDirectory() (protocol.TeamDirectory, error) {
	return h.teamDirectoryVersion(false)
}

func (h *Hub) teamDirectoryVersion(tags bool) (protocol.TeamDirectory, error) {
	out := protocol.TeamDirectory{RealmID: h.RealmID(), Teams: []protocol.TeamRef{}}
	if tags {
		out.Version = 2
	}
	rows, err := h.store.db.Query(`SELECT team,seq,hash FROM teams WHERE realm_id = ? AND (? OR coalesce(json_extract(state,'$.version'),1) = 1) ORDER BY team LIMIT ?`, h.RealmID(), tags, protocol.MaxTeams+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref protocol.TeamRef
		if err := rows.Scan(&ref.ID, &ref.Seq, &ref.Hash); err != nil {
			return out, err
		}
		out.Teams = append(out.Teams, ref)
	}
	if len(out.Teams) > protocol.MaxTeams {
		out.Teams = out.Teams[:protocol.MaxTeams]
		out.Truncated = true
	}
	return out, rows.Err()
}

func (h *Hub) handleTeams(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	d, err := h.teamDirectoryVersion(r.URL.Query().Get("version") == "2")
	if err != nil {
		writeError(w, 500, "", "team directory unavailable")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func teamHeadIn(q querier, realm, team string) (protocol.TeamState, bool, error) {
	var state protocol.TeamState
	var raw string
	err := q.QueryRow(`SELECT state FROM teams WHERE realm_id=? AND team=?`, realm, team).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	err = json.Unmarshal([]byte(raw), &state)
	return state, true, err
}

// putTeamStep authenticates both publisher and signed author, then applies
// one operation at the current predecessor in the same immediate transaction.
func (s *store) putTeamStep(realm string, caller identity.Public, step protocol.TeamStep, raw []byte) (same bool, err error) {
	if step.RealmID != realm {
		return false, fmt.Errorf("%w: workspace identity mismatch", errTeamRefused)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	me, err := agentIn(tx, caller.Address)
	if err != nil {
		return false, err
	}
	if me.Revoked || me.Pending || me.Person != step.Author.Person || step.Author.Address != caller.Address || step.Author.Fingerprint != caller.Fingerprint() {
		return false, fmt.Errorf("%w: publisher is not the author person's current device", errTeamRefused)
	}
	head, ok, err := personHeadIn(tx, me.Person)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errTeamRefused
	}
	current, err := protocol.ParsePersonRoster(head.record)
	if err != nil {
		return false, err
	}
	if !current.Has(caller.Address, caller.Fingerprint()) {
		return false, errTeamRefused
	}
	var rosterRaw string
	if err := tx.QueryRow(`SELECT record FROM person_chain WHERE person=? AND hash=?`, me.Person, step.Author.Roster).Scan(&rosterRaw); errors.Is(err, sql.ErrNoRows) {
		return false, errRosterStale
	} else if err != nil {
		return false, err
	}
	roster, err := protocol.ParsePersonRoster([]byte(rosterRaw))
	if err != nil {
		return false, err
	}
	dev, ok := roster.Device(step.Author.Fingerprint)
	if !ok || dev.Address != caller.Address || !ed25519.Verify(dev.SignKey, step.Canonical(), step.Sig) {
		return false, fmt.Errorf("%w: invalid author signature", errTeamRefused)
	}
	var stored string
	err = tx.QueryRow(`SELECT hash FROM team_chain WHERE realm_id=? AND team=? AND seq=?`, realm, step.Team, step.Seq).Scan(&stored)
	if err == nil {
		if stored == step.Hash() {
			return true, nil
		}
		return false, errTeamStale
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	prev, found, err := teamHeadIn(tx, realm, step.Team)
	if err != nil {
		return false, err
	}
	var before *protocol.TeamState
	if found {
		if step.Seq != prev.Seq+1 || step.Prev != prev.Hash {
			return false, errTeamStale
		}
		before = &prev
	} else if step.Seq != 0 {
		return false, errTeamStale
	}
	if !found {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM teams WHERE realm_id=?`, realm).Scan(&n); err != nil {
			return false, err
		}
		if n >= protocol.MaxTeams {
			return false, fmt.Errorf("%w: team directory limit reached", errTeamRefused)
		}
	}
	next, err := step.Apply(before, roster)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errTeamRefused, err)
	}
	state, _ := json.Marshal(next)
	if _, err := tx.Exec(`INSERT INTO team_chain(realm_id,team,seq,hash,record) VALUES(?,?,?,?,?)`, realm, step.Team, step.Seq, step.Hash(), string(raw)); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO teams(realm_id,team,seq,hash,state) VALUES(?,?,?,?,?) ON CONFLICT(realm_id,team) DO UPDATE SET seq=excluded.seq,hash=excluded.hash,state=excluded.state`, realm, step.Team, step.Seq, step.Hash(), string(state)); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func (h *Hub) handlePutTeam(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	step, err := protocol.ParseTeamStep(body)
	if err != nil {
		writeError(w, 400, "", err.Error())
		return
	}
	member, err := h.store.agent(caller)
	if err != nil {
		writeError(w, 500, "", "team storage unavailable")
		return
	}
	same, err := h.store.putTeamStep(h.RealmID(), member.Public, step, body)
	switch {
	case errors.Is(err, errTeamStale):
		writeError(w, 409, protocol.CodeTeamStale, err.Error())
		return
	case errors.Is(err, errRosterStale):
		writeError(w, 409, protocol.CodeRosterStale, err.Error())
		return
	case errors.Is(err, errTeamRefused):
		writeError(w, 403, protocol.CodeTeamRefused, err.Error())
		return
	case err != nil:
		writeError(w, 500, "", "team storage unavailable")
		return
	}
	if !same {
		h.membersChanged()
	} // reuse directory push generation, never a job notification
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) handleTeamChain(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	team := r.PathValue("id")
	if !protocol.ValidID(team) {
		writeError(w, 400, "", "invalid team id")
		return
	}
	after := int64(-1)
	var err error
	if text := r.URL.Query().Get("after"); text != "" {
		after, err = strconv.ParseInt(text, 10, 64)
		if err != nil || after < -1 {
			writeError(w, 400, "", "invalid chain offset")
			return
		}
	}
	if _, found, err := teamHeadIn(h.store.db, h.RealmID(), team); err != nil {
		writeError(w, 500, "", "team storage unavailable")
		return
	} else if !found {
		writeError(w, 404, "", "team not found")
		return
	}
	out := protocol.TeamChain{RealmID: h.RealmID(), Team: team, Records: []json.RawMessage{}}
	rows, err := h.store.db.Query(`SELECT record FROM team_chain WHERE realm_id=? AND team=? AND seq>? ORDER BY seq LIMIT ?`, h.RealmID(), team, after, protocol.TeamChainPage+1)
	if err != nil {
		writeError(w, 500, "", "team chain unavailable")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			writeError(w, 500, "", "team chain unavailable")
			return
		}
		out.Records = append(out.Records, json.RawMessage(raw))
	}
	if rows.Err() != nil {
		writeError(w, 500, "", "team chain unavailable")
		return
	}
	if len(out.Records) > protocol.TeamChainPage {
		out.Records = out.Records[:protocol.TeamChainPage]
		out.More = true
	}
	writeJSON(w, 200, out)
}

// Lists are workspace directory metadata, not conversation authority. Deletion
// uses the Hub's existing signed-request/admin checks; no caller-supplied role
// becomes part of the signed manager chain. Retain that chain as a tombstone.
func (h *Hub) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !protocol.ValidID(id) {
		writeError(w, 400, "", "invalid people list id")
		return
	}
	tx, err := h.store.db.Begin()
	if err != nil {
		writeError(w, 500, "", "people list unavailable")
		return
	}
	defer tx.Rollback()
	member, err := agentIn(tx, caller)
	if err != nil {
		writeError(w, 500, "", "people list unavailable")
		return
	}
	if member.Revoked || member.Pending {
		writeError(w, 403, protocol.CodeTeamRefused, "people list deletion refused")
		return
	}
	state, found, err := teamHeadIn(tx, h.RealmID(), id)
	if err != nil {
		writeError(w, 500, "", "people list unavailable")
		return
	}
	if !found {
		writeError(w, 404, "", "people list not found")
		return
	}
	// A removed roster device cannot keep a person's former manager/admin rights.
	if member.Person != "" {
		head, exists, err := personHeadIn(tx, member.Person)
		if err != nil {
			writeError(w, 500, "", "person unavailable")
			return
		}
		roster, err := protocol.ParsePersonRoster(head.record)
		if !exists || err != nil || !roster.Has(caller, member.Public.Fingerprint()) {
			writeError(w, 403, protocol.CodeTeamRefused, "people list deletion refused")
			return
		}
	}
	if !member.Admin && (member.Person == "" || !state.Manager(member.Person)) {
		writeError(w, 403, protocol.CodeTeamRefused, "Only a list manager or workspace admin can delete this list.")
		return
	}
	if _, err := tx.Exec(`DELETE FROM teams WHERE realm_id=? AND team=?`, h.RealmID(), id); err != nil {
		writeError(w, 500, "", "people list deletion failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "", "people list deletion failed")
		return
	}
	h.membersChanged()
	w.WriteHeader(http.StatusNoContent)
}
