package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Optional notifications for browser devices (docs/revival/NOTIFY.md §3-5).
//
// A device that turned them on has preferences (on/off, the exact sender
// keys whose attention may alert it, muted channels) and one Web Push
// subscription. A message whose sender asks for attention queues a pending
// alert, in the message's own transaction, keyed by (device, channel,
// sender): its deadline is set by the first message and never extended; a
// later one counts and bumps its generation. The device's presentation of
// the newest message cancels it. One scheduler sends what is due, after
// checking everything again, and completes only what it sent: an alert
// whose generation moved on meanwhile, or a subscription replaced
// meanwhile, is left for the next round.

// Operational constants (NOTIFY.md §4).
const (
	notifyGrace             = 5 * time.Second  // wait for the device's presentation of a message
	notifyLifetime          = 24 * time.Hour   // a pending alert not sent by then is dropped
	notifyChannelsPerSender = 16               // exact channels kept per sender and device; beyond, one summary per sender
	notifySummaryChannels   = 64               // channels a summary records; one beyond them never alerts on its own
	notifyBatch             = 32               // devices looked at per scheduler round
	pushBurst               = 3                // pushes a device may get at once…
	pushRefill              = 10 * time.Second // …then one per this
	pushTimeout             = 10 * time.Second // per push request
	pushTTL                 = 24 * 60 * 60     // seconds a push service keeps an undelivered push
	pushMaxAttempts         = 6
	pushMaxBackoff          = time.Hour
)

// enqueueAttention queues (or adds to) the pending alert for env, which
// asks for attention, within tx; it does nothing unless the recipient turned
// notifications on, holds a subscription, allows the sender with the key
// the Hub has enrolled for it now (senderFP), and has not muted the channel.
func enqueueAttention(tx *sql.Tx, env envelope.Envelope, senderFP string, now time.Time) error {
	var eligible bool
	if err := tx.QueryRow(`SELECT
		EXISTS (SELECT 1 FROM notify_prefs WHERE address = ? AND enabled = 1)
		AND EXISTS (SELECT 1 FROM push_subs WHERE address = ?)
		AND EXISTS (SELECT 1 FROM notify_senders WHERE address = ? AND sender = ? AND fingerprint = ?)
		AND NOT EXISTS (SELECT 1 FROM notify_mutes WHERE address = ? AND channel = ?)`,
		env.To, env.To, env.To, env.From, senderFP, env.To, env.Chan).Scan(&eligible); err != nil || !eligible {
		return err
	}
	channel := env.Chan
	var have, channels int
	if err := tx.QueryRow(`SELECT
		(SELECT count(*) FROM notify_pending WHERE address = ? AND sender = ? AND channel = ?),
		(SELECT count(*) FROM notify_pending WHERE address = ? AND sender = ? AND channel != '')`,
		env.To, env.From, channel, env.To, env.From).Scan(&have, &channels); err != nil {
		return err
	}
	recorded := ""
	if have == 0 && channels >= notifyChannelsPerSender {
		// This sender's summary. It keeps its sender, and records the
		// channels it stands for (bounded), so mutes still decide.
		channel = ""
		if err := tx.QueryRow(`SELECT channels FROM notify_pending WHERE address = ? AND channel = '' AND sender = ?`,
			env.To, env.From).Scan(&recorded); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if list := strings.Fields(recorded); !slices.Contains(list, env.Chan) && len(list) < notifySummaryChannels {
			recorded = strings.Join(append(list, env.Chan), " ")
		}
	}
	ms := now.UnixMilli()
	_, err := tx.Exec(`INSERT INTO notify_pending(address, channel, sender, gen, count, last_msg, due_ms, expires_ms, channels)
		VALUES(?, ?, ?, 1, 1, ?, ?, ?, ?)
		ON CONFLICT(address, channel, sender) DO UPDATE SET gen = gen + 1, count = count + 1, last_msg = excluded.last_msg,
		channels = excluded.channels`,
		env.To, channel, env.From, env.ID, ms+notifyGrace.Milliseconds(), ms+notifyLifetime.Milliseconds(), recorded)
	return err
}

