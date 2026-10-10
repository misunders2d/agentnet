package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Latest only (owner policy, v0.8.17): a relay that runs a release asks
// every device to run the latest release, the newer of its own version and
// the admin's recommendation. Each request says which version its client
// runs (protocol.VersionHeader, unsigned; none from older programs). A
// device whose version is older is still served for a grace period after
// this Hub first asked for a newer release; then it is suspended until it
// updates: every request but its stream gets HTTP 426, and its stream gets
// the release and update_required events, then pings only. What is
// addressed to it stays in custody meanwhile. Suspension is availability,
// never authority, and a development relay (no vX.Y.Z tag) suspends nobody.

const updateSchema = `
ALTER TABLE agents ADD COLUMN client_version TEXT;
CREATE TABLE releases_served(version TEXT PRIMARY KEY, since_ms INTEGER NOT NULL);
`

// updateState is what the policy decides by, replaced as a whole.
type updateState struct {
	latest string          // "" on a development relay: nobody is asked to update
	served []servedRelease // each release this Hub asked for, oldest first
}

type servedRelease struct {
	version string
	since   time.Time // when this Hub first asked for it
}

// whileSuspended are the requests a suspended device may still make: its
// stream, which says what to update to, the ping acknowledgements that
// keep it open, and the recommendation.
var whileSuspended = map[string]bool{"GET /v1/stream": true, "POST /v1/stream/ack": true, "GET /v1/release": true}

// latestRelease is the release a relay of version build asks clients to
// run: its own, or the admin's recommendation when that is a newer release.
// A development relay asks for none.
func latestRelease(build string, admin protocol.Release) string {
	if !protocol.IsRelease(build) {
		return ""
	}
	if protocol.Newer(admin.Version, build) {
		return admin.Version
	}
	return build
}

// pushedRelease is the recommendation clients are told: on a release relay
// always the latest release, with the admin's URL and note when the admin
// named that release and its project page otherwise; on a development
// relay the admin's recommendation as it is.
func (h *Hub) pushedRelease(admin protocol.Release) protocol.Release {
	latest := latestRelease(h.cfg.Version, admin)
	if latest == "" || latest == admin.Version {
		return admin
	}
	return protocol.Release{Version: latest, URL: protocol.ReleaseURL(latest)}
}

// serveLatest records the latest release as asked for (the first time
// only) and plans the member list's next change at a grace period's end.
// It reports whether the latest release changed.
func (h *Hub) serveLatest() (bool, error) {
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	old := h.update.Load()
	next := &updateState{latest: latestRelease(h.cfg.Version, *h.release.Load())}
	if old != nil {
		next.served = old.served
	}
	if next.latest != "" {
		served, err := h.store.releasesServed(next.latest, time.Now())
		if err != nil {
			return false, err
		}
		next.served = served
	}
	h.update.Store(next)
	h.planGraceEnd(next)
	return old != nil && old.latest != next.latest, nil
}

// planGraceEnd wakes the member list when the next grace period ends:
// devices still on an older version are suspended from then on. One timer,
// replaced on every change; each end fires once. Called under updateMu.
func (h *Hub) planGraceEnd(st *updateState) {
	if h.graceEnd != nil {
		h.graceEnd.Stop()
		h.graceEnd = nil
	}
	if st.latest == "" {
		return
	}
	now, next := time.Now(), time.Time{}
	for _, s := range st.served {
		if end := s.since.Add(h.cfg.UpdateGrace); end.After(now) && (next.IsZero() || end.Before(next)) {
			next = end
		}
	}
	if next.IsZero() {
		return
	}
	h.graceEnd = time.AfterFunc(time.Until(next), func() {
		h.membersChanged() // also wakes every stream: one in grace is suspended now
		h.updateMu.Lock()
		defer h.updateMu.Unlock()
		if h.update.Load() == st {
			h.planGraceEnd(st)
		}
	})
}

// updateRequired reports whether a client of version v must update before
// it may use this Hub, and to which release: its version is not current
// and the grace period has passed since this Hub first asked for a release
// newer than it.
func (h *Hub) updateRequired(v string) (string, bool) {
	st := h.update.Load()
	if st == nil || st.latest == "" || protocol.Current(v, st.latest) {
		return "", false
	}
	for _, s := range st.served {
		if !protocol.Current(v, s.version) {
			return st.latest, !time.Now().Before(s.since.Add(h.cfg.UpdateGrace))
		}
	}
	return st.latest, false
}

// reportedVersion is the version a request says its client runs, as the
// member list may show it: printable without spaces and short, or none.
func reportedVersion(r *http.Request) string {
	v := r.Header.Get(protocol.VersionHeader)
	if len(v) > 64 || strings.IndexFunc(v, func(c rune) bool { return c <= ' ' || c > '~' }) >= 0 {
		return ""
	}
	return v
}

// noteVersion records the version a device's stream reported (none from
// older programs) and moves the member list on when it changed.
func (h *Hub) noteVersion(address, version string) error {
	changed, err := h.store.setClientVersion(address, version)
	if err == nil && changed {
		h.membersChanged()
	}
	return err
}

// holdSuspended keeps the stream of a device that must update first: the
// release and update_required events, again whenever the latest release
// moves, and otherwise pings only, so nothing addressed to it leaves
// custody. It ends once the device need not update any more (the admin's
// recommendation was taken back), so it connects again as usual.
func (h *Hub) holdSuspended(ctx context.Context, sub *subscriber, version string, ping *time.Ticker, lease time.Duration, write func(string, ...any) bool) {
	told := ""
	for {
		latest, required := h.updateRequired(version)
		if !required {
			return
		}
		if latest != told {
			rel, _ := h.currentRelease()
			data, _ := json.Marshal(rel)
			upd, _ := json.Marshal(protocol.UpdateRequired{Latest: latest, URL: protocol.ReleaseURL(latest)})
			if !write("event: release\ndata: %s\n\n", data) || !write("event: update_required\ndata: %s\n\n", upd) {
				return
			}
			told = latest
		}
		select {
		case <-sub.wake:
		case <-ping.C:
			if time.Since(time.Unix(0, sub.lastAck.Load())) > lease {
				return
			}
			if !write("event: ping\ndata: {\"conn\":%q}\n\n", sub.id) {
				return
			}
		case <-ctx.Done():
			return
		case <-h.done:
			return
		}
	}
}

// releasesServed records version as asked for now, unless it was before,
// and lists every release asked for, oldest first.
func (s *store) releasesServed(version string, now time.Time) ([]servedRelease, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO releases_served(version, since_ms) VALUES(?, ?)`, version, now.UnixMilli()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT version, since_ms FROM releases_served ORDER BY since_ms, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []servedRelease
	for rows.Next() {
		var r servedRelease
		var since int64
		if err := rows.Scan(&r.version, &since); err != nil {
			return nil, err
		}
		r.since = time.UnixMilli(since)
		out = append(out, r)
	}
	return out, rows.Err()
}

// setClientVersion records the version address's stream reported and
// reports whether it differs from the one recorded before.
func (s *store) setClientVersion(address, version string) (bool, error) {
	res, err := s.db.Exec(`UPDATE agents SET client_version = ? WHERE address = ? AND client_version IS NOT ?`, version, address, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
