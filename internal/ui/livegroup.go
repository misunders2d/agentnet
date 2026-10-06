package ui

import (
	"context"
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Groups exposes explicit human actions over the client's existing signed
// group operations. Reading or rendering never consents or publishes.
type Groups interface {
	GroupInvitations() ([]GroupInvitationView, error)
	NewGroup(context.Context, string) (string, error)
	InviteGroup(context.Context, GroupInviteDraft) (GroupInvitationView, error)
	DecideGroup(context.Context, string, bool) error
	PublishGroup(context.Context, string) error
	CancelGroup(context.Context, string) error
	RefreshGroup(context.Context, string) (GroupInvitationView, error)
	ManageGroup(context.Context, GroupChange) (GroupChangeResult, error)
}

type GroupChange struct {
	Conv   string `json:"conv"`
	Action string `json:"action"`
	Title  string `json:"title,omitempty"`
	Person string `json:"person,omitempty"`
}
type GroupChangeResult struct {
	Queued bool `json:"queued"`
}

func (l *Live) ManageGroup(ctx context.Context, c GroupChange) (GroupChangeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	var err error
	var out GroupChangeResult
	switch c.Action {
	case "rename":
		if c.Person != "" {
			return out, Refuse("Rename takes only a title.")
		}
		_, err = l.a.RenameGroup(ctx, c.Conv, c.Title)
	case "promote", "demote", "remove":
		if c.Title != "" || c.Person == "" {
			return out, Refuse("Choose the exact current member.")
		}
		switch c.Action {
		case "promote":
			_, err = l.a.PromoteGroupMember(ctx, c.Conv, c.Person)
		case "demote":
			_, err = l.a.DemoteGroupMember(ctx, c.Conv, c.Person)
		case "remove":
			_, err = l.a.RemoveGroupMember(ctx, c.Conv, c.Person)
		}
	case "leave":
		if c.Person != "" || c.Title != "" {
			return out, Refuse("Leave refers only to this group.")
		}
		var r client.GroupLeaveResult
		r, err = l.a.LeaveGroup(ctx, c.Conv)
		out.Queued = r.Queued
	default:
		return out, Refuse("Unknown group action.")
	}
	if err != nil {
		return out, Refuse(sentence(err))
	}
	return out, nil
}

type GroupMemberView struct {
	PersonView
	Admin bool `json:"admin"`
}

type GroupInvitationView struct {
	ID         string                     `json:"id"`
	Conv       string                     `json:"conv"`
	Direction  string                     `json:"direction"`
	State      string                     `json:"status"`
	Title      string                     `json:"title"`
	Inviter    string                     `json:"inviter"`
	Target     string                     `json:"target"`
	History    []protocol.GroupHistoryRef `json:"history"`
	CanCancel  bool                       `json:"can_cancel,omitempty"`
	CanRefresh bool                       `json:"can_refresh,omitempty"`
}

type GroupInviteDraft struct {
	Conv    string `json:"conv"`
	Person  string `json:"person"`
	History struct {
		Last  int                        `json:"last,omitempty"`
		Since int64                      `json:"since,omitempty"`
		Refs  []protocol.GroupHistoryRef `json:"refs,omitempty"`
	} `json:"history"`
}

func groupInvitationView(i client.GroupInvitationInfo) GroupInvitationView {
	return GroupInvitationView{ID: i.ID, Conv: i.Proposal.State.Conv, Direction: i.Direction, State: i.State, Title: i.Proposal.State.Title, Inviter: i.Inviter, Target: i.Proposal.Target, History: i.Proposal.History, CanCancel: i.CanCancel, CanRefresh: i.CanRefresh}
}

func (l *Live) GroupInvitations() ([]GroupInvitationView, error) {
	rows, err := l.a.GroupInvitations()
	if err != nil {
		return nil, err
	}
	result := []GroupInvitationView{}
	for _, row := range rows {
		result = append(result, groupInvitationView(row))
	}
	return result, nil
}

func (l *Live) NewGroup(ctx context.Context, title string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	packet, err := l.a.CreateGroup(ctx, title)
	if err != nil {
		return "", Refuse(sentence(err))
	}
	return packet.Root.ID(), nil
}

func (l *Live) InviteGroup(ctx context.Context, draft GroupInviteDraft) (GroupInvitationView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	refs, err := l.a.SelectGroupHistory(ctx, draft.Conv, client.GroupHistorySelection{Last: draft.History.Last, Since: draft.History.Since, Refs: draft.History.Refs})
	if err != nil {
		return GroupInvitationView{}, Refuse(sentence(err))
	}
	i, err := l.a.InviteGroup(ctx, draft.Conv, draft.Person, refs)
	if err != nil {
		return GroupInvitationView{}, Refuse(sentence(err))
	}
	return groupInvitationView(i), nil
}

func (l *Live) DecideGroup(ctx context.Context, id string, accept bool) error {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if err := l.a.DecideGroupInvitation(ctx, id, accept); err != nil {
		return Refuse(sentence(err))
	}
	return nil
}

func (l *Live) PublishGroup(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if err := l.a.PublishGroupInvitation(ctx, id); err != nil {
		return Refuse(sentence(err))
	}
	return nil
}

func (l *Live) CancelGroup(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if err := l.a.CancelGroupInvitation(ctx, id); err != nil {
		return Refuse(sentence(err))
	}
	return nil
}

func (l *Live) RefreshGroup(ctx context.Context, id string) (GroupInvitationView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	i, err := l.a.RefreshGroupInvitation(ctx, id)
	if err != nil {
		return GroupInvitationView{}, Refuse(sentence(err))
	}
	return groupInvitationView(i), nil
}

func (l *Live) groupMembers(c client.ConversationInfo) ([]GroupMemberView, error) {
	packet, err := l.a.GroupContext(c.ID)
	if err != nil {
		return nil, err
	}
	result := []GroupMemberView{}
	for _, p := range c.Members {
		m, ok := packet.State.Member(p.Person)
		if !ok {
			return nil, errors.New("group member projection differs from verified context")
		}
		result = append(result, GroupMemberView{PersonView: personView(p), Admin: m.Admin})
	}
	return result, nil
}

func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Groups)
	if !ok {
		writeErr(w, NotFound("groups are unavailable from this provider"))
		return
	}
	var v any
	var err error
	switch r.URL.Path {
	case "/api/groups/invitations":
		v, err = p.GroupInvitations()
	case "/api/groups/new":
		var d struct {
			Title string `json:"title"`
		}
		if !readJSON(w, r, &d) {
			return
		}
		var id string
		id, err = p.NewGroup(r.Context(), d.Title)
		v = map[string]string{"id": id}
	case "/api/groups/invite":
		var d GroupInviteDraft
		if !readJSON(w, r, &d) {
			return
		}
		v, err = p.InviteGroup(r.Context(), d)
	case "/api/groups/decide":
		var d struct {
			ID     string `json:"id"`
			Accept *bool  `json:"accept"`
		}
		if !readJSON(w, r, &d) {
			return
		}
		if d.Accept == nil {
			writeErr(w, Refuse("Choose accept or decline explicitly."))
			return
		}
		err = p.DecideGroup(r.Context(), d.ID, *d.Accept)
		v = map[string]bool{"recorded": err == nil}
	case "/api/groups/publish":
		var d struct {
			ID string `json:"id"`
		}
		if !readJSON(w, r, &d) {
			return
		}
		err = p.PublishGroup(r.Context(), d.ID)
		v = map[string]bool{"published": err == nil}
	case "/api/groups/cancel", "/api/groups/refresh":
		var d struct {
			ID string `json:"id"`
		}
		if !readJSON(w, r, &d) {
			return
		}
		if r.URL.Path == "/api/groups/cancel" {
			err = p.CancelGroup(r.Context(), d.ID)
			v = map[string]bool{"cancelled": err == nil}
		} else {
			v, err = p.RefreshGroup(r.Context(), d.ID)
		}
	case "/api/groups/manage":
		var d GroupChange
		if !readJSON(w, r, &d) {
			return
		}
		v, err = p.ManageGroup(r.Context(), d)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}
