package ui

import (
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// "Remind me later" on the daemon's page (S-R), through the client's
// reminder methods only. A reminder asks for attention and changes nothing
// about the message or the sender.

// reminders are the pending reminders, soonest first, with each message's
// first line.
func (l *Live) reminders() ([]ReminderView, error) {
	rs, err := l.a.Reminders(false)
	if err != nil {
		return nil, err
	}
	out := make([]ReminderView, 0, len(rs))
	for _, r := range rs {
		out = append(out, ReminderView{Message: r.Message, Conv: r.Conv, From: r.From, Title: l.firstLineOf(r), Due: time.Unix(r.Due, 0), Overdue: r.Overdue})
	}
	return out, nil
}

// firstLineOf finds the reminded message's first line ("" if it is not held).
func (l *Live) firstLineOf(r client.Reminder) string {
	if r.Conv != "" {
		msgs, err := l.a.ConversationMessages(r.Conv)
		if err == nil {
			for _, m := range msgs {
				if m.ID == r.Message {
					return firstLine(m.Body)
				}
			}
		}
		return ""
	}
	if t, err := l.Thread(r.Message); err == nil {
		for _, m := range t.Messages {
			if m.ID == r.Message {
				return firstLine(m.Body)
			}
		}
	}
	return ""
}

func (l *Live) reminderResult(err error) error {
	if err == nil {
		l.a.NoteChange()
		return nil
	}
	if errors.Is(err, client.ErrNoReminder) {
		return NotFound("that message has no pending reminder")
	}
	return Refuse(sentence(err))
}

// SetReminder implements Reminders.
func (l *Live) SetReminder(id string, due time.Time) error {
	_, err := l.a.SetReminder(id, due)
	return l.reminderResult(err)
}

// DoneReminder implements Reminders.
func (l *Live) DoneReminder(id string) error { return l.reminderResult(l.a.DoneReminder(id)) }

// CancelReminder implements Reminders.
func (l *Live) CancelReminder(id string) error { return l.reminderResult(l.a.CancelReminder(id)) }
