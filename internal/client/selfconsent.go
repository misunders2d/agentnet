package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// Self-invite consent (owner decision D3, docs/plans/ROOM_V1.md §5): an
// invite of this person's own agent here, signed by a device this host
// trusts, is accepted without the person's click (selfConsent,
// participation.go). Other people's agents still need their owner's accept.
//
// The trust set is this host device plus the own devices the person added,
// each by its exact key, under the config key self_consent. Only the local
// person changes it; nothing received does. A browser device must never be
// in it: its code comes from the relay, and a browser key in the trust set
// would turn that code into execution here (review X4). Nothing in a link
// request tells a browser from a computer running agentnet, so a device is
// added only when the person says it is the latter, while approving its
// pending link request here with agentnet person approve --native ID
// (ApproveNativeLink). Nothing else adds one (not person approve ID, not
// an approval on the page or on another device, not a request approved
// already), and nothing is meant to trust a browser: --native said of one
// is the mistake the command's output and help warn against. person
// untrust ADDRESS removes a device. A key that changes is no longer trusted.
//
// D3 holds for invites stored here from the first time a build with it
// opened this home (selfConsentSinceKey): earlier ones were made when
// nothing ran before the click, and still wait for it.
//
// Each accept leaves a local notice (agentnet inbox --review, and the
// page's review items with reason "self_consented") until the person
// dismisses it (agentnet resolve PID, or resolve on the page).

const (
	selfConsentKey        = "self_consent"
	selfConsentNoticesKey = "self_consent_notices"
	selfConsentSinceKey   = "self_consent_since" // unix seconds: set once, when Open first runs a build with D3
	maxSelfConsentNotices = 64                   // the oldest notice goes first
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
	Inviter string `json:"inviter"` // the inviting device
	At      int64  `json:"at"`      // unix seconds
}

// SelfConsentTrust lists the trust set: this device first, then the own
// devices the person added.
func (a *Agent) SelfConsentTrust() ([]TrustedDevice, error) {
	added, err := selfConsentAddedIn(a.store.db)
	if err != nil {
		return nil, err
	}
	return append([]TrustedDevice{{Address: a.Address, Fingerprint: a.Self().Fingerprint()}}, added...), nil
}

// UntrustOwnDevice removes the device at address from the trust set; this
// device itself always stays in it.
func (a *Agent) UntrustOwnDevice(address string) error {
	if address == a.Address {
		return errors.New("this device always trusts itself: its own invites are its person's")
	}
	return a.setSelfConsentTrust(func(tx *sql.Tx) error {
		added, err := selfConsentAddedIn(tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(added, func(d TrustedDevice) bool { return d.Address == address })
		if i < 0 {
			return fmt.Errorf("%s is not in the trust set", address)
		}
		return putConfigJSON(tx, selfConsentKey, slices.Delete(added, i, i+1))
	})
}

// markSelfConsentSince records, once, when this home first ran a build
// with D3 (Open calls it): invites stored here before then are not
// accepted without a click.
func (s *store) markSelfConsentSince() error {
	if _, err := s.config(selfConsentSinceKey); !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO config(k, v) VALUES(?, ?)`, selfConsentSinceKey, strconv.FormatInt(time.Now().Unix(), 10))
	return err
}

// invitedSinceSelfConsent reports whether the invite event hash was stored
// here since D3 began here; without that record, it was not.
func (s *store) invitedSinceSelfConsent(hash string) (bool, error) {
	var received int64
	var since sql.NullString
	err := s.db.QueryRow(`SELECT received_at, (SELECT v FROM config WHERE k = ?) FROM participation_events WHERE hash = ?`, selfConsentSinceKey, hash).Scan(&received, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	began, perr := strconv.ParseInt(since.String, 10, 64)
	return since.Valid && perr == nil && received >= began, nil
}

func (a *Agent) setSelfConsentTrust(change func(*sql.Tx) error) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		return err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}

// selfConsentTrusted reports whether the device at address with key
// fingerprint fp is in the trust set (fp alone, with address "": any
// trusted device has that key).
func (a *Agent) selfConsentTrusted(address, fp string) (bool, error) {
	if fp == a.Self().Fingerprint() && (address == "" || address == a.Address) {
		return true, nil
	}
	added, err := selfConsentAddedIn(a.store.db)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(added, func(d TrustedDevice) bool {
		return d.Fingerprint == fp && (address == "" || d.Address == address)
	}), nil
}

func selfConsentAddedIn(q querier) ([]TrustedDevice, error) {
	var out []TrustedDevice
	return out, configJSONIn(q, selfConsentKey, &out)
}

// addSelfConsentTrustIn adds (or, for a new key, replaces) address in the
// trust set within tx.
func addSelfConsentTrustIn(tx *sql.Tx, address, fp string) error {
	added, err := selfConsentAddedIn(tx)
	if err != nil {
		return err
	}
	added = slices.DeleteFunc(added, func(d TrustedDevice) bool { return d.Address == address })
	return putConfigJSON(tx, selfConsentKey, append(added, TrustedDevice{Address: address, Fingerprint: fp}))
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
