package ui

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// InviteMessage is the ready-made message an admin sends with an
// invitation link; from is the inviter's name ("" when unknown). The
// browser engine writes the same words (engine.mjs inviteMessage).
func InviteMessage(from, link string) string {
	if from == "" {
		return "You're invited to AgentNet. Open this link to get the app and join: " + link
	}
	return from + " invited you to AgentNet. Open this link to get the app and join: " + link
}

// Invite implements Invitations: one invitation link for a new person.
func (l *Live) Invite(r InviteRequest) (InviteView, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return InviteView{}, Refuse("Write the name of the person you invite.")
	}
	if !protocol.ValidInviteHint(name, protocol.MaxInviteHint) {
		return InviteView{}, Refuse("Use a shorter name (up to 64 characters) without line breaks.")
	}
	if !slices.Contains(InviteDays, r.Days) {
		return InviteView{}, Refuse("Choose how long the link works: 1, 7 or 30 days.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	// TODO(integrate:P2): pass the workspace's name (WorkspaceName) as the
	// invitation's workspace hint once P2 is merged.
	inv, err := l.a.CreateInvite(ctx, client.InviteOptions{Name: name, TTL: time.Duration(r.Days) * 24 * time.Hour, Admin: r.Admin})
	if err != nil {
		return InviteView{}, Refuse(inviteWords(err))
	}
	link, err := protocol.InviteLink(inv.Code)
	if err != nil {
		return InviteView{}, Refuse("The invitation was made, but your server cannot give it as a link: " + err.Error())
	}
	from := ""
	if me, ok, err := l.a.Person(); err == nil && ok {
		from = me.Label
	}
	return InviteView{Link: link, Label: inv.Label, Expires: inv.Expires, Message: InviteMessage(from, link)}, nil
}

// inviteWords is a refused invitation in the admin's words.
func inviteWords(err error) string {
	var he *client.HubError
	if errors.As(err, &he) && he.Status == http.StatusForbidden {
		return "Only an admin of your server can invite people."
	}
	return err.Error()
}

// Invites implements Invitations. Whether this device may invite is the
// Hub's answer for this device (an admin device).
// TODO(integrate:P2): use P2's shared canAdmin helper here once merged.
func (l *Live) Invites() (InvitesView, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	p, err := l.a.Invites(ctx)
	if err != nil {
		return InvitesView{}, Refuse("Your server could not be asked about invitations: " + err.Error())
	}
	return invitesView(p), nil
}

func invitesView(p protocol.PendingInvites) InvitesView {
	v := InvitesView{CanInvite: p.CanInvite, Invites: []PendingInviteView{}}
	for _, i := range p.Invites {
		pv := PendingInviteView{ID: i.ID, Name: i.Name, Label: i.Label, Admin: i.Admin, By: i.CreatedBy, Expires: time.Unix(i.Expires, 0).UTC()}
		if i.CreatedAt > 0 {
			pv.Created = time.Unix(i.CreatedAt, 0).UTC()
		}
		v.Invites = append(v.Invites, pv)
	}
	return v
}

// RevokeInvite implements Invitations.
func (l *Live) RevokeInvite(id string) error {
	if id == "" {
		return Refuse("Choose an invitation.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if err := l.a.RevokeInvite(ctx, id); err != nil {
		var he *client.HubError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return NotFound("That invitation was already used, withdrawn or expired.")
		}
		return Refuse(inviteWords(err))
	}
	return nil
}