// dropNotify removes a device's notification state (revoked, or turned off
// with its subscription deleted), within tx.
func dropNotify(tx *sql.Tx, address string) error {
	for _, q := range []string{
		`DELETE FROM notify_prefs WHERE address = ?`,
		`DELETE FROM notify_senders WHERE address = ?`,
		`DELETE FROM notify_mutes WHERE address = ?`,
		`DELETE FROM push_subs WHERE address = ?`,
		`DELETE FROM notify_pending WHERE address = ?`,
	} {
		if _, err := tx.Exec(q, address); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) notifyState(address string) (protocol.NotifyState, error) {
	st := protocol.NotifyState{Prefs: protocol.NotifyPrefs{Senders: []protocol.NotifySender{}, Mutes: []string{}}}
	err := s.db.QueryRow(`SELECT enabled FROM notify_prefs WHERE address = ?`, address).Scan(&st.Prefs.Enabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	rows, err := s.db.Query(`SELECT sender, fingerprint FROM notify_senders WHERE address = ? ORDER BY sender`, address)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var ns protocol.NotifySender
		if err := rows.Scan(&ns.Address, &ns.Fingerprint); err != nil {
			rows.Close()
			return st, err
		}
		st.Prefs.Senders = append(st.Prefs.Senders, ns)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT channel FROM notify_mutes WHERE address = ? ORDER BY channel`, address)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return st, err
		}
		st.Prefs.Mutes = append(st.Prefs.Mutes, c)
	}
	rows.Close()
	err = s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM push_subs WHERE address = ?)`, address).Scan(&st.Subscribed)
	return st, err
}

// setNotifyPrefs replaces a device's preferences. Turning off drops its
// pending alerts; a muted channel or a removed sender is dropped when due.
func (s *store) setNotifyPrefs(address string, p protocol.NotifyPrefs) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO notify_prefs(address, enabled, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		address, p.Enabled, time.Now().Unix()); err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM notify_senders WHERE address = ?`, `DELETE FROM notify_mutes WHERE address = ?`} {
		if _, err := tx.Exec(q, address); err != nil {
			return err
		}
	}
	for _, ns := range p.Senders {
		if _, err := tx.Exec(`INSERT INTO notify_senders(address, sender, fingerprint) VALUES(?, ?, ?)`, address, ns.Address, ns.Fingerprint); err != nil {
			return err
		}
	}
	for _, c := range p.Mutes {
		if _, err := tx.Exec(`INSERT INTO notify_mutes(address, channel) VALUES(?, ?)`, address, c); err != nil {
			return err
		}
	}
	if !p.Enabled {
		if _, err := tx.Exec(`DELETE FROM notify_pending WHERE address = ?`, address); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// setPushSub replaces a device's subscription under a new identity, so an
// outcome for the old one never touches it.
func (s *store) setPushSub(address string, sub protocol.PushSubscription) error {
	_, err := s.db.Exec(`INSERT INTO push_subs(address, sub_id, endpoint, p256dh, auth, created_at) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET sub_id = excluded.sub_id, endpoint = excluded.endpoint, p256dh = excluded.p256dh,
		auth = excluded.auth, created_at = excluded.created_at`,
		address, protocol.NewID(), sub.Endpoint, sub.P256DH, sub.Auth, time.Now().Unix())
	return err
}

// deletePushSub removes a device's subscription (only the one named, if
// subID is set) and, with it, the device's pending alerts.
func (s *store) deletePushSub(address, subID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM push_subs WHERE address = ? AND (? = '' OR sub_id = ?)`, address, subID, subID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		if _, err := tx.Exec(`DELETE FROM notify_pending WHERE address = ?`, address); err != nil {
			return false, err
		}
	}
	return n == 1, tx.Commit()
}

// notifySeen cancels the device's pending alerts of channel (and its
// senders' summaries) whose newest message is among ids.
func (s *store) notifySeen(address string, seen protocol.NotifySeen) error {
	args := []any{address, seen.Channel}
	marks := make([]string, len(seen.IDs))
	for i, id := range seen.IDs {
		marks[i] = "?"
		args = append(args, id)
	}
	_, err := s.db.Exec(`DELETE FROM notify_pending WHERE address = ? AND channel IN (?, '') AND last_msg IN (`+strings.Join(marks, ", ")+`)`, args...)
	return err
}

// Handlers. Every call but GET /v1/notify is signed and acts on the
// calling device only; bodies are strict and bounded. A subscription is
// never echoed, logged or put in an error.

