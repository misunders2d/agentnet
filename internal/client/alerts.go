package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Optional chat alerts on this desktop (docs/revival/NOTIFY.md §8). The daemon
// holds the stream and decrypts, so it decides locally: a verified chat turn
// meant for a person (asksAttention), in a chat not muted, with alerts on,
// queues an
// alert in the message's own admission transaction. A later message of that
// conversation adds to it without moving its deadline. The local page
// reporting that it presented the newest message cancels it. At the
// deadline the alert is removed (recorded as shown) and then a native
// notification is asked for, with fixed, content-free text: a crash in
// between loses that alert rather than showing it twice, and the OS may
// still drop, delay or hide it.

// alertGrace is how long an alert waits for the local page's presentation.
var alertGrace = 5 * time.Second

// Bounds of the local preferences.
const (
	maxAlertSenders = protocol.MaxNotifySenders
	maxAlertMutes   = protocol.MaxNotifyMutes
)

// AlertPrefs are this desktop's alert preferences.
type AlertPrefs struct {
	Enabled bool                    `json:"enabled"`
	Senders []protocol.NotifySender `json:"senders"` // exact keys whose DM turns may alert here
	Mutes   []string                `json:"mutes"`   // conversation ids that never alert
}

// AlertPrefs returns this desktop's alert preferences (off by default).
func (a *Agent) AlertPrefs() (AlertPrefs, error) {
	p := AlertPrefs{Senders: []protocol.NotifySender{}, Mutes: []string{}}
	on, err := a.store.config("alerts")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.Enabled = on == "on"
	rows, err := a.store.db.Query(`SELECT address, fingerprint FROM alert_senders ORDER BY address`)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var s protocol.NotifySender
		if err := rows.Scan(&s.Address, &s.Fingerprint); err != nil {
			rows.Close()
			return p, err
		}
		p.Senders = append(p.Senders, s)
	}
	rows.Close()
	rows, err = a.store.db.Query(mutedChatConvs + ` ORDER BY conv`)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return p, err
		}
		p.Mutes = append(p.Mutes, c)
	}
	return p, rows.Err()
}

