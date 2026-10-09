package client

import "database/sql"

// Add the direct ledger without interpreting queue completion as receipt or
// recipient rendering. Existing rooted conversation cursors stay untouched.
func (a *Agent) deviceHistoryProgress(j *HistoryJob, fp string) error {
	var older, tail, ceiling int64
	err := a.store.db.QueryRow(`SELECT older,tail,(SELECT coalesce(max(seq),0) FROM device_history_sources) FROM device_history_jobs WHERE device=? AND fingerprint=?`, j.Device, fp).Scan(&older, &tail, &ceiling)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	j.DeliveryKnown = true
	var pending int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM device_history_pending WHERE recipient_fp=?`, fp).Scan(&pending); err != nil {
		return err
	}
	j.Deferred += pending
	if j.State != "ended" && (older != 0 || tail < ceiling || pending > 0) {
		j.State = "running"
		j.ConvsDone = 0
		j.ConvsTotal = 0
	}
	rows, err := a.store.db.Query(`SELECT coalesce(o.state,''),count(*) FROM device_history_copies c LEFT JOIN outbox o ON o.id=c.carrier AND o.recipient=? AND o.recipient_fp=c.recipient_fp AND o.sub='device-history' WHERE c.recipient_fp=? GROUP BY o.state`, j.Device, fp)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err = rows.Scan(&state, &n); err != nil {
			return err
		}
		switch state {
		case "queued", "waiting":
			j.Queued += n
		case "custody":
			j.Custody += n
		case "delivered":
			j.Delivered += n
		default:
			j.Blocked += n
		}
	}
	return rows.Err()
}
