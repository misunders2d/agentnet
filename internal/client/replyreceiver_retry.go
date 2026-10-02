package client

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// The encrypted original cannot be reopened by its sender on retry. Its
// local frozen batch is the exact route proof; current authority is rechecked.
func receiverOriginCopy(q dbq, id string) (*ReplyReceiverBinding, int, error) {
	rows, err := q.Query(`SELECT id,receiver FROM reply_receivers`)
	if err != nil {
		return nil, -1, err
	}
	var found *ReplyReceiverBinding
	index := -1
	for rows.Next() {
		var b ReplyReceiverBinding
		var raw string
		if err = rows.Scan(&b.ID, &raw); err != nil {
			break
		}
		if err = decodeReceiverBinding(raw, &b); err != nil {
			break
		}
		if b.remote == nil || b.remote.Role != "origin" {
			continue
		}
		for i, original := range b.remote.OriginalIDs {
			if original != id {
				continue
			}
			if found != nil {
				err = errors.New("ambiguous frozen receiver original copy")
				break
			}
			copy := b
			found, index = &copy, i
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return found, index, err
}

func (a *Agent) receiverOriginalMayDeliver(env envelope.Envelope) (bool, error) {
	b, index, err := receiverOriginCopy(a.store.db, env.ID)
	if err != nil || b == nil {
		return b == nil && err == nil, err
	}
	binding, err := replyReceiverIn(a.store.db, b.ID)
	if err != nil {
		return false, err
	}
	r := binding.remote
	var state, raw string
	if err = a.store.db.QueryRow(`SELECT state,envelope FROM outbox WHERE id=?`, env.ID).Scan(&state, &raw); err != nil {
		return false, err
	}
	if state != stateQueued {
		return false, nil
	}
	why := ""
	switch {
	case binding.State == "canceled" || receiverRetracted(a.store.db, r):
		why = "selected receiver delegation was canceled or its original deleted"
	case !r.Ready:
		why = "selected receiver has not authorized this original"
	case index >= len(r.OriginalHashes) || fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) != r.OriginalHashes[index]:
		why = "sealed original differs from its frozen receiver commitment"
	default:
		if err = groupReceiverBinding(a.store.db, binding); err != nil {
			why = err.Error()
		}
	}
	if why == "" {
		return true, nil
	}
	return false, a.store.setOutboxState(env.ID, stateNotDelivered, why, "")
}

func receiverCopyNeedsCapability(q dbq, id, sub, body string) (bool, error) {
	b, _, err := receiverOriginCopy(q, id)
	if err != nil || b != nil {
		return b != nil, err
	}
	if sub == envelope.SubHistory {
		// Legacy non-group history kept no local body. Newly routed history
		// retains its typed body at insertion so offline retries can require rcv1.
		if body == "" {
			return false, nil
		}
		var h HistoryItem
		if err := json.Unmarshal([]byte(body), &h); err != nil {
			return false, err
		}
		return h.ReceiverRoute != nil, nil
	}
	return false, nil
}
