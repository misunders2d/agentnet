package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// "Remind me later" (S-R; docs/MESSENGER_ARCHITECTURE.md §16): a personal,
// local reminder on one received message, due at a time the person chose.
// It only asks for attention: it never answers, accepts, declines or runs
// anything, never changes the message's own state, and nothing is sent.
// Only a reply to that very message, stored here by any reply path (a reply
// typed by hand or a decline, the worker's answer, a DM reply naming it),
// ends it, in the same step that stores the reply; a later message that
// does not reply to it, an automatic report of a failure, timeout or
// cancellation, a receipt or reading it does not.
//
// The daemon arms one wall timer on the earliest reminder whose attention
// was not yet asked for, and looks again after every local change (its own
// or another process's, which kicks it); nothing polls. Attention is asked
// for once per revision (setting a new time is a new revision): the
// revision is recorded first, then one content-free notification is asked
// for. A reminder past due stays overdue, shown by Reminders, whether or not
// the notification was shown. While the daemon is stopped nothing comes
// due; a reminder that came due then is overdue at its next start.

// Reminder states.
const (
	ReminderPending   = "pending"   // waiting for its time, or overdue
	ReminderDone      = "done"      // the person marked it done
	ReminderCancelled = "cancelled" // the person cancelled it
	ReminderReplied   = "replied"   // a reply to the message was stored here
)

// Reminder is a reminder on one received message.
type Reminder struct {
	Message string `json:"message"`        // the received message's id
	Conv    string `json:"conv,omitempty"` // its DM ("" : a direct message thread)
	From    string `json:"from"`           // its sender
	Due     int64  `json:"due"`            // unix seconds
	State   string `json:"state"`
	Overdue bool   `json:"overdue"` // pending and past its time
	Alerted bool   `json:"alerted"` // attention was asked for at its current time
}

// ErrNoReminder means there is no pending reminder on that message.
var ErrNoReminder = errors.New("no pending reminder on that message")

// SetReminder sets the reminder on received message id to due, or moves an
// existing one (a new revision: its attention is asked for again at the new
// time). due must be in the future.
func (a *Agent) SetReminder(id string, due time.Time) (Reminder, error) {
	now := time.Now()
	if !due.After(now) {
		return Reminder{}, errors.New("choose a time in the future")
	}
	if !protocol.ValidID(id) {
		return Reminder{}, fmt.Errorf("invalid message id %q", id)
	}
	var conv sql.NullString
	err := a.store.db.QueryRow(`SELECT conv FROM inbox WHERE id = ? AND local = 0`, id).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		return Reminder{}, fmt.Errorf("no received message %s", id)
	}
	if err != nil {
		return Reminder{}, err
	}
	_, err = a.store.db.Exec(`INSERT INTO reminders(message, conv, due_at, state, rev, notified_rev, created_at, updated_at)
		VALUES(?, ?, ?, ?, 1, 0, ?, ?)
		ON CONFLICT(message) DO UPDATE SET due_at = excluded.due_at, state = excluded.state, rev = rev + 1, updated_at = excluded.updated_at`,
		id, conv, due.Unix(), ReminderPending, now.Unix(), now.Unix())
	if err := a.store.done(err); err != nil {
		return Reminder{}, err
	}
	notifyDaemon(a.home) // a daemon in another process looks again
	r, _, err := a.Reminder(id)
	return r, err
}

// DoneReminder marks the pending reminder on id done; CancelReminder
// cancels it. Neither touches the message.
func (a *Agent) DoneReminder(id string) error { return a.endReminder(id, ReminderDone) }

// CancelReminder cancels the pending reminder on id.
func (a *Agent) CancelReminder(id string) error { return a.endReminder(id, ReminderCancelled) }

func (a *Agent) endReminder(id, state string) error {
	res, err := a.store.db.Exec(`UPDATE reminders SET state = ?, updated_at = ? WHERE message = ? AND state = ?`,
		state, time.Now().Unix(), id, ReminderPending)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNoReminder
	}
	a.store.changed()
	notifyDaemon(a.home)
	return nil
}

