package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// InviteOptions describe an invitation an admin makes for a new person.
type InviteOptions struct {
	// Name is the invited person's name, written on the invitation and
	// used by the Hub to make their label when Label is empty.
	Name string
	// Label is the person label to give (the CLI's LABEL); empty lets the
	// Hub make a free one from Name.
	Label string
	TTL   time.Duration
	Admin bool
	// Workspace is the workspace's name written on the invitation.
	Workspace string
}

// InviteResult is an invitation made: the code, the label it gives and
// when it stops working.
type InviteResult struct {
	Code    string
	Label   string
	Expires time.Time
}

// CreateInvite makes one invitation for a new person (admin only): the
// kind a link opens in a browser and in the AgentNet app alike. It carries
// the person's name and, as the inviter, this installation's person's name
// (both unsigned hints). The Hub refuses before creating anything unless
// its page is served over HTTPS that browsers trust; the admin may still
// connect through an older pinned endpoint, whose pin says nothing about
// the endpoint in the new invite.
func (a *Agent) CreateInvite(ctx context.Context, o InviteOptions) (InviteResult, error) {
	if err := inviteTTL(o.TTL); err != nil {
		return InviteResult{}, err
	}
	if o.Label == "" && o.Name == "" {
		return InviteResult{}, errors.New("an invitation needs the person's name")
	}
	if !protocol.ValidInviteHint(o.Name, protocol.MaxInviteHint) {
		return InviteResult{}, fmt.Errorf("the name must be at most %d readable characters, without spaces around it", protocol.MaxInviteHint)
	}
	from := ""
	if me, ok, err := a.Person(); err == nil && ok && protocol.ValidInviteHint(me.Label, protocol.MaxInviteHint) {
		from = me.Label
	}
	workspace := ""
	if protocol.ValidInviteHint(o.Workspace, protocol.MaxWorkspaceHint) {
		workspace = o.Workspace
	}
	req := protocol.InviteRequest{Label: o.Label, Name: o.Name, From: from, Workspace: workspace, TTL: o.TTL, Admin: o.Admin, Browser: true}
	expires := time.Now().Add(o.TTL)
	var out protocol.InviteCreated
	if err := a.hub.do(ctx, "POST", "/v1/admin/invites", req, &out); err != nil {
		var he *HubError
		if errors.As(err, &he) && he.Status == 400 && o.Label == "" { // an older Hub needs a label and knows no names
			return InviteResult{}, fmt.Errorf("%w (your server needs an update to make invitations by name)", err)
		}
		if errors.As(err, &he) && he.Status == 409 {
			return InviteResult{}, fmt.Errorf("your server cannot make invitation links: it needs to serve its page over HTTPS that browsers trust (agentnet hub serve --web, without a certificate pin) (%w)", err)
		}
		return InviteResult{}, err
	}
	label := out.Label
	if label == "" {
		label = o.Label
	}
	return InviteResult{Code: out.Code, Label: label, Expires: expires}, nil
}

// Invites asks the Hub whether this device may invite people and, if it
// may, which invitations are still waiting to be used.
func (a *Agent) Invites(ctx context.Context) (protocol.PendingInvites, error) {
	var out protocol.PendingInvites
	if err := a.hub.do(ctx, "GET", "/v1/admin/invites", nil, &out); err != nil {
		return out, err
	}
	if out.Invites == nil {
		out.Invites = []protocol.PendingInvite{}
	}
	return out, nil
}

// RevokeInvite withdraws an unused invitation (admin only).
func (a *Agent) RevokeInvite(ctx context.Context, id string) error {
	return a.hub.do(ctx, "POST", "/v1/admin/invites/revoke", protocol.InviteRevokeRequest{ID: id}, nil)
}
