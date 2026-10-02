package hub

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const GroupHubSchema = `
CREATE TABLE group_heads(
 conv TEXT NOT NULL, bootstrap TEXT NOT NULL, realm TEXT NOT NULL, seq INTEGER NOT NULL,
 hash TEXT NOT NULL, admins TEXT NOT NULL, PRIMARY KEY(conv,bootstrap));
CREATE TABLE group_journal(
 conv TEXT NOT NULL, bootstrap TEXT NOT NULL, seq INTEGER NOT NULL, hash TEXT NOT NULL,
 record BLOB NOT NULL, PRIMARY KEY(conv,bootstrap,seq),
 FOREIGN KEY(conv,bootstrap) REFERENCES group_heads(conv,bootstrap));
`

var errGroupForbidden = errors.New("group: only a current device of an admin person may append")
var errGroupStale = errors.New("group: head changed; rebase and obtain fresh admission consent")
var errGroupRealm = errors.New("group: foreign workspace")

type groupHeadRow struct {
	seq         int64
	hash, realm string
	admins      []string
}

func groupHeadIn(q querier, conv, bootstrap string) (groupHeadRow, bool, error) {
	var h groupHeadRow
	var admins string
	err := q.QueryRow("SELECT seq,hash,realm,admins FROM group_heads WHERE conv=? AND bootstrap=?", conv, bootstrap).Scan(&h.seq, &h.hash, &h.realm, &admins)
	if errors.Is(err, sql.ErrNoRows) {
		return h, false, nil
	}
	if err != nil {
		return h, false, err
	}
	err = json.Unmarshal([]byte(admins), &h.admins)
	return h, true, err
}
func groupPersonIn(q querier, address string) (agent, protocol.PersonRoster, error) {
	me, err := agentIn(q, address)
	if err != nil {
		return me, protocol.PersonRoster{}, err
	}
	if me.Revoked || me.Pending || me.Person == "" {
		return me, protocol.PersonRoster{}, errGroupForbidden
	}
	head, ok, err := personHeadIn(q, me.Person)
	if err != nil {
		return me, protocol.PersonRoster{}, err
	}
	if !ok {
		return me, protocol.PersonRoster{}, errGroupForbidden
	}
	roster, err := protocol.ParsePersonRoster(head.record)
	if err != nil {
		return me, roster, err
	}
	if !roster.Has(address, me.Public.Fingerprint()) {
		return me, roster, errGroupForbidden
	}
	return me, roster, nil
}

