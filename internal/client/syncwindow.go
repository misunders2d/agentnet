package client

import (
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Background replication carries no new human turn. User decisions, edits,
// reactions, group invitations and requested files retain their normal lane.
const syncSubs = `('history-archive','history','device-history','read-sync','root-sync','topic-sync','topic-state-sync','reminder-sync','direct-topic-sync','model-sync','invitation-sync')`
const syncStateSubs = `('read-sync','root-sync','topic-sync','topic-state-sync','reminder-sync','direct-topic-sync','model-sync','invitation-sync')`

// The cold window has two parts. Copies on the wire one by one (and current
// state copies) count one each against historyPage. Archive children staged
// but not yet packed count against historyArchiveStage, one part's worth, so
// the producer can fill a whole part while earlier parts travel. Parts in
// flight are bounded by protocol.HistoryArchiveWindow when packing, and
// their children by the part bound; they hold no page capacity, so a part's
// round trip never stops production (the stop-and-wait of 50-message chunks
// copied 30 messages a minute to a new phone, owner, 2026-11-27).
const syncWindowQuery = `SELECT (SELECT count(*) FROM outbox o WHERE o.state IN ('queued','waiting','custody','archive_uploading') AND o.recipient=? AND o.recipient_fp=? AND o.sub IN ` + syncSubs + ` AND o.sub<>'history-archive'), (SELECT count(*) FROM outbox o WHERE state='archive_staged' AND recipient=? AND recipient_fp=? AND NOT EXISTS(SELECT 1 FROM history_archive_entries e WHERE e.child=o.id))`

// historyArchiveStage is how many unpacked archive children one device may
// have staged: exactly one full part.
const historyArchiveStage = protocol.MaxHistoryArchiveEntries

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

// syncWindowRoom is how many more cold copies this device's window takes now.
// A paced page never overshoots it: the bound does not depend on a page's size.
func syncWindowRoom(q dbq, dev identity.Public) (int, error) {
	if revoked, err := revokedDeviceKnown(q, dev.Address, dev.Fingerprint()); err != nil || revoked {
		return 0, err
	}
	var live, staged int
	if err := q.QueryRow(syncWindowQuery, dev.Address, dev.Fingerprint(), dev.Address, dev.Fingerprint()).Scan(&live, &staged); err != nil {
		return 0, err
	}
	return max(min(historyPage-live, historyArchiveStage-staged), 0), nil
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
	var n, staged int
	var err error
	if stateOnly {
		err = q.QueryRow(syncStateWindowQuery, dev.Address, fp).Scan(&n)
	} else {
		err = q.QueryRow(syncWindowQuery, dev.Address, fp, dev.Address, fp).Scan(&n, &staged)
	}
	if err != nil {
		return false, err
	}
	for _, batch := range prepared {
		for _, c := range batch {
			if c.env.To == dev.Address && c.recipientFP == fp && (!stateOnly || isSyncStateSub(c.in.Sub)) {
				if c.state == archiveStaged {
					staged++
				} else {
					n++
				}
			}
		}
	}
	return n >= historyPage || staged >= historyArchiveStage, nil
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
	case "read-sync", "root-sync", "topic-sync", "topic-state-sync", "reminder-sync", "direct-topic-sync", "model-sync", "invitation-sync":
		return true
	}
	return false
}
