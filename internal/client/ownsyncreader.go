package client

import (
	"context"
	"errors"

	"github.com/misunders2d/agentnet/internal/identity"
)

// ownSyncReader reports whether own-device history should be produced for
// dev now: not while the relay serves it nothing until it updates
// AgentNet, and not while its signed capabilities do not read cap (when
// given). Copies sealed for such a device only waited here with no bound
// and later burst ahead of live messages; its job keeps its cursor instead
// and produces once the device can read them (the next member list looks
// again: convWork.readerWait). A profile the Hub cannot answer now holds
// nothing back: delivery still checks every copy at handover. checked
// keeps one answer per device and requirement for one pass.
func (a *Agent) ownSyncReader(ctx context.Context, dev identity.Public, cap string, checked map[string]bool) bool {
	key := dev.Address + "\x00" + dev.Fingerprint() + "\x00" + cap
	if ok, seen := checked[key]; seen {
		return ok
	}
	ok := !a.store.suspendedDevices()[dev.Address]
	if ok && cap != "" {
		ok = !errors.Is(a.requireParticipationCaps(ctx, dev, cap), errAgentIdentityUnsupported)
	}
	if !ok {
		a.convWork.readerWait.Store(true)
	}
	checked[key] = ok
	return ok
}

// deviceHistoryDue reports whether dev's direct-history job has anything
// left to look at: no job yet while sources exist, an older range not
// done, a newer source, or a pending item. A device with nothing due
// costs no profile read.
func (s *store) deviceHistoryDue(dev identity.Public) (bool, error) {
	var due bool
	err := s.db.QueryRow(`SELECT CASE
		WHEN NOT EXISTS (SELECT 1 FROM device_history_jobs WHERE device=?1 AND fingerprint=?2) THEN EXISTS (SELECT 1 FROM device_history_sources)
		ELSE EXISTS (SELECT 1 FROM device_history_jobs j WHERE j.device=?1 AND j.fingerprint=?2 AND (j.older!=0 OR j.tail<(SELECT coalesce(max(seq),0) FROM device_history_sources)))
		  OR EXISTS (SELECT 1 FROM device_history_pending WHERE recipient_fp=?2) END`, dev.Address, dev.Fingerprint()).Scan(&due)
	return due, err
}
