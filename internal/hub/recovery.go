package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/googleauth"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const recoverySchema = `
CREATE TABLE person_recoveries (
 id TEXT PRIMARY KEY, realm TEXT NOT NULL, old_person TEXT NOT NULL,
 new_person TEXT NOT NULL, record TEXT NOT NULL, subject TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','committed','denied')),
 actor TEXT NOT NULL DEFAULT '', actor_key TEXT NOT NULL DEFAULT '', actor_person TEXT NOT NULL DEFAULT '', decided INTEGER NOT NULL DEFAULT 0
 );
CREATE UNIQUE INDEX person_recoveries_old ON person_recoveries(realm,old_person) WHERE status!='denied';
CREATE INDEX person_recoveries_new ON person_recoveries(realm,new_person);
`

var errRecovery = errors.New("recovery proof, state or authorization refused")

func recoveryRosters(q querier, s protocol.PersonSuccessor) (protocol.PersonRoster, protocol.PersonRoster, protocol.PersonRoster, error) {
	old, ok, e := personHeadIn(q, s.OldPerson)
	if e != nil {
		return protocol.PersonRoster{}, protocol.PersonRoster{}, protocol.PersonRoster{}, e
	}
	if !ok || old.hash != s.OldHead {
		return protocol.PersonRoster{}, protocol.PersonRoster{}, protocol.PersonRoster{}, errRecovery
	}
	o, e := protocol.ParsePersonRoster(old.record)
	if e != nil {
		return o, o, o, e
	}
	head, ok, e := personHeadIn(q, s.NewPerson)
	if e != nil {
		return o, o, o, e
	}
	if !ok {
		return o, o, o, errRecovery
	}
	n, e := protocol.ParsePersonRoster(head.record)
	if e != nil {
		return o, o, o, e
	}
	var raw string
	if e = q.QueryRow(`SELECT record FROM person_chain WHERE person=? AND seq=0`, s.NewPerson).Scan(&raw); e != nil {
		return o, n, n, e
	}
	g, e := protocol.ParsePersonRoster([]byte(raw))
	return o, g, n, e
}
func validateRecovery(q querier, realm string, s protocol.PersonSuccessor) error {
	o, g, n, e := recoveryRosters(q, s)
	if e != nil {
		return e
	}
	if o.Email != s.Email || s.Verify(realm, g, n) != nil {
		return errRecovery
	}
	d, _ := n.Device(s.Signer)
	var active int
	if e = q.QueryRow(`SELECT count(*) FROM agents WHERE address=? AND person_id=? AND revoked_at IS NULL AND pending_person IS NULL`, d.Address, s.NewPerson).Scan(&active); e != nil {
		return e
	}
	if active != 1 {
		return errRecovery
	}
	// A target with an outgoing committed successor is no longer current.
	// This also prevents a cycle when an old predecessor is chosen as target.
	var count int
	if e = q.QueryRow(`SELECT count(*) FROM person_recoveries WHERE realm=? AND old_person=? AND status='committed'`, realm, s.NewPerson).Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return errRecovery
	}
	return nil
}
func (s *store) requestRecovery(realm, caller string, c googleauth.Claims, req protocol.RecoveryRequest) (protocol.RecoveryRecord, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return protocol.RecoveryRecord{}, e
	}
	defer tx.Rollback()
	p := req.Successor
	if req.Google.Verify() != nil || c.Email != p.Email || c.IssuedAt <= 0 || time.Now().Unix()-c.IssuedAt > 300 || c.IssuedAt > time.Now().Unix()+60 || time.Now().Unix()-p.Created > 900 || p.Created > time.Now().Unix()+60 {
		return protocol.RecoveryRecord{}, errRecovery
	}
	a, e := agentIn(tx, caller)
	if e != nil {
		return protocol.RecoveryRecord{}, e
	}
	if a.Revoked || a.Pending || a.Person != p.NewPerson || a.Public.Fingerprint() != p.Signer || req.Google.Public.Fingerprint() != p.Signer || req.Google.Public.Address != caller {
		return protocol.RecoveryRecord{}, errRecovery
	}
	if _, e = googleAllowed(tx, c); e != nil {
		return protocol.RecoveryRecord{}, e
	}
	if e = validateRecovery(tx, realm, p); e != nil {
		return protocol.RecoveryRecord{}, e
	}
	// A surviving binding may refer to old or new, never an unrelated person.
	var bound string
	e = tx.QueryRow(`SELECT person FROM google_people WHERE email=?`, p.Email).Scan(&bound)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return protocol.RecoveryRecord{}, e
	}
	if bound != "" && (bound != p.OldPerson && bound != p.NewPerson) {
		return protocol.RecoveryRecord{}, errRecovery
	}
	raw, _ := json.Marshal(p)
	_, e = tx.Exec(`INSERT INTO person_recoveries(id,realm,old_person,new_person,record,subject,status) VALUES(?,?,?,?,?,?,'pending') ON CONFLICT DO NOTHING`, p.Hash(), realm, p.OldPerson, p.NewPerson, string(raw), c.Subject)
	if e != nil {
		return protocol.RecoveryRecord{}, e
	}
	existing, e := recoveryIn(tx, p.Hash())
	if e != nil {
		return existing, errRecovery
	}
	if e = tx.Commit(); e != nil {
		return existing, e
	}
	return existing, nil
}
func recoveryIn(q querier, id string) (protocol.RecoveryRecord, error) {
	var v protocol.RecoveryRecord
	v.ID = id
	var raw string
	e := q.QueryRow(`SELECT record,status,actor,actor_key,actor_person,decided FROM person_recoveries WHERE id=?`, id).Scan(&raw, &v.Status, &v.Actor, &v.ActorKey, &v.ActorPerson, &v.Decided)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &v.Successor)
	}
	return v, e
}
func (s *store) decideRecovery(realm, caller, id string, approve, operator bool) (protocol.RecoveryRecord, []string, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return protocol.RecoveryRecord{}, nil, e
	}
	defer tx.Rollback()
	v, e := recoveryIn(tx, id)
	if e != nil {
		return v, nil, e
	}
	p := v.Successor
	if p.Realm != realm {
		return v, nil, errRecovery
	}
	actorKey, actorPerson := "", ""
	if !operator {
		a, e := agentIn(tx, caller)
		if e != nil {
			return v, nil, e
		}
		if !a.Admin || a.Revoked || a.Pending || a.Person == p.OldPerson || a.Person == p.NewPerson {
			return v, nil, errRecovery
		}
		// A detached historical device is still the recovering person;
		// clearing person_id must not enable self approval.
		var ownHistory int
		if e = tx.QueryRow(`SELECT count(*) FROM person_chain pc,json_each(pc.record,'$.devices') d WHERE pc.person IN (?,?) AND json_extract(d.value,'$.address')=?`, p.OldPerson, p.NewPerson, caller).Scan(&ownHistory); e != nil {
			return v, nil, e
		}
		if ownHistory != 0 {
			return v, nil, errRecovery
		}
		actorKey = a.Public.Fingerprint()
		actorPerson = a.Person
	}
	if v.Status == "committed" && approve {
		return v, nil, tx.Commit()
	}
	if v.Status == "denied" && !approve {
		return v, nil, tx.Commit()
	}
	if v.Status != "pending" {
		return v, nil, errRecovery
	}
	if !approve {
		v.Status = "denied"
	} else {
		v.Status = "committed"
		if e = validateRecovery(tx, realm, p); e != nil {
			return v, nil, e
		}
	}
	addresses := []string{}
	if approve {
		// Include historical roster devices and detached old keys; exclude every
		// exact successor association even when both people share a Google label.
		rows, e := tx.Query(`SELECT address FROM agents WHERE revoked_at IS NULL AND (person_id=? OR pending_person=? OR (label=? AND COALESCE(person_id,'')='' AND COALESCE(pending_person,'')='') OR address IN (SELECT json_extract(d.value,'$.address') FROM person_chain pc,json_each(pc.record,'$.devices') d WHERE pc.person=?)) AND COALESCE(person_id,'')!=? AND COALESCE(pending_person,'')!=?`, p.OldPerson, p.OldPerson, protocol.GoogleLabel(p.Email), p.OldPerson, p.NewPerson, p.NewPerson)
		if e != nil {
			return v, nil, e
		}
		for rows.Next() {
			var address string
			if e = rows.Scan(&address); e != nil {
				rows.Close()
				return v, nil, e
			}
			addresses = append(addresses, address)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return v, nil, e
		}
		for _, address := range addresses {
			if e = revokeDeviceAdminGrants(tx, address, caller); e != nil {
				return v, nil, e
			}
			if _, e = tx.Exec(`UPDATE agents SET revoked_at=?,revoked_reason='removed' WHERE address=?`, time.Now().Unix(), address); e != nil {
				return v, nil, e
			}
			if e = settleRevokedDevice(tx, address); e != nil {
				return v, nil, e
			}
		}
		if _, e = tx.Exec(`UPDATE invites SET expires_at=0 WHERE person=? AND used_by IS NULL`, p.OldPerson); e != nil {
			return v, nil, e
		}
		var subject string
		if e = tx.QueryRow(`SELECT subject FROM person_recoveries WHERE id=?`, id).Scan(&subject); e != nil {
			return v, nil, e
		}
		var bound string
		e = tx.QueryRow(`SELECT person FROM google_people WHERE email=?`, p.Email).Scan(&bound)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return v, nil, e
		}
		if bound != "" && bound != p.OldPerson && bound != p.NewPerson {
			return v, nil, errRecovery
		}
		if _, e = tx.Exec(`INSERT INTO google_people(email,subject,person) VALUES(?,?,?) ON CONFLICT(email) DO UPDATE SET subject=excluded.subject,person=excluded.person`, p.Email, subject, p.NewPerson); e != nil {
			return v, nil, e
		}
		if _, e = tx.Exec(`INSERT INTO google_emails(email,admin,denied) VALUES(?,0,0) ON CONFLICT(email) DO UPDATE SET denied=0`, p.Email); e != nil {
			return v, nil, e
		}
	}
	v.Actor = caller
	v.ActorKey, v.ActorPerson = actorKey, actorPerson
	if operator {
		v.Actor = "hub-operator"
	}
	v.Decided = time.Now().Unix()
	if _, e = tx.Exec(`UPDATE person_recoveries SET status=?,actor=?,actor_key=?,actor_person=?,decided=? WHERE id=?`, v.Status, v.Actor, v.ActorKey, v.ActorPerson, v.Decided, id); e != nil {
		return v, nil, e
	}
	return v, addresses, tx.Commit()
}
func (h *Hub) handleRecoveryRequest(w http.ResponseWriter, r *http.Request) {
	caller, b, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.RecoveryRequest
	if decodeStrict(b, &req) != nil || req.Google.Verify() != nil {
		writeError(w, 400, "", "invalid recovery request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	c, e := h.google.Verify(ctx, req.Google.IDToken, protocol.GoogleNonce(req.Google.Public))
	if e != nil {
		writeError(w, 401, "", "fresh Google sign-in required")
		return
	}
	v, e := h.store.requestRecovery(h.realmID, caller, c, req)
	if e != nil {
		writeError(w, 409, "", "recovery refused")
		return
	}
	h.lookAgain() // Pending requests also need a push when membership is unchanged.
	writeJSON(w, 201, v)
}
func (h *Hub) handleRecoveryList(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	h.writeRecoveries(w, "status='pending'", nil)
}
func (h *Hub) handleRecoveryFetch(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	person := r.PathValue("person")
	if !protocol.ValidID(person) {
		writeError(w, 400, "", "exact person id required")
		return
	}
	h.writeRecoveries(w, "status='committed' AND (old_person=? OR new_person=?)", []any{person, person})
}
func (h *Hub) writeRecoveries(w http.ResponseWriter, where string, args []any) {
	rows, e := h.store.db.Query(`SELECT id,record,status,actor,actor_key,actor_person,decided FROM person_recoveries WHERE `+where+` ORDER BY id`, args...)
	if e != nil {
		writeError(w, 500, "", "storage error")
		return
	}
	defer rows.Close()
	out := []protocol.RecoveryRecord{}
	for rows.Next() {
		var v protocol.RecoveryRecord
		var raw string
		if e = rows.Scan(&v.ID, &raw, &v.Status, &v.Actor, &v.ActorKey, &v.ActorPerson, &v.Decided); e != nil {
			break
		}
		if e = json.Unmarshal([]byte(raw), &v.Successor); e != nil {
			break
		}
		out = append(out, v)
	}
	if e != nil || rows.Err() != nil {
		writeError(w, 500, "", "storage error")
		return
	}
	writeJSON(w, 200, out)
}
func (h *Hub) handleRecoveryDecision(w http.ResponseWriter, r *http.Request) {
	caller, _, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	if action != "approve" && action != "deny" {
		writeError(w, 400, "", "approve or deny required")
		return
	}
	v, addresses, e := h.store.decideRecovery(h.realmID, caller, r.PathValue("id"), action == "approve", false)
	if e != nil {
		writeError(w, 409, "", "recovery refused")
		return
	}
	for _, a := range addresses {
		h.streams.disconnect(a)
		h.presence.drop(a)
	}
	h.lookAgain()
	h.waiters.notifyAll()
	writeJSON(w, 200, v)
}

// ApproveRecovery is an offline host operation, under OpenMaintenance's lock.
func (m *Maintenance) ApproveRecovery(id string) (protocol.RecoveryRecord, error) {
	var realm string
	if e := m.store.db.QueryRow(`SELECT realm_id FROM realm WHERE id=1 AND initialized=1`).Scan(&realm); e != nil {
		return protocol.RecoveryRecord{}, e
	}
	v, _, e := m.store.decideRecovery(realm, "hub-operator", id, true, true)
	return v, e
}