func (h *Hub) handleNotifyInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, protocol.NotifyInfo{PushKey: h.push.publicKey, PushHosts: h.pushHosts(), GraceMS: notifyGrace.Milliseconds()})
}

func (h *Hub) handleNotifyPrefsGet(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	st, err := h.store.notifyState(caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// notifyBody authenticates a notify request and decodes its bounded body.
func (h *Hub) notifyBody(w http.ResponseWriter, r *http.Request, v any) (string, bool) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return "", false
	}
	if len(body) > protocol.MaxNotifyRequest || decodeStrict(body, v) != nil {
		writeError(w, http.StatusBadRequest, "", "malformed request")
		return "", false
	}
	return caller, true
}

func (h *Hub) handleNotifyPrefsPut(w http.ResponseWriter, r *http.Request) {
	var p protocol.NotifyPrefs
	caller, ok := h.notifyBody(w, r, &p)
	if !ok {
		return
	}
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	if err := h.store.setNotifyPrefs(caller, p); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.notifier.wake()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var sub protocol.PushSubscription
	caller, ok := h.notifyBody(w, r, &sub)
	if !ok {
		return
	}
	if err := sub.Validate(h.pushHosts()); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error()) // never contains the subscription
		return
	}
	if err := h.store.setPushSub(caller, sub); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.notifier.wake()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if _, err := h.store.deletePushSub(caller, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.notifier.wake()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) handleNotifySeen(w http.ResponseWriter, r *http.Request) {
	var seen protocol.NotifySeen
	caller, ok := h.notifyBody(w, r, &seen)
	if !ok {
		return
	}
	if err := seen.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	if err := h.store.notifySeen(caller, seen); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.notifier.wake()
	w.WriteHeader(http.StatusNoContent)
}

// pushHosts is the explicit list of push services this Hub sends to.
func (h *Hub) pushHosts() []string {
	return append(slices.Clone(protocol.DefaultPushHosts), h.cfg.PushHosts...)
}

// The scheduler.

// pushSender sends one push; status is the push service's HTTP status (0
// with err for a failure before one), retryAfter its Retry-After. Its
// errors never contain the subscription.
type pushSender func(ctx context.Context, sub protocol.PushSubscription, payload []byte, topic string) (status int, retryAfter time.Duration, err error)

