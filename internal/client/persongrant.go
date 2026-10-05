package client

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Local decisions only. Membership and keys are read from the current verified
// roster at use time, so a later device needs no copied or received grant.
const personGrantSchema = `CREATE TABLE person_grants(person TEXT PRIMARY KEY, questions INTEGER NOT NULL DEFAULT 0, tasks INTEGER NOT NULL DEFAULT 0, added_at INTEGER NOT NULL);`

const personGrantFor = `EXISTS (SELECT 1 FROM person_grants pg
 JOIN persons ps ON ps.person=pg.person AND ps.state IN ('self','pinned')
 JOIN person_devices pd ON pd.person=ps.person
 JOIN peers pp ON pp.address=pd.address AND pp.pending IS NULL
 JOIN json_each(ps.record,'$.devices') rd ON json_extract(rd.value,'$.address')=pd.address
 WHERE pg.%s=1 AND pd.address=%s AND pd.fingerprint=%s
 AND json_extract(pp.public,'$.sign_key')=json_extract(rd.value,'$.sign_key')
 AND json_extract(pp.public,'$.box_recipient')=json_extract(rd.value,'$.box_recipient'))`

func personGrantHolds(kind, address, key string) string {
	return fmt.Sprintf(personGrantFor, kind, address, key)
}

const deviceQuestionFor = `EXISTS (SELECT 1 FROM approvals qa WHERE qa.address=%s AND NOT EXISTS (SELECT 1 FROM person_devices fd JOIN persons fs ON fs.person=fd.person WHERE fd.address=qa.address AND fs.state='conflict'))`

var questionApprovalHolds = `(` + fmt.Sprintf(deviceQuestionFor, "inbox.sender") + ` OR ` + personGrantHolds("questions", "inbox.sender", "inbox.verified_by") + `)`

func questionApproved(q querier, address, key string) (bool, error) {
	var ok bool
	err := q.QueryRow(`SELECT `+fmt.Sprintf(deviceQuestionFor, "?")+` OR `+personGrantHolds("questions", "?", "?"), address, address, key).Scan(&ok)
	return ok, err
}

// PermissionTarget resolves a person's exact ID or an exact device
// address. A name is only the person's own claim, so it never picks who a
// permission covers: the answer lists the pinned people with that name and
// their IDs to choose from.
func (a *Agent) PermissionTarget(target string) (string, error) {
	if _, _, err := protocol.SplitAddress(target); err == nil {
		return target, nil
	}
	if protocol.ValidID(target) {
		var id string
		err := a.store.db.QueryRow(`SELECT person FROM persons WHERE person=? AND state IN ('self','pinned')`, target).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("no verified person has that ID here")
		}
		return id, err
	}
	rows, err := a.store.db.Query(`SELECT p.person, group_concat(d.address, ', ') FROM persons p LEFT JOIN person_devices d ON d.person=p.person WHERE p.label=? AND p.state IN ('self','pinned') GROUP BY p.person`, target)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var id string
		var devices sql.NullString
		if err = rows.Scan(&id, &devices); err != nil {
			return "", err
		}
		found = append(found, id+" (on "+devices.String+")")
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", errors.New("names are the person's own claim and pick no one: use their verified person ID or an exact device address")
	}
	return "", errors.New("names are the person's own claim and pick no one; choose by ID: " + strings.Join(found, "; "))
}

func setPersonGrant(tx *sql.Tx, person, kind string, allow bool) error {
	if kind != "questions" && kind != "tasks" {
		return errors.New("invalid permission kind")
	}
	var state string
	if err := tx.QueryRow(`SELECT state FROM persons WHERE person=?`, person).Scan(&state); err != nil {
		return err
	}
	if allow && state != personSelf && state != personPinned {
		return errPersonConflict
	}
	if _, err := tx.Exec(`INSERT INTO person_grants(person,`+kind+`,added_at) VALUES(?,?,?) ON CONFLICT(person) DO UPDATE SET `+kind+`=excluded.`+kind+`,added_at=excluded.added_at`, person, allow, time.Now().Unix()); err != nil {
		return err
	}
	if !allow {
		_, err := tx.Exec(`DELETE FROM person_grants WHERE person=? AND questions=0 AND tasks=0`, person)
		return err
	}
	return nil
}