// putGroupCommit stores full signed ciphertext in the same immediate transaction
// as current-device/person/admin checks and CAS. No fanout completion inferred.
func (s *store) putGroupCommit(caller identity.Public, c protocol.GroupCommit, realm string) (protocol.GroupJournalResult, error) {
	result := protocol.GroupJournalResult{}
	if c.Realm != realm {
		return result, errGroupRealm
	}
	if err := c.Verify(caller); err != nil {
		return result, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	me, roster, err := groupPersonIn(tx, caller.Address)
	if err != nil {
		return result, err
	}
	if me.Public.Fingerprint() != caller.Fingerprint() || c.Actor != me.Person {
		return result, errGroupForbidden
	}
	head, found, err := groupHeadIn(tx, c.Conv, c.Bootstrap)
	if err != nil {
		return result, err
	}
	if found {
		if head.realm != realm {
			return result, errGroupRealm
		}
	} else if caller.Fingerprint() != c.Bootstrap || c.Seq != 0 || !slices.Contains(c.Admins, me.Person) {
		return result, errGroupForbidden
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return result, err
	}
	var previous []byte
	err = tx.QueryRow("SELECT record FROM group_journal WHERE conv=? AND bootstrap=? AND seq=?", c.Conv, c.Bootstrap, c.Seq).Scan(&previous)
	if err == nil {
		if !bytes.Equal(previous, raw) {
			return result, errGroupStale
		}
		return protocol.GroupJournalResult{Seq: c.Seq, Hash: c.Hash, Same: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	// An exact already-stored replay proves custody only. It creates no append
	// authority after self-removal; a removed device is still refused above.
	if c.ActorRoster != roster.Hash() || found && !slices.Contains(head.admins, me.Person) {
		return result, errGroupForbidden
	}
	if found && (c.Seq != head.seq+1 || c.Prev != head.hash) || !found && (c.Seq != 0 || c.Prev != "") {
		return result, errGroupStale
	}
	for _, admin := range c.Admins {
		if _, ok, err := personHeadIn(tx, admin); err != nil {
			return result, err
		} else if !ok {
			return result, errGroupForbidden
		}
	}
	admins, _ := json.Marshal(c.Admins)
	if found {
		_, err = tx.Exec("UPDATE group_heads SET seq=?,hash=?,admins=? WHERE conv=? AND bootstrap=?", c.Seq, c.Hash, string(admins), c.Conv, c.Bootstrap)
	} else {
		_, err = tx.Exec("INSERT INTO group_heads(conv,bootstrap,realm,seq,hash,admins)VALUES(?,?,?,?,?,?)", c.Conv, c.Bootstrap, c.Realm, c.Seq, c.Hash, string(admins))
	}
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec("INSERT INTO group_journal(conv,bootstrap,seq,hash,record)VALUES(?,?,?,?,?)", c.Conv, c.Bootstrap, c.Seq, c.Hash, raw); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return protocol.GroupJournalResult{Seq: c.Seq, Hash: c.Hash}, nil
}
func (s *store) groupChain(caller, conv, bootstrap string, after int64) (protocol.GroupJournalPage, error) {
	page := protocol.GroupJournalPage{Records: []protocol.GroupCommit{}}
	tx, err := s.db.Begin()
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	me, _, err := groupPersonIn(tx, caller)
	if err != nil {
		return page, err
	}
	head, found, err := groupHeadIn(tx, conv, bootstrap)
	if err != nil {
		return page, err
	}
	if !found {
		return page, errNotFound
	}
	if !slices.Contains(head.admins, me.Person) {
		return page, errGroupForbidden
	}
	rows, err := tx.Query("SELECT record FROM group_journal WHERE conv=? AND bootstrap=? AND seq>? ORDER BY seq LIMIT 17", conv, bootstrap, after)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	pageBytes := 64
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return page, err
		}
		if pageBytes+len(raw)+1 > protocol.MaxBody-1024 {
			page.More = true
			break
		}
		pageBytes += len(raw) + 1
		var c protocol.GroupCommit
		if err = json.Unmarshal(raw, &c); err != nil {
			return page, err
		}
		page.Records = append(page.Records, c)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Records) > 16 {
		page.More = true
		page.Records = page.Records[:16]
	}
	return page, nil
}
func (s *store) groupHeads(caller string) ([]protocol.GroupHead, error) {
	out := []protocol.GroupHead{}
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	me, _, err := groupPersonIn(tx, caller)
	if errors.Is(err, errGroupForbidden) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	rows, err := tx.Query("SELECT conv,bootstrap,seq,hash FROM group_heads WHERE EXISTS(SELECT 1 FROM json_each(group_heads.admins) WHERE value=?) ORDER BY conv", me.Person)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var h protocol.GroupHead
		if err = rows.Scan(&h.Conv, &h.Bootstrap, &h.Seq, &h.Hash); err != nil {
			return out, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
func (h *Hub) handleGroupCommit(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var c protocol.GroupCommit
	if decodeStrict(body, &c) != nil || c.Validate() != nil || c.Conv != r.PathValue("id") || c.Bootstrap != r.URL.Query().Get("creator") {
		writeError(w, 400, "", "invalid group commit")
		return
	}
	me, err := h.store.agent(caller)
	if err != nil {
		writeError(w, 500, "", "storage error")
		return
	}
	result, err := h.store.putGroupCommit(me.Public, c, h.RealmID())
	switch {
	case errors.Is(err, errGroupForbidden):
		writeError(w, 403, "group_forbidden", err.Error())
	case errors.Is(err, errGroupStale):
		writeError(w, 409, "group_stale", err.Error())
	case errors.Is(err, errGroupRealm):
		writeError(w, 409, "group_realm", err.Error())
	case err != nil:
		writeError(w, 400, "", "group commit refused")
	default:
		h.streams.notifyAll()
		writeJSON(w, 200, result)
	}
}
func (h *Hub) handleGroupChain(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	conv := r.PathValue("id")
	after := int64(-1)
	if raw := r.URL.Query().Get("after"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < -1 {
			writeError(w, 400, "", "invalid group cursor")
			return
		}
	}
	bootstrap := r.URL.Query().Get("creator")
	if !protocol.ValidHash(conv) || !protocol.ValidFingerprint(bootstrap) {
		writeError(w, 400, "", "invalid conversation")
		return
	}
	page, err := h.store.groupChain(caller, conv, bootstrap, after)
	switch {
	case errors.Is(err, errGroupForbidden):
		writeError(w, 403, "group_forbidden", err.Error())
	case errors.Is(err, errNotFound):
		writeError(w, 404, "", "unknown group")
	case err != nil:
		writeError(w, 500, "", "storage error")
	default:
		writeJSON(w, 200, page)
	}
}