type notifier struct {
	h       *Hub
	wakeC   chan struct{}
	send    pushSender
	now     func() time.Time
	mu      sync.Mutex // guards buckets (the scheduler and tests)
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newNotifier(h *Hub) *notifier {
	return &notifier{h: h, wakeC: make(chan struct{}, 1), now: time.Now, buckets: map[string]*bucket{}}
}

// wake makes the scheduler look again (non-blocking).
func (n *notifier) wake() {
	if n == nil {
		return
	}
	select {
	case n.wakeC <- struct{}{}:
	default:
	}
}

// run sends due alerts until ctx ends: one timer on the earliest deadline,
// woken by changes; nothing polls.
func (n *notifier) run(ctx context.Context) {
	for {
		next, err := n.round(ctx)
		if err != nil {
			n.h.cfg.Logf("notifications: %v", err)
		}
		var timer *time.Timer
		var due <-chan time.Time
		if !next.IsZero() {
			timer = time.NewTimer(max(0, next.Sub(n.now())))
			due = timer.C
		}
		select {
		case <-ctx.Done():
		case <-n.wakeC:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// round sends for up to notifyBatch devices with alerts due, and returns
// the next deadline (zero: none).
func (n *notifier) round(ctx context.Context) (time.Time, error) {
	now := n.now()
	rows, err := n.h.store.db.Query(`SELECT DISTINCT address FROM notify_pending WHERE due_ms <= ? LIMIT ?`, now.UnixMilli(), notifyBatch)
	if err != nil {
		return time.Time{}, err
	}
	var due []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return time.Time{}, err
		}
		due = append(due, a)
	}
	rows.Close()
	for _, a := range due {
		if ctx.Err() != nil {
			break
		}
		if err := n.dispatch(ctx, a, now); err != nil {
			n.h.cfg.Logf("notifications for %s: %v", a, err)
			// Not looked at again at once: a failure repeating does not spin.
			if _, err := n.h.store.db.Exec(`UPDATE notify_pending SET due_ms = ? WHERE address = ? AND due_ms <= ?`,
				now.Add(time.Minute).UnixMilli(), a, now.UnixMilli()); err != nil {
				return time.Time{}, err
			}
		}
	}
	var next sql.NullInt64
	if err := n.h.store.db.QueryRow(`SELECT min(due_ms) FROM notify_pending`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}, err
	}
	return time.UnixMilli(next.Int64), nil
}

// alert is one pending row as it was when a push was made from it. Its
// generation and newest message together name that state of that row: a
// row recreated after a presentation starts again at generation 1 but
// always with another newest message, so an outcome for the old state
// never touches it.
type alert struct {
	channel, sender, lastMsg string
	gen                      int64
	attempts                 int
	expires                  int64
	recorded                 []string // a summary's channels
}

// sameState matches a pending row in exactly the state a was read in.
const sameState = `address = ? AND channel = ? AND sender = ? AND gen = ? AND last_msg = ?`

func (a alert) state(address string) []any {
	return []any{address, a.channel, a.sender, a.gen, a.lastMsg}
}

// dispatch sends one push for the device's due alerts, if they are still
// allowed, and completes only what that push covered.
func (n *notifier) dispatch(ctx context.Context, address string, now time.Time) error {
	st := n.h.store
	var subID string
	var sub protocol.PushSubscription
	err := st.db.QueryRow(`SELECT sub_id, endpoint, p256dh, auth FROM push_subs WHERE address = ?`, address).Scan(&subID, &sub.Endpoint, &sub.P256DH, &sub.Auth)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = st.db.Exec(`DELETE FROM notify_pending WHERE address = ?`, address)
		return err
	}
	if err != nil {
		return err
	}
	rows, err := st.db.Query(`SELECT channel, sender, last_msg, gen, attempts, expires_ms, channels FROM notify_pending WHERE address = ? AND due_ms <= ?`,
		address, now.UnixMilli())
	if err != nil {
		return err
	}
	var all []alert
	for rows.Next() {
		var a alert
		var recorded string
		if err := rows.Scan(&a.channel, &a.sender, &a.lastMsg, &a.gen, &a.attempts, &a.expires, &recorded); err != nil {
			rows.Close()
			return err
		}
		a.recorded = strings.Fields(recorded)
		all = append(all, a)
	}
	rows.Close()
	var send, drop []alert
	for _, a := range all {
		ok, err := n.allowed(address, a, now)
		if err != nil {
			return err
		}
		if ok {
			send = append(send, a)
		} else {
			drop = append(drop, a)
		}
	}
	if err := complete(st, address, drop, now, false); err != nil {
		return err
	}
	if len(send) == 0 {
		return nil
	}
	if wait := n.take(address, now); wait > 0 { // rate-limited: kept, with a finite deadline
		return reschedule(st, address, send, now.Add(wait))
	}
	// One push for all of it: the one conversation, or a summary.
	payload := protocol.PushPayload{V: 1}
	topic := "summary"
	if channels := distinctChannels(send); len(channels) == 1 && channels[0] != "" {
		payload.Channel, topic = channels[0], channels[0]
	}
	body, _ := json.Marshal(payload)
	sendCtx, cancel := context.WithTimeout(ctx, pushTimeout)
	status, retryAfter, err := n.send(sendCtx, sub, body, topic)
	cancel()
	switch {
	case err == nil && status >= 200 && status < 300:
		return complete(st, address, send, now, true)
	case err == nil && (status == http.StatusNotFound || status == http.StatusGone):
		// Only that subscription ends; a replacement made meanwhile stays,
		// and the alerts wait for it.
		gone, err := st.deletePushSub(address, subID)
		if err != nil || gone {
			return err
		}
		return reschedule(st, address, send, now)
	case err != nil || status == http.StatusTooManyRequests || status >= 500:
		return retry(st, address, send, now, retryAfter, status, n.h.cfg.Logf)
	default:
		n.h.cfg.Logf("notifications for %s: the push service refused a push (HTTP %d); dropped", address, status)
		return complete(st, address, send, now, false)
	}
}

