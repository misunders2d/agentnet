package client

import (
	"database/sql"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A review notice is a snapshot of what waits on its host (reviewnotice.go):
// the newest one from a host stands for all its earlier ones, so one card
// per host is left (MEL-532). Offline is normal, so notices may arrive out
// of order: "newest" is by the host's own time (a report's At, else the
// envelope's), never by arrival. A report that says nothing waits (no
// items, no count) settles the host's card and is kept already resolved.

// noticeTime is the host time a notice speaks for.
func noticeTime(body string, ts int64) int64 {
	if r, ok := ParseReport(body); ok && r.At > 0 {
		return r.At
	}
	return ts
}

// noticeSettled reports whether a notice says nothing waits any more.
func noticeSettled(body string) bool {
	r, ok := ParseReport(body)
	return ok && len(r.Items) == 0 && r.Count == 0
}

// A host's time has whole seconds: of two notices from the same second,
// the one stored later counts as the newer (out-of-order delivery within
// one second of the host's clock is not told apart).

// supersedeNotices runs in the transaction that stores review notice in:
// it resolves the host's older open notices, and stores in resolved when a
// newer one is open already, or when it settles the card.
func supersedeNotices(tx *sql.Tx, in envelope.Inner) error {
	at := noticeTime(in.Body, in.TS)
	rows, err := tx.Query(`SELECT id, body, ts FROM inbox WHERE sender = ? AND id != ? AND state = ? AND conv IS NULL AND (`+receivedNotice+`)`,
		in.From, in.ID, stateNeedHuman, envelope.KindMessage, envelope.StatusReviewNotice)
	if err != nil {
		return err
	}
	var older []string
	stale := noticeSettled(in.Body)
	for rows.Next() {
		var id, body string
		var ts int64
		if err := rows.Scan(&id, &body, &ts); err != nil {
			rows.Close()
			return err
		}
		if at >= noticeTime(body, ts) {
			older = append(older, id)
		} else {
			stale = true // a newer snapshot is here already
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range older {
		if _, err := tx.Exec(`UPDATE inbox SET state = ? WHERE id = ? AND state = ?`, stateResolved, id, stateNeedHuman); err != nil {
			return err
		}
	}
	if stale {
		_, err = tx.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateResolved, in.ID)
	}
	return err
}

// settleLeftoverNotices resolves, once per home, every open review notice
// but the newest per host: those stored before notices superseded each
// other (bounded recovery). It changes nothing a second time.
func (s *store) settleLeftoverNotices() error {
	const done = "review_notices_settled"
	if v, err := s.config(done); err == nil && v == "1" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, sender, body, ts FROM inbox WHERE state = ? AND conv IS NULL AND (`+receivedNotice+`) ORDER BY arrival`,
		stateNeedHuman, envelope.KindMessage, envelope.StatusReviewNotice)
	if err != nil {
		return err
	}
	type notice struct {
		id string
		at int64
	}
	newest := map[string]notice{}
	var resolve []string
	for rows.Next() {
		var id, sender, body string
		var ts int64
		if err := rows.Scan(&id, &sender, &body, &ts); err != nil {
			rows.Close()
			return err
		}
		n := notice{id, noticeTime(body, ts)}
		cur, ok := newest[sender]
		switch {
		case !ok:
			newest[sender] = n
		case n.at >= cur.at: // in arrival order: a later one of the same second is newer
			resolve = append(resolve, cur.id)
			newest[sender] = n
		default:
			resolve = append(resolve, n.id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range resolve {
		if _, err := tx.Exec(`UPDATE inbox SET state = ? WHERE id = ? AND state = ?`, stateResolved, id, stateNeedHuman); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO config(k, v) VALUES(?, '1')`, done); err != nil {
		return err
	}
	return tx.Commit()
}
