package ui

import (
	"slices"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Optional DM alerts on this computer (docs/revival/NOTIFY.md §8), through
// the client's alert preferences only. The daemon decides and shows the
// native notification; this page turns alerts on and off, mutes DMs,
// allows people, and reports the messages it actually presented.

// notifyView is the overview's alert state.
func (l *Live) notifyView() (*NotifyView, error) {
	p, err := l.a.AlertPrefs()
	if err != nil {
		return nil, err
	}
	v := &NotifyView{Available: true, Native: true, Enabled: p.Enabled, Mutes: p.Mutes, Allowed: []string{}}
	for _, s := range p.Senders {
		v.Allowed = append(v.Allowed, s.Address)
	}
	return v, nil
}

// decided are the people this computer's person started a DM with: their
// exact keys may alert once alerts are on. An arrival adds nobody.
func (l *Live) decided() ([]protocol.NotifySender, error) {
	convs, err := l.a.Conversations()
	if err != nil {
		return nil, err
	}
	var out []protocol.NotifySender
	for _, c := range convs {
		if c.Creator == l.a.Address && c.Peer.State == PersonPinned && !slices.ContainsFunc(out, func(s protocol.NotifySender) bool { return s.Address == c.Peer.Address }) {
			out = append(out, protocol.NotifySender{Address: c.Peer.Address, Fingerprint: c.Peer.Fingerprint})
		}
	}
	return out, nil
}

func (l *Live) setAlerts(change func(*client.AlertPrefs) error) error {
	p, err := l.a.AlertPrefs()
	if err != nil {
		return err
	}
	if err := change(&p); err != nil {
		return err
	}
	if err := l.a.SetAlertPrefs(p); err != nil {
		return Refuse(sentence(err))
	}
	l.a.NoteChange()
	return nil
}

// NotifyEnable implements Alerts.
func (l *Live) NotifyEnable() (string, error) {
	return "Alerts are on.", l.setAlerts(func(p *client.AlertPrefs) error {
		d, err := l.decided()
		if err != nil {
			return err
		}
		for _, s := range d {
			if !slices.ContainsFunc(p.Senders, func(x protocol.NotifySender) bool { return x.Address == s.Address }) {
				p.Senders = append(p.Senders, s)
			}
		}
		p.Enabled = true
		return nil
	})
}

// NotifyDisable implements Alerts.
func (l *Live) NotifyDisable() (string, error) {
	return "Alerts are off.", l.setAlerts(func(p *client.AlertPrefs) error { p.Enabled = false; return nil })
}

// NotifyMute implements Alerts.
func (l *Live) NotifyMute(conv string, muted bool) (string, error) {
	convs, err := l.a.Conversations()
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(convs, func(c client.ConversationInfo) bool { return c.ID == conv }) {
		return "", NotFound("no conversation with that id")
	}
	note := "This DM notifies you again."
	if muted {
		note = "This DM is muted."
	}
	return note, l.setAlerts(func(p *client.AlertPrefs) error {
		p.Mutes = slices.DeleteFunc(p.Mutes, func(c string) bool { return c == conv })
		if muted {
			p.Mutes = append(p.Mutes, conv)
		}
		return nil
	})
}

// NotifyAllow implements Alerts: the person's exact current key.
func (l *Live) NotifyAllow(person string, allowed bool) (string, error) {
	known, err := l.a.KnownPersons()
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(known, func(p client.PersonInfo) bool { return p.Person == person && person != "" })
	if i < 0 || known[i].State != PersonPinned {
		return "", Refuse("That person is not known here, or their record is frozen.")
	}
	who := known[i]
	note := "Alerts from them are off."
	if allowed {
		note = "Alerts from them are on."
	}
	return note, l.setAlerts(func(p *client.AlertPrefs) error {
		p.Senders = slices.DeleteFunc(p.Senders, func(s protocol.NotifySender) bool { return s.Address == who.Address })
		if allowed {
			p.Senders = append(p.Senders, protocol.NotifySender{Address: who.Address, Fingerprint: who.Fingerprint})
		}
		return nil
	})
}

// NotifySeen implements Alerts: the newest messages the page presented.
func (l *Live) NotifySeen(conv string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > protocol.MaxNotifySeenIDs {
		ids = ids[len(ids)-protocol.MaxNotifySeenIDs:]
	}
	if err := l.a.AlertPresented(conv, ids); err != nil {
		return Refuse(sentence(err))
	}
	return nil
}