// allowed checks a due alert again: turned on, not revoked, not expired,
// the sender still allowed with the key the Hub has enrolled for it now
// and not revoked, and its channel not muted; a summary, at least one of
// the channels it recorded not muted (a channel beyond its record never
// alerts on its own).
func (n *notifier) allowed(address string, a alert, now time.Time) (bool, error) {
	if a.expires <= now.UnixMilli() {
		return false, nil
	}
	st := n.h.store
	var enabled bool
	var fp string
	err := st.db.QueryRow(`SELECT
		EXISTS (SELECT 1 FROM notify_prefs p JOIN agents g ON g.address = p.address WHERE p.address = ? AND p.enabled = 1 AND g.revoked_at IS NULL),
		coalesce((SELECT fingerprint FROM notify_senders WHERE address = ? AND sender = ?), '')`,
		address, address, a.sender).Scan(&enabled, &fp)
	if err != nil || !enabled || fp == "" {
		return false, err
	}
	channels := []string{a.channel}
	if a.channel == "" {
		channels = a.recorded
	}
	open := false
	for _, c := range channels {
		var muted bool
		if err := st.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM notify_mutes WHERE address = ? AND channel = ?)`, address, c).Scan(&muted); err != nil {
			return false, err
		}
		open = open || !muted
	}
	if !open {
		return false, nil
	}
	sender, err := st.agent(a.sender)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !sender.Revoked && sender.Public.Fingerprint() == fp, nil
}

// complete removes the alerts a push covered (or that were dropped), only
// in the state they were read in: one that gained activity meanwhile, or
// was removed and made again, stays; after a push, it is due again no
// sooner than the grace.
func complete(st *store, address string, as []alert, now time.Time, sent bool) error {
	for _, a := range as {
		res, err := st.db.Exec(`DELETE FROM notify_pending WHERE `+sameState, a.state(address)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 && sent {
			if _, err := st.db.Exec(`UPDATE notify_pending SET due_ms = max(due_ms, ?), attempts = 0 WHERE address = ? AND channel = ? AND sender = ?`,
				now.Add(notifyGrace).UnixMilli(), address, a.channel, a.sender); err != nil {
				return err
			}
		}
	}
	return nil
}

// reschedule moves the alerts, in the state they were read in, to at; one
// changed meanwhile keeps its own deadline.
func reschedule(st *store, address string, as []alert, at time.Time) error {
	for _, a := range as {
		if _, err := st.db.Exec(`UPDATE notify_pending SET due_ms = ? WHERE `+sameState, append([]any{at.UnixMilli()}, a.state(address)...)...); err != nil {
			return err
		}
	}
	return nil
}

// retry schedules another attempt after a transient failure, or drops an
// alert that has had its attempts or would outlive its lifetime. The push
// service's Retry-After is a minimum and is never shortened; without one,
// the wait doubles up to pushMaxBackoff. Only alerts in the state they were
// read in are touched.
func retry(st *store, address string, as []alert, now time.Time, retryAfter time.Duration, status int, logf func(string, ...any)) error {
	dropped := 0
	for _, a := range as {
		attempts := a.attempts + 1
		wait := retryAfter
		if wait <= 0 {
			wait = min(pushMaxBackoff, notifyGrace<<attempts)
		}
		if attempts >= pushMaxAttempts || now.Add(wait).UnixMilli() >= a.expires {
			res, err := st.db.Exec(`DELETE FROM notify_pending WHERE `+sameState, a.state(address)...)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				dropped++
			}
			continue
		}
		if _, err := st.db.Exec(`UPDATE notify_pending SET attempts = ?, due_ms = ? WHERE `+sameState,
			append([]any{attempts, now.Add(wait).UnixMilli()}, a.state(address)...)...); err != nil {
			return err
		}
	}
	if dropped > 0 {
		logf("notifications for %s: %d alert(s) dropped after failed pushes (last HTTP %d)", address, dropped, status)
	}
	return nil
}

func distinctChannels(as []alert) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range as {
		if !seen[a.channel] {
			seen[a.channel] = true
			out = append(out, a.channel)
		}
	}
	return out
}

// take spends one of the device's push tokens, or says how long until one
// is there.
func (n *notifier) take(address string, now time.Time) time.Duration {
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[address]
	if b == nil {
		b = &bucket{tokens: pushBurst, at: now}
		n.buckets[address] = b
	}
	b.tokens = min(pushBurst, b.tokens+now.Sub(b.at).Seconds()/pushRefill.Seconds())
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return time.Duration((1 - b.tokens) * float64(pushRefill))
}
