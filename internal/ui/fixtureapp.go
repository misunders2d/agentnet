package ui

import (
	"slices"
	"strings"
	"time"
)

// The demo's invitations and folders (Invitations, Folders): invented,
// kept in memory, so the demo and the skins' contract and rendered checks
// reach the same routes as the AgentNet app.

type fxInvites struct {
	list []PendingInviteView
	next int
}

func (f *Fixture) invitesLocked() *fxInvites {
	v := f.invites
	if v == nil {
		now := f.now()
		v = &fxInvites{list: []PendingInviteView{{ID: "demo-invite-1", Name: "Dana", Label: "dana", By: f.me.Address,
			Created: now.Add(-2 * time.Hour), Expires: now.Add(7*24*time.Hour - 2*time.Hour)}}}
		f.invites = v
	}
	return v
}

// Invite implements Invitations with an invented link.
func (f *Fixture) Invite(r InviteRequest) (InviteView, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return InviteView{}, Refuse("Write the name of the person you invite.")
	}
	if !slices.Contains(InviteDays, r.Days) {
		return InviteView{}, Refuse("Choose how long the link works: 1, 7 or 30 days.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.invitesLocked()
	v.next++
	label := strings.ToLower(strings.Fields(name)[0])
	now := f.now()
	expires := now.Add(time.Duration(r.Days) * 24 * time.Hour)
	v.list = append([]PendingInviteView{{ID: "demo-invite-new-" + string(rune('a'+v.next%26)), Name: name, Label: label, Admin: r.Admin,
		By: f.me.Address, Created: now, Expires: expires}}, v.list...)
	link := "https://agentnet.example/#agentnet-invite-v1:demo-only-not-a-real-invitation"
	from := ""
	if f.person != nil {
		from = f.person.Label
	}
	return InviteView{Link: link, Label: label, Expires: expires, Message: InviteMessage(from, link)}, nil
}

// Invites implements Invitations: the demo device is an admin.
func (f *Fixture) Invites() (InvitesView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return InvitesView{CanInvite: true, Invites: append([]PendingInviteView{}, f.invitesLocked().list...)}, nil
}

// RevokeInvite implements Invitations.
func (f *Fixture) RevokeInvite(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.invitesLocked()
	for i, p := range v.list {
		if p.ID == id {
			v.list = append(v.list[:i], v.list[i+1:]...)
			return nil
		}
	}
	return NotFound("That invitation was already used, withdrawn or expired.")
}

// Folders implements Folders with an invented home.
func (f *Fixture) Folders(path string) (FoldersView, error) {
	const home = "/home/alice"
	tree := map[string][]string{
		home:                   {"Documents", "projects", "a folder with a rather long name to see how it wraps on a phone"},
		home + "/projects":     {"api", "website", "agentnet"},
		home + "/Documents":    {},
		home + "/projects/api": {},
	}
	if path == "" {
		path = home
	}
	kids, ok := tree[path]
	if !ok {
		return FoldersView{}, NotFound("That folder cannot be opened here.")
	}
	v := FoldersView{Path: path, Home: home, Dirs: []FolderView{}}
	if path != "/" {
		v.Parent = path[:strings.LastIndexByte(path, '/')]
		if v.Parent == "" {
			v.Parent = "/"
		}
	}
	for _, k := range kids {
		v.Dirs = append(v.Dirs, FolderView{Name: k, Path: path + "/" + k})
	}
	return v, nil
}