// replyEndsReminder ends the pending reminder on the message a reply being
// stored in tx answers (replyTo; "" : not a reply). An automatic report
// that the work failed, timed out or was cancelled (status) is not an
// answer: the reminder stays.
func replyEndsReminder(tx *sql.Tx, replyTo, status string) error {
	if replyTo == "" || status == envelope.StatusFailed || status == envelope.StatusTimeout || status == envelope.StatusCancelled {
		return nil
	}
	_, err := tx.Exec(`UPDATE reminders SET state = ?, updated_at = ? WHERE message = ? AND state = ?`,
		ReminderReplied, time.Now().Unix(), replyTo, ReminderPending)
	return err
}

const reminderCols = `r.message, coalesce(r.conv, ''), coalesce(i.sender, ''), r.due_at, r.state, r.notified_rev = r.rev`

func scanReminder(row interface{ Scan(...any) error }, now time.Time) (Reminder, error) {
	var r Reminder
	err := row.Scan(&r.Message, &r.Conv, &r.From, &r.Due, &r.State, &r.Alerted)
	r.Overdue = r.State == ReminderPending && r.Due <= now.Unix()
	return r, err
}

// Reminder returns the reminder on message id, if any.
func (a *Agent) Reminder(id string) (Reminder, bool, error) {
	r, err := scanReminder(a.store.db.QueryRow(`SELECT `+reminderCols+` FROM reminders r LEFT JOIN inbox i ON i.id = r.message
		WHERE r.message = ?`, id), time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// Reminders lists the pending reminders (overdue ones included), soonest
// first; with all, the ended ones too.
func (a *Agent) Reminders(all bool) ([]Reminder, error) {
	rows, err := a.store.db.Query(`SELECT `+reminderCols+` FROM reminders r LEFT JOIN inbox i ON i.id = r.message
		WHERE ? OR r.state = ? ORDER BY r.due_at, r.message`, all, ReminderPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := []Reminder{}
	for rows.Next() {
		r, err := scanReminder(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// remindLoop asks for attention when reminders come due, until ctx ends:
// one wall timer on the earliest reminder not yet alerted, re-armed after
// every local change.
func (a *Agent) remindLoop(ctx context.Context) {
	timer := newWallTimer()
	defer timer.Stop()
	for {
		_, changed := a.Changed() // before looking, so no change is missed
		next, err := a.remindDue(time.Now())
		if err != nil {
			a.Logf("reminders: %v", err)
			next = time.Now().Add(time.Minute) // a failure repeating does not spin
		}
		if err := timer.Arm(next); err != nil {
			a.Logf("reminders: timer: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
		case <-changed:
		}
	}
}

// remindDue records, for the reminders due at now whose attention was not
// asked for at their current time, that it now is; then asks for one
// content-free notification. It returns the next time a reminder comes due
// (zero: none).
func (a *Agent) remindDue(now time.Time) (time.Time, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT message, coalesce(conv, '') FROM reminders WHERE state = ? AND due_at <= ? AND notified_rev < rev
		ORDER BY due_at, message`, ReminderPending, now.Unix())
	if err != nil {
		return time.Time{}, err
	}
	type due struct{ message, conv string }
	var dues []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.message, &d.conv); err != nil {
			rows.Close()
			return time.Time{}, err
		}
		dues = append(dues, d)
	}
	rows.Close()
	if len(dues) > 0 {
		if _, err := tx.Exec(`UPDATE reminders SET notified_rev = rev WHERE state = ? AND due_at <= ? AND notified_rev < rev`,
			ReminderPending, now.Unix()); err != nil {
			return time.Time{}, err
		}
	}
	var next sql.NullInt64
	if err := tx.QueryRow(`SELECT min(due_at) FROM reminders WHERE state = ? AND notified_rev < rev`, ReminderPending).Scan(&next); err != nil {
		return time.Time{}, err
	}
	if len(dues) > 0 {
		if err := a.store.done(tx.Commit()); err != nil {
			return time.Time{}, err
		}
		body := "A reminder is due"
		var argv []string
		var onClick func()
		switch {
		case len(dues) > 1:
			body = fmt.Sprintf("%d reminders are due", len(dues))
			argv, onClick = a.convClick("") // no one target: the local page
		case dues[0].conv != "":
			argv, onClick = a.convClick(dues[0].conv) // the DM, on the local page
		default:
			argv, onClick = a.reviewClick(dues[0].message) // the message's whole thread
		}
		if err := a.notify("AgentNet", body, argv, onClick); err != nil {
			a.Logf("reminder notification not shown (%v); see agentnet remind list", err)
		}
	}
	if !next.Valid {
		return time.Time{}, nil
	}
	return time.Unix(next.Int64, 0), nil
}
