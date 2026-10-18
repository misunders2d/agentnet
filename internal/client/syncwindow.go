package client

import "github.com/misunders2d/agentnet/internal/identity"

// Background replication carries no new human turn. User decisions, edits,
// reactions, group invitations and requested files retain their normal lane.
const syncSubs = `('history-archive','history','device-history','read-sync','root-sync','topic-sync','topic-state-sync','model-sync','invitation-sync')`
const syncStateSubs = `('read-sync','root-sync','topic-sync','topic-state-sync','model-sync','invitation-sync')`

const syncWindowQuery = `SELECT (SELECT coalesce(sum(CASE WHEN sub='history-archive' THEN ? ELSE 1 END),0) FROM outbox WHERE state IN ('queued','waiting','custody','archive_uploading') AND recipient=? AND recipient_fp=? AND sub IN ` + syncSubs + `) + (SELECT count(*) FROM outbox o WHERE state='archive_staged' AND recipient=? AND recipient_fp=? AND NOT EXISTS(SELECT 1 FROM history_archive_entries e WHERE e.child=o.id))`
const syncStateWindowQuery = `SELECT count(*) FROM outbox WHERE state IN ('queued','waiting','custody') AND recipient=? AND recipient_fp=? AND sub IN ` + syncStateSubs

// Cold production leaves a separate page of capacity for small state updates.
// Preserve each source page's atomic cursor/dependency expansion; the page
// threshold is not a hard bound on dependency records or retained ciphertext.
func syncWindowFull(q dbq, dev identity.Public, prepared ...[]outCopy) (bool, error) {
	return syncPendingFull(q, dev, false, prepared...)
}

// A stalled cold archive must not starve read, topic or other current state,
// even if dependency expansion or an older producer filled the cold window.
func syncStateWindowFull(q dbq, dev identity.Public, prepared ...[]outCopy) (bool, error) {
	return syncPendingFull(q, dev, true, prepared...)
}

func syncPendingFull(q dbq, dev identity.Public, stateOnly bool, prepared ...[]outCopy) (bool, error) {
	// A retained signed roster may still list a relay-revoked key. Do not
	// regenerate its background copies after terminal reconciliation.
	fp := dev.Fingerprint()
	if revoked, err := revokedDeviceKnown(q, dev.Address, fp); err != nil {
		return false, err
	} else if revoked {
		return true, nil
	}
	var n int
	var err error
	if stateOnly {
		err = q.QueryRow(syncStateWindowQuery, dev.Address, fp).Scan(&n)
	} else {
		err = q.QueryRow(syncWindowQuery, historyPage, dev.Address, fp, dev.Address, fp).Scan(&n)
	}
	if err != nil {
		return false, err
	}
	for _, batch := range prepared {
		for _, c := range batch {
			if c.env.To == dev.Address && c.recipientFP == fp && (!stateOnly || isSyncStateSub(c.in.Sub)) {
				n++
			}
		}
	}
	return n >= historyPage, nil
}

func isSyncSub(sub string) bool {
	switch sub {
	case "history-archive", "history", "device-history":
		return true
	}
	return isSyncStateSub(sub)
}

func isSyncStateSub(sub string) bool {
	switch sub {
	case "read-sync", "root-sync", "topic-sync", "topic-state-sync", "model-sync", "invitation-sync":
		return true
	}
	return false
}
