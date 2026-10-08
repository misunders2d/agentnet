package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const historyRecoveryScan = "group-history-invalid-recovery-v1"
const historyRecoveryCarrier = historyRecoveryScan + "/"

var errHistoryRecoveryAuthority = errors.New("group history recovery requires current own human devices and unchanged pins")

// The upgrade may reconsider only inert group participation/control replicas
// from a current own human to a current own human. Keep the exact device keys
// with each carrier while missing context/proof is resolved by normal ingress.
type historyRecoveryDevices struct {
	Sender identity.Public `json:"sender"`
	Reader identity.Public `json:"reader"`
}

func historyRecoveryCurrent(q dbq, devices ...identity.Public) error {
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if !ok || !me.has(device.Address, device.Fingerprint()) || !me.roster.Human(device.Fingerprint()) {
			return errHistoryRecoveryAuthority
		}
		pinned, found, err := pinnedKey(q, device.Address)
		if err != nil {
			return err
		}
		if found && !sameKeys(pinned, device) {
			return errHistoryRecoveryAuthority
		}
		var pending int
		if err := q.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, device.Address).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return errHistoryRecoveryAuthority
		}
	}
	return nil
}

func (d historyRecoveryDevices) check(q dbq) error {
	if err := historyRecoveryCurrent(q, d.Sender, d.Reader); err != nil {
		return err
	}
	// The reader's own key need not also appear in peers; the sender's pin
	// is mandatory. historyRecoveryCurrent already checked its exact keys.
	_, found, err := pinnedKey(q, d.Sender.Address)
	if err != nil {
		return err
	}
	if !found {
		return errHistoryRecoveryAuthority
	}
	return nil
}

// recoverInvalidGroupHistory performs a one-time, local-only upgrade scan.
// v0.8.6 could persist legitimate history after an assistant's dismissal as
// invalid. Never retry live work or arbitrary invalid envelopes. Only verified
// encrypted replicas enter the existing bounded proof queue; all current
// history admission checks still run there, with no grant/cursor changes.
func (a *Agent) recoverInvalidGroupHistory() error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done string
	err = tx.QueryRow(`SELECT v FROM config WHERE k=?`, historyRecoveryScan).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Opening before linking (or while own authority is unavailable) must
	// not consume the upgrade. This snapshot and the completion marker share
	// one transaction: a concurrent roster/pin change aborts the whole scan.
	if err := historyRecoveryCurrent(tx, a.Self()); err != nil {
		if errors.Is(err, errHistoryRecoveryAuthority) {
			return nil
		}
		return err
	}
	rows, err := tx.Query(`SELECT id,sender,envelope FROM quarantine WHERE reason=?`, reasonInvalid)
	if err != nil {
		return err
	}
	type held struct{ id, sender, raw string }
	var candidates []held
	for rows.Next() {
		var h held
		if err = rows.Scan(&h.id, &h.sender, &h.raw); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, h := range candidates {
		var env envelope.Envelope
		if json.Unmarshal([]byte(h.raw), &env) != nil || env.ID != h.id || env.From != h.sender || env.V != envelope.Version2 {
			continue
		}
		sender, found, err := pinnedKey(tx, env.From)
		if err != nil {
			return err
		}
		devices := historyRecoveryDevices{Sender: sender, Reader: a.Self()}
		if !found {
			continue
		}
		if err = devices.check(tx); err != nil {
			if errors.Is(err, errHistoryRecoveryAuthority) {
				continue
			}
			return err
		}
		in, err := envelope.Open(env, a.id, a.Address, sender)
		if err != nil || in.Sub != envelope.SubHistory || !in.Replica || in.Kind != envelope.KindMessage || in.PID != "" || in.Human != nil || len(in.Attachments) != 0 {
			continue
		}
		root, err := protocol.ParseConvRoot(in.Root)
		if err != nil || root.Kind != protocol.ConvKindGroup || root.ID() != in.Conv {
			continue
		}
		var item HistoryItem
		if decodeGroupCarrierJSON([]byte(in.Body), &item) != nil || item.V != 1 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) || !protocol.ValidFingerprint(item.FromKey) || item.PID == "" && item.Ref == nil {
			continue
		}
		raw, err := json.Marshal(devices)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?,?)`, historyRecoveryCarrier+env.ID, string(raw)); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE quarantine SET reason=? WHERE id=? AND reason=?`, reasonProof, env.ID, reasonInvalid); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?, '1')`, historyRecoveryScan); err != nil {
		return err
	}
	return a.store.done(tx.Commit())
}

// historyRecoveryCheck survives proof_pending, restarts and retries. Admission
// calls this inside its insertion transaction, before deduplication as well.
func historyRecoveryCheck(q dbq, sender, carrier string) error {
	var raw string
	err := q.QueryRow(`SELECT v FROM config WHERE k=?`, historyRecoveryCarrier+carrier).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var devices historyRecoveryDevices
	if json.Unmarshal([]byte(raw), &devices) != nil || devices.Sender.Address != sender {
		return errors.New("group history recovery carrier does not match its devices")
	}
	return devices.check(q)
}
