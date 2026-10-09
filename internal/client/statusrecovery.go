package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const statusRecoveryScan = "conversation-status-order-invalid-recovery-v1"
const statusRecoveryCarrier = statusRecoveryScan + "/"

var errStatusRecoveryAuthority = errors.New("status executor pin changed or is pending")

func statusSenderCheck(q dbq, sender identity.Public) error {
	current, found, err := pinnedKey(q, sender.Address)
	if err != nil {
		return err
	}
	var pending int
	if err = q.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, sender.Address).Scan(&pending); err != nil {
		return err
	}
	if !found || pending != 0 || !sameKeys(current, sender) {
		return errStatusRecoveryAuthority
	}
	return nil
}

// Retain the verified executor key through proof waits and the admission
// transaction. An upgrade never turns an old changed-key control into consent.
func statusRecoveryCheck(q dbq, sender, id string) error {
	var raw string
	err := q.QueryRow(`SELECT v FROM config WHERE k=?`, statusRecoveryCarrier+id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var original identity.Public
	if json.Unmarshal([]byte(raw), &original) != nil || original.Address != sender {
		return errStatusRecoveryAuthority
	}
	return statusSenderCheck(q, original)
}

// Earlier status admission treated missing conversation/request proof as
// invalid. Reconsider only signed inert statuses once, locally and in pages;
// normal proof retry still performs full admission and never runs a request.
func (a *Agent) recoverInvalidStatuses() error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done string
	err = tx.QueryRow(`SELECT v FROM config WHERE k=?`, statusRecoveryScan).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	type held struct{ id, sender, raw string }
	for after := ""; ; {
		rows, err := tx.Query(`SELECT id,sender,envelope FROM quarantine WHERE reason=? AND id>? ORDER BY id LIMIT ?`, reasonInvalid, after, proofPage)
		if err != nil {
			return err
		}
		var page []held
		for rows.Next() {
			var h held
			if err = rows.Scan(&h.id, &h.sender, &h.raw); err != nil {
				rows.Close()
				return err
			}
			page = append(page, h)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].id
		for _, h := range page {
			var env envelope.Envelope
			if json.Unmarshal([]byte(h.raw), &env) != nil || env.V != envelope.Version3 || env.ID != h.id || env.From != h.sender || env.To != a.Address {
				continue
			}
			sender, found, err := pinnedKey(tx, env.From)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			var pending int
			if err = tx.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, env.From).Scan(&pending); err != nil {
				return err
			}
			if pending != 0 {
				continue
			}
			in, err := envelope.Open(env, a.id, a.Address, sender)
			if err != nil || in.Sub != envelope.SubStatus || in.Conv == "" || in.Ref == nil {
				continue
			}
			var status envelope.Status
			if json.Unmarshal([]byte(in.Body), &status) != nil || status.Decision != "" {
				continue
			}
			root, _, found, err := conversationIn(tx, in.Conv)
			if err != nil {
				return err
			}
			if found {
				if root.Kind != protocol.ConvKindGroup && root.Kind != protocol.ConvKindDM {
					continue
				}
				err = a.groupControlTarget(tx, in)
				if err != nil && !errors.Is(err, ErrGroupContextPending) {
					continue
				}
				if err == nil {
					if ok, _ := a.statusAllowedIn(tx, in, env.From, sender.Fingerprint()); !ok {
						continue
					}
					if root.Kind == protocol.ConvKindGroup {
						if _, err = a.groupStatusScope(tx, ControlRef{Conv: in.Conv, ID: in.Ref.ID, Fingerprint: in.Ref.Fingerprint}, env.From, sender.Fingerprint()); err != nil && !errors.Is(err, ErrGroupContextPending) {
							continue
						}
					} else {
						members, err := membersIn(tx, in.Conv)
						if err != nil {
							return err
						}
						if !members.device(env.From, sender.Fingerprint()) || !members.device(a.Address, a.Self().Fingerprint()) {
							continue
						}
					}
				}
			}
			raw, err := json.Marshal(sender)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?,?) ON CONFLICT(k) DO NOTHING`, statusRecoveryCarrier+env.ID, string(raw)); err != nil {
				return err
			}
			if _, err = tx.Exec(`UPDATE quarantine SET reason=? WHERE id=? AND reason=?`, reasonProof, env.ID, reasonInvalid); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?, '1')`, statusRecoveryScan); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.convWork.due(convRetry)
	return nil
}