// demotePersonJobs reconciles only pending automatic work. A local one-time
// acceptance stays a separate decision; no old held task is silently revived.
func demotePersonJobs(tx *sql.Tx) error {
	if _, err := tx.Exec(`UPDATE inbox SET state=?,detail='automatic question permission ended; accept it once',notified=0,review_sent=0 WHERE conv IS NULL AND kind='question' AND state=? AND NOT `+questionApprovalHolds, stateHeld, statePending); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE inbox SET state=?,detail='standing task permission ended; accept it once',notified=0,review_sent=0 WHERE conv IS NULL AND kind='task' AND state=? AND NOT (`+taskGrantHolds+` OR `+ownTaskHolds+`)`, stateAwaiting, statePending); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM reported WHERE item IN (SELECT id FROM inbox WHERE state IN (?,?) AND review_sent=0)`, stateHeld, stateAwaiting)
	return err
}

// PersonGrants reports the saved local decisions, including a frozen person so
// its grant can still be revoked. A frozen roster never authorizes execution.
func (a *Agent) PersonGrants() ([]PersonGrant, error) {
	rows, err := a.store.db.Query(`SELECT g.person,p.label,p.state,g.questions,g.tasks FROM person_grants g JOIN persons p ON p.person=g.person ORDER BY p.label,g.person`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonGrant
	for rows.Next() {
		var g PersonGrant
		if err = rows.Scan(&g.Person, &g.Label, &g.State, &g.Questions, &g.Tasks); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

type PersonGrant struct {
	Person, Label, State string
	Questions, Tasks     bool
}

// PersonPermissionTarget is the verified person behind an exact currently
// pinned sender key. It never uses a listed/display-only roster.
func (a *Agent) PersonPermissionTarget(address, key string) (string, error) {
	return permissionPersonIn(a.store.db, address, key)
}

func permissionPersonIn(q querier, address, key string) (string, error) {
	var id string
	err := q.QueryRow(`SELECT d.person FROM person_devices d JOIN persons p ON p.person=d.person JOIN peers k ON k.address=d.address AND k.pending IS NULL JOIN json_each(p.record,'$.devices') r ON json_extract(r.value,'$.address')=d.address WHERE d.address=? AND d.fingerprint=? AND p.state IN ('self','pinned') AND json_extract(k.public,'$.sign_key')=json_extract(r.value,'$.sign_key') AND json_extract(k.public,'$.box_recipient')=json_extract(r.value,'$.box_recipient')`, address, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// Removal/key/conflict ends access even during a run admitted by this person's
// grant. Ordinary grant revocation retains the existing may-finish behavior.
func (a *Agent) personGrantStop(j job) string {
	if j.PermissionPerson == "" {
		return ""
	}
	person, err := permissionPersonIn(a.store.db, j.From, j.Key)
	if err != nil || person != j.PermissionPerson {
		return "requester's current verified person or device key changed"
	}
	return ""
}

// PermissionState is the effective local permission for this current peer key.
func (a *Agent) PermissionState(address, key string) (questions, tasks bool, err error) {
	questions, err = questionApproved(a.store.db, address, key)
	if err != nil {
		return
	}
	tasks, err = taskGranted(a.store.db, address, key)
	return
}

func isPersonTarget(s string) bool { return protocol.ValidID(strings.TrimSpace(s)) }

// PermissionLabel is a local display label only, never execution authority.
func (a *Agent) PermissionLabel(target string) string {
	if isPersonTarget(target) {
		var name string
		if a.store.db.QueryRow(`SELECT label FROM persons WHERE person=?`, target).Scan(&name) == nil {
			return name
		}
	}
	return DeviceWords(target)
}

// PersonGrantForPeer returns the person grant that actually holds for this
// exact peer key, separately from any advanced explicit device grant.
func (a *Agent) PersonGrantForPeer(address, key string) (person string, questions, tasks bool, err error) {
	person, err = a.PersonPermissionTarget(address, key)
	if err != nil || person == "" {
		return
	}
	err = a.store.db.QueryRow(`SELECT questions,tasks FROM person_grants WHERE person=?`, person).Scan(&questions, &tasks)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

// coveredByPerson refuses to report a device revocation as done when the
// device had no grant of its own and a person grant of that kind still
// covers it: the person's ID is what stops it.
func coveredByPerson(q querier, address, kind string) error {
	if kind != "questions" && kind != "tasks" {
		return errors.New("invalid permission kind")
	}
	var person, label string
	err := q.QueryRow(`SELECT pg.person, ps.label FROM person_grants pg JOIN persons ps ON ps.person=pg.person JOIN person_devices pd ON pd.person=pg.person WHERE pd.address=? AND pg.`+kind+`=1`, address).Scan(&person, &label)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	flag := ""
	if kind == "tasks" {
		flag = "--tasks "
	}
	return fmt.Errorf("%s is covered by the permission for %s (person %s); stop it with: agentnet unapprove %s%s", address, strconv.Quote(label), person, flag, person)
}
