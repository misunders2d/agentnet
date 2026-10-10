package client

import "github.com/misunders2d/agentnet/internal/identity"

// Background replication carries no new human turn. User decisions, edits,
// reactions, group invitations and requested files retain their normal lane.
const syncSubs = `('history','device-history','read-sync','root-sync','topic-sync','topic-state-sync','model-sync','invitation-sync')`

// One existing source page may be prepared only while less than one page is
// outstanding. Preserve its atomic cursor/dependency commit: the last page
// can exceed the threshold by its existing bounded dependency expansion.
// Counts are durable and exact-key scoped; relay custody is not admission.
func syncWindowFull(q dbq, dev identity.Public, prepared ...[]outCopy) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND recipient_fp=? AND sub IN `+syncSubs+` AND state IN ('queued','waiting','custody')`, dev.Address, dev.Fingerprint()).Scan(&n)
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
	case "history", "device-history", "read-sync", "root-sync", "topic-sync", "topic-state-sync", "model-sync", "invitation-sync":
		return true
	}
	return false
}
