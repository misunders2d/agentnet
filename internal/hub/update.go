package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Latest only (owner policy, v0.8.17): a relay that runs a release asks
// every device to run at least that release, its own. Each request says
// which version its client runs (protocol.VersionHeader, unsigned; none
// from older programs). A device whose version is older is still served
// for a grace period after this relay first ran a release newer than it;
// then it is suspended until it updates: it may only finish what was
// admitted before (whileSuspended), its other requests get HTTP 426, and
// its stream gets the release and update_required events, then pings only.
// What is addressed to it stays in custody meanwhile. Suspension is
// availability, never authority. Only the relay's own release suspends: an
// admin's recommendation of a newer one is pushed as a notice, never
// enforced (a typo or a tag not yet published must lock nobody out), and a
// development relay (no vX.Y.Z tag) suspends nobody.

const updateSchema = `
ALTER TABLE agents ADD COLUMN client_version TEXT;
CREATE TABLE releases_served(version TEXT PRIMARY KEY, since_ms INTEGER NOT NULL);
`

// updateState is what the policy decides by, replaced as a whole.
type updateState struct {
	release string          // the relay's own release; "" on a development relay: nobody is asked to update
	served  []servedRelease // each release this relay ran, oldest first
}

type servedRelease struct {
	version string
	since   time.Time // when this relay first ran it
}

// whileSuspended are the requests a suspended device may still make, so
// that what was admitted before drains and nothing new starts: its stream,
// which says what to update to, the ping acknowledgements that keep it
// open, the recommendation, receipts of what it received, and the
// read-only lookups of a member that its sends make first (programs before
// v0.8.17 look up the asker before they answer). Posting a message is
// decided by its kind once its signature is verified (handlePostMessage):
// an answer or a result may still go.
var whileSuspended = map[string]bool{
	"GET /v1/stream": true, "POST /v1/stream/ack": true, "GET /v1/release": true,
	"POST /v1/messages/{id}/ack":     true,
	"GET /v1/agents/{label}/{agent}": true, "GET /v1/agents/{label}/{agent}/sessions": true, "GET /v1/agents/{label}/{agent}/profile": true,
}

// drainsWork reports whether a suspended device may still post a message
// of outer kind: an answer or a result finishes what was admitted before.
func drainsWork(kind string) bool { return kind == envelope.KindAnswer || kind == envelope.KindResult }

// refuseOutdated answers HTTP 426 update_required to a request whose client
// must update first, and reports whether it did.
func (h *Hub) refuseOutdated(w http.ResponseWriter, r *http.Request) bool {
	latest, required := h.updateRequired(r.Header.Get(protocol.VersionHeader))
	if required {
		writeJSON(w, http.StatusUpgradeRequired, protocol.NewUpdateRequired(latest))
	}
	return required
}

// relayRelease is the release a relay of version build asks every device
// to run: its own, or none for a development build.
func relayRelease(build string) string {
	if !protocol.IsRelease(build) {
		return ""
	}
	return build
}

// pushedRelease is the recommendation clients are told: on a release relay
// the newer of its own release and the admin's recommendation, with the
// admin's URL and note when the admin named it and the project page
// otherwise; on a development relay the admin's recommendation as it is.
// A newer recommendation is a notice only: it suspends nobody.
func (h *Hub) pushedRelease(admin protocol.Release) protocol.Release {
	own := relayRelease(h.cfg.Version)
	if own == "" || admin.Version == own || protocol.Newer(admin.Version, own) {
		return admin
	}
	return protocol.Release{Version: own, URL: protocol.ReleaseURL(own)}
}

// serveRelease records the relay's own release as run (the first time
// only) and plans the member list's next change at a grace period's end.
func (h *Hub) serveRelease() error {
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	next := &updateState{release: relayRelease(h.cfg.Version)}
	if next.release != "" {
		served, err := h.store.releasesServed(next.release, time.Now())
		if err != nil {
			return err
		}
		next.served = served
	}
	h.update.Store(next)
	h.planGraceEnd(next)
	return nil
}

// planGraceEnd wakes the member list when the next grace period ends:
// devices still on an older version are suspended from then on. One timer,
// replaced on every change; each end fires once. Called under updateMu.
func (h *Hub) planGraceEnd(st *updateState) {
	if h.graceEnd != nil {
		h.graceEnd.Stop()
		h.graceEnd = nil
	}
	if st.release == "" {
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
// it may use this Hub, and to which release, the relay's own: its version
// is not current and the grace period has passed since this relay first
// ran a release newer than it.
func (h *Hub) updateRequired(v string) (string, bool) {
	st := h.update.Load()
	if st == nil || st.release == "" || protocol.Current(v, st.release) {
		return "", false
	}
	for _, s := range st.served {
		if !protocol.Current(v, s.version) {
			return st.release, !time.Now().Before(s.since.Add(h.cfg.UpdateGrace))
		}
	}
	return st.release, false
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
// release event, again whenever the recommendation changes, the
// update_required event, and otherwise pings only, so nothing addressed to
// it leaves custody. It ends with the Hub.
func (h *Hub) holdSuspended(ctx context.Context, sub *subscriber, version string, ping *time.Ticker, lease time.Duration, write func(string, ...any) bool) {
	sentRelease, told := int64(-1), false
	for {
		latest, required := h.updateRequired(version)
		if !required {
			return
		}
		if rel, gen := h.currentRelease(); gen != sentRelease {
			data, _ := json.Marshal(rel)
			if !write("event: release\ndata: %s\n\n", data) {
				return
			}
			sentRelease = gen
		}
		if !told {
			upd, _ := json.Marshal(protocol.UpdateRequired{Latest: latest, URL: protocol.ReleaseURL(latest)})
			if !write("event: update_required\ndata: %s\n\n", upd) {
				return
			}
			told = true
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

// releasesServed records version as run now, unless it was before, and
// lists every release run, oldest first.
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
