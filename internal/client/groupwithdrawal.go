package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (a *Agent) latestGroupWithdrawal(ctx context.Context, w protocol.GroupWithdrawal) error {
	if !protocol.ValidHash(w.Conv) || !protocol.ValidID(w.Realm) || !protocol.ValidID(w.Person) || !protocol.ValidHash(w.Admission) || !protocol.ValidHash(w.Roster) || !protocol.ValidFingerprint(w.By) {
		return errors.New("group: invalid withdrawal binding")
	}
	if _, err := a.refreshPerson(ctx, w.Person, false); err != nil {
		return err
	}
	person, ok, err := a.store.personByID(w.Person)
	if err != nil {
		return err
	}
	if !ok || person.info.State == personConflict || person.roster.Hash() != w.Roster {
		return errors.New("group: withdrawal signing roster is not latest locally pinned roster")
	}
	device, ok := person.roster.Device(w.By)
	if !ok || !ed25519.Verify(device.SignKey, w.Canonical(), w.Sig) {
		return errors.New("group: withdrawal device signature invalid or removed")
	}
	return nil
}

func groupWithdrawalRows(db *sql.DB, table, conv string) ([]protocol.GroupWithdrawal, error) {
	// Only these private constant table names are supplied by callers.
	rows, err := db.Query("SELECT record FROM "+table+" WHERE conv=?", conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []protocol.GroupWithdrawal
	for rows.Next() {
		var raw []byte
		var w protocol.GroupWithdrawal
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &w); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

func sameGroupWithdrawal(a, b protocol.GroupWithdrawal) bool {
	return bytes.Equal(a.Canonical(), b.Canonical()) && bytes.Equal(a.Sig, b.Sig)
}

func pinGroupWithdrawal(tx *sql.Tx, w protocol.GroupWithdrawal) error {
	raw, _ := json.Marshal(w)
	if _, err := tx.Exec("INSERT INTO group_withdrawals(conv,person,admission,record)VALUES(?,?,?,?) ON CONFLICT(conv,person,admission)DO NOTHING", w.Conv, w.Person, w.Admission, raw); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?", w.Conv, w.Person, w.Admission)
	return err
}

func (a *Agent) deferGroupWithdrawal(w protocol.GroupWithdrawal) error {
	raw, _ := json.Marshal(w)
	_, err := a.store.db.Exec("INSERT INTO group_pending_withdrawals(conv,person,admission,record)VALUES(?,?,?,?) ON CONFLICT(conv,person,admission)DO NOTHING", w.Conv, w.Person, w.Admission, raw)
	return err
}

func (a *Agent) groupPendingForState(ctx context.Context, state protocol.GroupState, resolve protocol.GroupRosterResolver) ([]protocol.GroupWithdrawal, error) {
	pending, err := groupWithdrawalRows(a.store.db, "group_pending_withdrawals", state.Conv)
	if err != nil {
		return nil, err
	}
	var applicable []protocol.GroupWithdrawal
	for _, w := range pending {
		m, ok := state.Member(w.Person)
		if !ok || m.Admission.Hash() != w.Admission {
			continue
		}
		if m.Admin {
			return nil, errors.New("group: withdrawal conflicts with promoted admin; explicit authority/context resolution required")
		}
		if err = a.latestGroupWithdrawal(ctx, w); err != nil {
			return nil, err
		}
		roster, ok, e := a.store.chainStep(w.Person, w.Roster)
		if e != nil {
			return nil, e
		}
		if !ok {
			return nil, ErrGroupContextPending
		}
		withdrawalResolve := func(person, hash string) (protocol.PersonRoster, bool) {
			if person == w.Person && hash == w.Roster {
				return roster, true
			}
			return resolve(person, hash)
		}
		if err = w.Verify(state, withdrawalResolve); err != nil {
			return nil, err
		}
		applicable = append(applicable, w)
	}
	return applicable, nil
}

func groupWithdrawalPinned(pins []protocol.GroupWithdrawal, w protocol.GroupWithdrawal) bool {
	for _, pin := range pins {
		if sameGroupWithdrawal(pin, w) {
			return true
		}
	}
	return false
}

func currentGroupWithdrawals(state protocol.GroupState, records []protocol.GroupWithdrawal) []protocol.GroupWithdrawal {
	var result []protocol.GroupWithdrawal
	for _, w := range records {
		m, ok := state.Member(w.Person)
		if ok && !m.Admin && m.Admission.Hash() == w.Admission && !groupWithdrawalPinned(result, w) {
			result = append(result, w)
		}
	}
	return result
}

// The same immediate transaction installs current membership and accepted
// withdrawal pins. A competing promotion cannot turn ordinary leave into admin leave.
func installGroupMembership(tx *sql.Tx, packet GroupContext, withdrawals []protocol.GroupWithdrawal) error {
	if err := checkGroupWithdrawalPromotion(tx, packet, withdrawals); err != nil {
		return err
	}
	for _, w := range withdrawals {
		if m, ok := packet.State.Member(w.Person); ok && !m.Admin && m.Admission.Hash() == w.Admission {
			if err := pinGroupWithdrawal(tx, w); err != nil {
				return err
			}
		}
	}
	return installGroupContext(tx, packet)
}

// Shared current/previous-role guard; visitor storage reuses it without member installation.
func checkGroupWithdrawalPromotion(q dbq, packet GroupContext, withdrawals []protocol.GroupWithdrawal) error {
	var raw []byte
	var old GroupContext
	err := q.QueryRow("SELECT payload FROM group_context WHERE conv=?", packet.State.Conv).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal(raw, &old); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, w := range withdrawals {
		for _, state := range []protocol.GroupState{old.State, packet.State} {
			if m, ok := state.Member(w.Person); ok && m.Admission.Hash() == w.Admission && m.Admin {
				return errors.New("group: withdrawal conflicts with promoted admin; explicit authority/context resolution required")
			}
		}
	}
	return nil
}
