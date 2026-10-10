package client

import "github.com/misunders2d/agentnet/internal/identity"

// Background replication carries no new human turn. User decisions, edits,
// reactions, group invitations and requested files retain their normal lane.
const syncSubs = `('history-archive','history','device-history','read-sync','root-sync','topic-sync','topic-state-sync','model-sync','invitation-sync')`

const syncWindowQuery = `SELECT (SELECT coalesce(sum(CASE WHEN sub='history-archive' THEN ? ELSE 1 END),0) FROM outbox WHERE state IN ('queued','waiting','custody','archive_uploading') AND recipient=? AND recipient_fp=? AND sub IN ` + syncSubs + `) + (SELECT count(*) FROM outbox o WHERE state='archive_staged' AND recipient=? AND recipient_fp=? AND NOT EXISTS(SELECT 1 FROM history_archive_entries e WHERE e.child=o.id))`

// One existing source page may be prepared only while less than one page is
// outstanding. Preserve its atomic cursor/dependency commit: the last page
// can exceed the threshold by its existing bounded dependency expansion.
// Counts are durable and exact-key scoped; relay custody is not admission.
func syncWindowFull(q dbq, dev identity.Public, prepared ...[]outCopy) (bool, error) {
	var n int
	err := q.QueryRow(syncWindowQuery, historyPage, dev.Address, dev.Fingerprint(), dev.Address, dev.Fingerprint()).Scan(&n)
	if err != nil {
		return false, err
	}
	for _, batch := range prepared {
		for _, c := range batch {
			if c.env.To == dev.Address && c.recipientFP == dev.Fingerprint() {
				n++
			}
		}
	}
	return n >= historyPage, nil
}

func isSyncSub(sub string) bool {
	switch sub {
	case "history-archive", "history", "device-history", "read-sync", "root-sync", "topic-sync", "topic-state-sync", "model-sync", "invitation-sync":
		return true
	}
	return false
}
