package client

import (
	"database/sql"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// skippedCopySchema keeps each device a sent conversation message was not
// sealed for at all because its key cannot be used here: changed and not
// trusted yet, unknown to the Hub, removed by an admin, or not the key its
// person's roster names. It is no copy: nothing is ever sent or retried
// for it. It is the record that says so, so the message keeps listing that
// device (state not_delivered, with why) and its delivery never reads as
// everyone's while a member got nothing.
const skippedCopySchema = `
CREATE TABLE skipped_copies(
  id TEXT PRIMARY KEY,
  conv TEXT NOT NULL,
  lid TEXT NOT NULL,
  recipient TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL,
  created_ms INTEGER NOT NULL);
CREATE INDEX skipped_copies_lid ON skipped_copies(conv, lid);
`

// skippedCopy is the stand-in for a device sendGroupTurn sealed nothing
// for: why in detail ("not sent: …").
func skippedCopy(to, person string, own bool, why error) ConvCopy {
	return ConvCopy{ID: protocol.NewID(), To: to, State: stateNotDelivered, Detail: "not sent: " + why.Error(), Person: person, Own: own, NotSent: true}
}

// addSkippedCopies records skipped with the copies of message lid, in the
// transaction that stores them.
func addSkippedCopies(tx *sql.Tx, conv, lid string, skipped []ConvCopy) error {
	now := time.Now().UnixMilli()
	for _, c := range skipped {
		if _, err := tx.Exec(`INSERT INTO skipped_copies(id, conv, lid, recipient, person, detail, created_ms) VALUES(?, ?, ?, ?, ?, ?, ?)`, c.ID, conv, lid, c.To, c.Person, c.Detail, now); err != nil {
			return err
		}
	}
	return nil
}

// skippedCopies lists the skipped devices of conv's messages sent here by
// logical id (lid "": all of them), in the order they were recorded.
func (s *store) skippedCopies(conv, lid string) (map[string][]ConvCopy, error) {
	rows, err := s.db.Query(`SELECT lid, id, recipient, person, detail FROM skipped_copies WHERE (?1 = '' OR conv = ?1) AND (?2 = '' OR lid = ?2) ORDER BY created_ms, rowid`, conv, lid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]ConvCopy{}
	for rows.Next() {
		var l string
		c := ConvCopy{State: stateNotDelivered, NotSent: true}
		if err := rows.Scan(&l, &c.ID, &c.To, &c.Person, &c.Detail); err != nil {
			return nil, err
		}
		out[l] = append(out[l], c)
	}
	return out, rows.Err()
}

// notSentFirst is a sent message's least advanced state once its skipped
// devices count: one of another person that got nothing comes before every
// copy but a failed one, so the message names it (client and engine
// convMessages, sendGroupTurn).
func notSentFirst(state, detail string, copies []ConvCopy) (string, string) {
	if state == stateFailed {
		return state, detail
	}
	for _, c := range copies {
		if c.NotSent && !c.Own {
			return c.State, c.Detail
		}
	}
	return state, detail
}
