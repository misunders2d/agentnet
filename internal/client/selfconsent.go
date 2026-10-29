package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Owner rule, 2026-10-12 (supersedes D3): a current verified human device of
// this host's person speaks for the owner, on phones, browsers and computers.
// Its invitations, questions and tasks need no second AgentNet approval.
// Native harness permissions still apply. The old native-device trust list is
// retained in stored data without new reads or writes; it no
// longer supplies authority. Current roster, human role, exact pin, pending-key
// and revocation checks are shared with own-human questions (persongrant.go).

const (
	selfConsentNoticesKey = "self_consent_notices"
	maxSelfConsentNotices = 64 // the oldest notice goes first
)

// TrustedDevice is one own device whose invites of this person's agents
// here are accepted without a click.
type TrustedDevice struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// SelfConsentNotice says that this device accepted an invite of its
// person's own agent without a click.
type SelfConsentNotice struct {
	PID     string `json:"pid"`
	Conv    string `json:"conv"`
	AgentID string `json:"agent_id,omitempty"`
	Inviter string `json:"inviter"`           // the inviting device
	At      int64  `json:"at"`                // unix seconds
	Granted bool   `json:"granted,omitempty"` // another person this one approved (grantConsent)
}

// OwnHumanDevices lists the current verified own human devices, host first.
func (a *Agent) OwnHumanDevices() ([]TrustedDevice, error) {
	rows, err := a.store.db.Query(`SELECT candidate.address,candidate.fingerprint FROM person_devices candidate
 WHERE candidate.address<>? AND `+ownHumanQuestionHolds("candidate.address", "candidate.fingerprint")+` ORDER BY candidate.address`, a.Address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []TrustedDevice
	for rows.Next() {
		var d TrustedDevice
		if err := rows.Scan(&d.Address, &d.Fingerprint); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	local, err := a.ownHumanAuthor(a.store.db, a.Address, a.Self().Fingerprint())
	if err != nil {
		return nil, err
	}
	if local {
		devices = append([]TrustedDevice{{Address: a.Address, Fingerprint: a.Self().Fingerprint()}}, devices...)
	}
	return devices, nil
}

// OwnHumanPermissions reports effective authority, including phones, without a separate grant.
func (a *Agent) OwnHumanPermissions() ([]Grant, error) {
	devices, err := a.OwnHumanDevices()
	if err != nil {
		return nil, err
	}
	grants := make([]Grant, 0, len(devices))
	for _, d := range devices {
		grants = append(grants, Grant{Address: d.Address, Fingerprint: d.Fingerprint, Status: "active"})
	}
	return grants, nil
}

var ownTaskHolds = ownHumanQuestionHolds("inbox.sender", "inbox.verified_by")

// ownDeviceHolds uses the same verified human authority for tasks and invites.
func ownDeviceHolds(q querier, address, fp string) (bool, error) {
	if fp == "" {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(`SELECT `+ownHumanQuestionHolds("?", "?"), address, fp, address, fp).Scan(&ok)
	return ok, err
}

// The local identity is held by this process, not a peer pin. Its human role
// and current membership still come from the same verified own-person roster.
func (a *Agent) ownHumanAuthor(q dbq, address, fp string) (bool, error) {
	if address != a.Address || fp != a.Self().Fingerprint() {
		return ownDeviceHolds(q, address, fp)
	}
	p, found, err := personBySelfIn(q, address)
	if err != nil || !found || p.info.State != personSelf || !p.has(address, fp) || !p.roster.Human(fp) {
		return false, err
	}
	var refused bool
	err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM config WHERE k=? AND v=?) OR EXISTS(SELECT 1 FROM peers WHERE address=? AND pending IS NOT NULL)`, "revoked-exact-device/"+address+"/"+fp, address, address).Scan(&refused)
	return !refused, err
}

// demoteLapsedOwnTasks moves the device-thread tasks waiting to run here
// for which neither the own-device rule nor a task grant (taskgrant.go)
// holds any more back to awaiting the person, saying why, so none is left
// pending where the worker no longer claims it and the person cannot see
// it. A task the person accepted is not pending (it is accepted) and stays.
func demoteLapsedOwnTasks(tx *sql.Tx, why string) error {
	rows, err := tx.Query(`UPDATE inbox SET state = ?, detail = ?, notified = 0, review_sent = 0
		WHERE kind = ? AND state = ? AND conv IS NULL AND NOT `+taskGrantHolds+` AND NOT `+ownTaskHolds+` AND NOT `+ownProposalHolds+` RETURNING id`,
		stateAwaiting, "not run without asking: "+why+"; accept ID runs it once", envelope.KindTask, statePending)
	if err != nil {
		return err
	}
	var ids []any
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return err
	}
	// Back in review: reported afresh to each recipient.
	_, err = tx.Exec(`DELETE FROM reported WHERE item IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, ids...)
	return err
}

// SelfConsentNotices lists the auto-accepts the person has not dismissed,
// oldest first.
func (a *Agent) SelfConsentNotices() ([]SelfConsentNotice, error) {
	var out []SelfConsentNotice
	return out, configJSONIn(a.store.db, selfConsentNoticesKey, &out)
}

// addSelfConsentNoticeIn records n within tx, the accept's transaction.
func addSelfConsentNoticeIn(tx *sql.Tx, n SelfConsentNotice) error {
	var notices []SelfConsentNotice
	if err := configJSONIn(tx, selfConsentNoticesKey, &notices); err != nil {
		return err
	}
	notices = append(notices, n)
	if len(notices) > maxSelfConsentNotices {
		notices = notices[len(notices)-maxSelfConsentNotices:]
	}
	return putConfigJSON(tx, selfConsentNoticesKey, notices)
}

// dismissSelfConsentNotice removes pid's notice; it reports whether there
// was one.
func (a *Agent) dismissSelfConsentNotice(pid string) (bool, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var notices []SelfConsentNotice
	if err := configJSONIn(tx, selfConsentNoticesKey, &notices); err != nil {
		return false, err
	}
	i := slices.IndexFunc(notices, func(n SelfConsentNotice) bool { return n.PID == pid })
	if i < 0 {
		return false, nil
	}
	if err := putConfigJSON(tx, selfConsentNoticesKey, slices.Delete(notices, i, i+1)); err != nil {
		return false, err
	}
	return true, a.store.done(tx.Commit())
}

// configJSONIn decodes the config value k into v; a missing key leaves v.
func configJSONIn(q querier, k string, v any) error {
	var raw string
	err := q.QueryRow(`SELECT v FROM config WHERE k = ?`, k).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return fmt.Errorf("corrupt %s configuration", k)
	}
	return nil
}

func putConfigJSON(tx *sql.Tx, k string, v any) error {
	raw, _ := json.Marshal(v)
	_, err := tx.Exec(`INSERT OR REPLACE INTO config(k, v) VALUES(?, ?)`, k, string(raw))
	return err
}