// SetAlertPrefs replaces this desktop's alert preferences. Turning alerts
// off drops the pending ones; a muted conversation's or a removed sender's
// pending alert is dropped when due.
func (a *Agent) SetAlertPrefs(p AlertPrefs) error {
	if len(p.Senders) > maxAlertSenders || len(p.Mutes) > maxAlertMutes {
		return errors.New("alerts: too many senders or muted conversations")
	}
	seen := map[string]bool{}
	for _, s := range p.Senders {
		if _, _, err := protocol.SplitAddress(s.Address); err != nil || !protocol.ValidFingerprint(s.Fingerprint) || seen[s.Address] {
			return errors.New("alerts: invalid or repeated sender")
		}
		seen[s.Address] = true
	}
	for _, c := range p.Mutes {
		if !protocol.ValidHash(c) || seen[c] {
			return errors.New("alerts: invalid or repeated conversation")
		}
		seen[c] = true
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	on := "off"
	if p.Enabled {
		on = "on"
	}
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT OR REPLACE INTO config(k, v) VALUES('alerts', ?)`, []any{on}},
		{`DELETE FROM alert_senders`, nil},
		{`DELETE FROM alert_mutes`, nil},
	}
	if !p.Enabled {
		stmts = append(stmts, struct {
			q    string
			args []any
		}{`DELETE FROM alerts`, nil})
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s.q, s.args...); err != nil {
			return err
		}
	}
	for _, s := range p.Senders {
		if _, err := tx.Exec(`INSERT INTO alert_senders(address, fingerprint) VALUES(?, ?)`, s.Address, s.Fingerprint); err != nil {
			return err
		}
	}
	for _, c := range p.Mutes {
		if _, err := tx.Exec(`INSERT INTO alert_mutes(conv) VALUES(?)`, c); err != nil {
			return err
		}
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.wakeAlerts()
	return nil
}

// AlertPresented records that the local page presented these messages of
// conv (visible, focused, that conversation, its newest message in view):
// the pending alert whose newest message is among them is cancelled.
// Nothing else cancels an alert.
func (a *Agent) AlertPresented(conv string, ids []string) error {
	if !protocol.ValidHash(conv) || len(ids) == 0 || len(ids) > protocol.MaxNotifySeenIDs {
		return fmt.Errorf("alerts: a conversation and 1-%d message ids", protocol.MaxNotifySeenIDs)
	}
	args := []any{conv}
	for _, id := range ids {
		if !protocol.ValidID(id) {
			return errors.New("alerts: invalid message id")
		}
		args = append(args, id)
	}
	_, err := a.store.db.Exec(`DELETE FROM alerts WHERE conv = ? AND last_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	return err
}

// mutedChatConvs derives person-chat mutes from existing signed member roots.
// Cleared histories retain their roots and quiet anchors; guest/group roots stay exact.
const mutedChatConvs = `SELECT conv FROM alert_mutes
UNION SELECT c.id FROM conversations c JOIN conversations anchor ON anchor.peer=c.peer
JOIN alert_mutes m ON m.conv=anchor.id JOIN persons p ON p.person=c.peer AND p.state='pinned'
WHERE c.kind='dm' AND anchor.kind='dm'
AND EXISTS (SELECT 1 FROM json_each(c.root,'$.members') j JOIN persons own ON own.person=json_extract(j.value,'$.person') AND own.state='self')
AND EXISTS (SELECT 1 FROM json_each(anchor.root,'$.members') j JOIN persons own ON own.person=json_extract(j.value,'$.person') AND own.state='self')`

// queueAlert queues (or adds to) the alert for in, a chat message just
// admitted within tx, verified by the key with fingerprint verifiedBy, if
// it asks for attention and the person's preferences allow it.
func queueAlert(tx *sql.Tx, in envelope.Inner, verifiedBy string, now time.Time) error {
	if !asksAttention(in) {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(`SELECT
		EXISTS (SELECT 1 FROM config WHERE k = 'alerts' AND v = 'on')
		AND (EXISTS (SELECT 1 FROM group_context WHERE conv=?) OR EXISTS (SELECT 1 FROM person_devices d JOIN persons p ON p.person=d.person WHERE d.address=? AND d.fingerprint=? AND p.state=?))
		AND ? NOT IN (`+mutedChatConvs+`)`, in.Conv, in.From, verifiedBy, personPinned, in.Conv).Scan(&ok); err != nil || !ok {
		return err
	}
	_, err := tx.Exec(`INSERT INTO alerts(conv, sender, last_id, count, due_ms) VALUES(?, ?, ?, 1, ?)
		ON CONFLICT(conv) DO UPDATE SET sender = excluded.sender, last_id = excluded.last_id, count = count + 1`,
		in.Conv, in.From, in.ID, now.Add(alertGrace).UnixMilli())
	return err
}

// wakeAlerts makes the alert loop look again (nothing waits outside Run).
func (a *Agent) wakeAlerts() {
	select {
	case a.alertWake <- struct{}{}:
	default:
	}
}

// alertLoop shows due alerts until ctx ends: one timer on the earliest
// deadline, woken by changes; nothing polls.
func (a *Agent) alertLoop(ctx context.Context, wake <-chan struct{}) {
	for {
		next, err := a.showDueAlerts(time.Now())
		if err != nil {
			a.Logf("alerts: %v", err)
			next = time.Now().Add(time.Minute) // a failure repeating does not spin
		}
		var due <-chan time.Time
		var timer *time.Timer
		if !next.IsZero() {
			timer = time.NewTimer(max(0, time.Until(next)))
			due = timer.C
		}
		select {
		case <-ctx.Done():
		case <-wake:
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

// showDueAlerts shows one notification for the alerts due at now, still
// eligible, and returns the next deadline (zero: none). The alerts are
// removed before the notification is asked for.
func (a *Agent) showDueAlerts(now time.Time) (time.Time, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT l.conv,
		EXISTS (SELECT 1 FROM config WHERE k = 'alerts' AND v = 'on')
		AND l.conv NOT IN (`+mutedChatConvs+`)
		AND (EXISTS (SELECT 1 FROM group_context WHERE conv=l.conv) OR EXISTS (SELECT 1 FROM person_devices d JOIN persons p ON p.person = d.person WHERE d.address = l.sender AND p.state = ?))
		FROM alerts l WHERE l.due_ms <= ?`, personPinned, now.UnixMilli())
	if err != nil {
		return time.Time{}, err
	}
	var show []string
	due := 0
	for rows.Next() {
		var conv string
		var ok bool
		if err := rows.Scan(&conv, &ok); err != nil {
			rows.Close()
			return time.Time{}, err
		}
		due++
		if ok {
			show = append(show, conv)
		}
	}
	rows.Close()
	if due > 0 {
		if _, err := tx.Exec(`DELETE FROM alerts WHERE due_ms <= ?`, now.UnixMilli()); err != nil {
			return time.Time{}, err
		}
	}
	var next sql.NullInt64
	if err := tx.QueryRow(`SELECT min(due_ms) FROM alerts`).Scan(&next); err != nil {
		return time.Time{}, err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return time.Time{}, err
	}
	if len(show) > 0 {
		conv := ""
		if len(show) == 1 {
			conv = show[0]
		}
		if a.nativeNotify != nil {
			fragment := ""
			if protocol.ValidHash(conv) {
				fragment = "conv=" + conv
			}
			a.nativeNotify(fragment)
		} else {
			argv, onClick := a.convClick(conv)
			if err := a.notify("AgentNet", "New activity", argv, onClick); err != nil {
				a.Logf("alert not shown (%v)", err)
			}
		}
	}
	if !next.Valid {
		return time.Time{}, nil
	}
	return time.UnixMilli(next.Int64), nil
}

// convClick returns what clicking an alert does: open the local page on
// conv ("" : the page itself) where the platform takes clicks and the page
// is served; nil otherwise.
func (a *Agent) convClick(conv string) (argv []string, onClick func()) {
	switch {
	case a.openPage != nil: // the AgentNet app's window (RunOptions.OpenPage)
		fragment := ""
		if protocol.ValidHash(conv) {
			fragment = "conv=" + conv
		}
		argv = a.openPage(fragment)
	case a.openConv != nil:
		argv = a.openConv(conv)
	}
	if len(argv) == 0 {
		return nil, nil
	}
	return argv, func() {
		if err := a.launch(argv); err != nil {
			a.Logf("alert click: %v", err)
		}
	}
}

// Older preferences did not distinguish never allowed from explicitly denied.
// Preserve quiet configured DM chats once; new chats and groups default unmuted.
const chatAlertDefaultsSchema = `
INSERT OR IGNORE INTO alert_mutes(conv)
SELECT c.id FROM conversations c
WHERE c.kind='dm' AND EXISTS (SELECT 1 FROM config WHERE k='alerts')
AND NOT EXISTS (SELECT 1 FROM person_devices d JOIN alert_senders s ON s.address=d.address AND s.fingerprint=d.fingerprint WHERE d.person=c.peer);
`
