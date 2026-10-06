package ui

import (
	"context"
	"errors"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"net/http"
	"slices"
	"strings"
)

// GuestAction is separate from agent actions; receiving it grants no authority.
type GuestAction struct {
	Action string   `json:"-"`
	Conv   string   `json:"conv,omitempty"`
	Host   string   `json:"host,omitempty"`
	Share  []string `json:"share,omitempty"`
	Note   string   `json:"note,omitempty"`
	PID    string   `json:"pid,omitempty"`
	Accept *bool    `json:"accept,omitempty"`
}
type GuestView struct {
	NeedsUpdate     []string   `json:"needs_update,omitempty"`
	PID             string     `json:"pid"`
	State           string     `json:"state"`
	StateText       string     `json:"state_text"`
	Host            PersonView `json:"host"`
	HostHere        bool       `json:"host_here"`
	Inviter         PersonView `json:"inviter"`
	Shared          []string   `json:"shared"`
	Missing         int        `json:"missing"`
	Held            int        `json:"held"`
	CanDecide       bool       `json:"can_decide"`
	CanLeave        bool       `json:"can_leave"`
	CanEnd          bool       `json:"can_end"`
	CanSend         bool       `json:"can_send"`
	AudiencePending bool       `json:"audience_pending"`
}
type HumanParticipationProvider interface {
	ChangeHuman(context.Context, GuestAction) (GuestView, error)
}

// hostGuest is this device's own guest participation that the page acts on:
// the one it can send in, else one waiting for its decision, else the first.
// A guest invited back keeps its earlier, ended participations.
func hostGuest(views []GuestView) (GuestView, bool) {
	var found []GuestView
	for _, v := range views {
		if v.HostHere {
			found = append(found, v)
		}
	}
	if len(found) == 0 {
		return GuestView{}, false
	}
	for _, v := range found {
		if v.CanSend {
			return v, true
		}
	}
	for _, v := range found {
		if v.State == client.PartInvited {
			return v, true
		}
	}
	return found[0], true
}

// guestFrozen says why this guest device cannot send now, in the page's words.
func guestFrozen(v GuestView) string {
	if v.State == client.PartInvited {
		return "Join before sending."
	}
	return "Sending is unavailable here."
}

// originalViews are a guest's or visitor host's two verified original people
// (client.Conversations Members); a member's own DM view has none.
func originalViews(c client.ConversationInfo) []GroupMemberView {
	var out []GroupMemberView
	for _, p := range c.Members {
		out = append(out, GroupMemberView{PersonView: personView(p)})
	}
	return out
}

// guestEnd is how a dismissed guest participation ended, from its exact
// records held here (guestEnds).
type guestEnd struct {
	left    bool     // the guest's own device ended it
	unheard bool     // this device ended an accepted guest, and a device of the DM (a member's or the guest's) has not stored the end yet
	held    []string // then: its copies of the end that the server still holds
}

func guestView(info client.ParticipationInfo, member bool, end guestEnd) GuestView {
	v := GuestView{NeedsUpdate: info.NeedsUpdate, PID: info.PID, State: info.State, StateText: info.State, Host: personView(info.Host), HostHere: info.HostHere, Inviter: personView(info.Inviter), Shared: []string{}, Held: info.Held}
	for _, ref := range info.Grant {
		v.Shared = append(v.Shared, ref.LID)
	}
	v.CanDecide = info.HostHere && info.State == client.PartInvited && info.Held == 0
	v.CanLeave = info.HostHere && info.HumanActive()
	v.CanSend = info.HumanActive() && (member || info.HostHere)
	v.CanEnd = member && (info.State == client.PartInvited || info.State == client.PartActive || info.State == client.PartConflict)
	if info.State == client.PartDismissed {
		switch {
		case end.left:
			v.StateText = "Left this conversation. Previously shared copies remain."
		case end.unheard:
			v.StateText = "Ended here. Until every device in this conversation has stored the end, one that has not may still send to them. Previously shared copies remain."
			v.AudiencePending = true
		default:
			v.StateText = "Ended. Previously shared copies remain."
		}
	}
	if info.Held != 0 {
		v.StateText = "Waiting for verified participation evidence"
		v.AudiencePending = true
	}
	return v
}
func (l *Live) guestMember(conv string) bool {
	list, err := l.a.Conversations()
	if err != nil {
		return false
	}
	for _, c := range list {
		if c.ID == conv {
			return c.Role == "member" && c.Frozen == "" && (c.Kind == protocol.ConvKindDM || c.Kind == protocol.ConvKindGroup)
		}
	}
	return false
}
func (l *Live) guestViews(conv string) ([]GuestView, error) {
	infos, err := l.a.Participations(conv)
	if err != nil {
		return nil, err
	}
	ends, err := l.guestEnds(conv, infos)
	if err != nil {
		return nil, err
	}
	out := []GuestView{}
	member := l.guestMember(conv)
	for _, p := range infos {
		// A guest sees another guest only once its acceptance is held here.
		if p.Role == protocol.RoleHuman && (member || p.HostHere || p.Decision != "") {
			out = append(out, guestView(p, member, ends[p.PID]))
		}
	}
	return out, nil
}

// guestEnds says how each dismissed guest participation among infos ended,
// from conv's records held here. A guest who left applied that end before
// anyone else, and an invitation never accepted shared nothing: neither
// leaves anything pending. When a member ends an accepted guest, only the
// device that ended it has proof of when each device of the DM stored the
// end (its copy to that device delivered); until every one has, a device
// that has not applied the end may still send to the guest.
func (l *Live) guestEnds(conv string, infos []client.ParticipationInfo) (map[string]guestEnd, error) {
	ends := map[string]guestEnd{}
	dismissed := map[string]client.ParticipationInfo{}
	for _, p := range infos {
		if p.Role == protocol.RoleHuman && p.State == client.PartDismissed {
			dismissed[p.PID] = p
		}
	}
	if len(dismissed) == 0 {
		return ends, nil
	}
	msgs, err := l.a.ConversationMessages(conv)
	if err != nil {
		return nil, err
	}
	accepted := map[string]bool{}
	for _, m := range msgs {
		p, ok := dismissed[m.PID]
		if !ok || m.Sub != envelope.SubEvent {
			continue
		}
		ev, err := protocol.ParseParticipationEvent([]byte(m.Body))
		if err != nil {
			continue
		}
		switch {
		case ev.Type == protocol.EventAccept && ev.Hash() == p.Decision:
			accepted[p.PID] = true
		case ev.Type == protocol.EventDismiss && ev.Hash() == p.Dismissal:
			end := guestEnd{left: ev.Author.Person == p.Host.Person && ev.Author.Address == p.Host.Address && ev.Author.Fingerprint == p.Host.Fingerprint}
			if !end.left && m.Dir == "out" && m.Via == "" {
				end.unheard = len(m.Copies) == 0 && m.State != protocol.StateDelivered ||
					slices.ContainsFunc(m.Copies, func(c client.ConvCopy) bool { return c.State != protocol.StateDelivered })
				for _, c := range m.Copies {
					if c.ID != "" && c.State == protocol.StateCustody && !slices.Contains(end.held, c.ID) {
						end.held = append(end.held, c.ID)
					}
				}
			}
			ends[p.PID] = end
		}
	}
	for pid, end := range ends {
		end.unheard = end.unheard && accepted[pid]
		ends[pid] = end
	}
	return ends, nil
}
func (l *Live) ChangeHuman(ctx context.Context, c GuestAction) (GuestView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	var info client.ParticipationInfo
	var err error
	if c.Action != "invite" {
		info, err = l.a.Participation(c.PID)
		if err != nil {
			return GuestView{}, Refuse(l.updateSentence(err))
		}
		if info.Role != protocol.RoleHuman {
			return GuestView{}, Refuse("This action requires a human participation, not an agent.")
		}
	}
	switch c.Action {
	case "invite":
		info, err = l.a.InviteHuman(ctx, c.Conv, c.Host, c.Share, c.Note)
	case "decide":
		if c.Accept == nil {
			return GuestView{}, Refuse("Choose accept or decline.")
		}
		if *c.Accept {
			info, err = l.a.AcceptParticipation(ctx, c.PID)
		} else {
			info, err = l.a.DeclineParticipation(ctx, c.PID)
		}
	case "end":
		info, err = l.a.DismissParticipation(ctx, c.PID)
	default:
		err = errors.New("unknown human participation action")
	}
	if err != nil {
		return GuestView{}, Refuse(l.updateSentence(err))
	}
	ends, err := l.guestEnds(info.Conv, []client.ParticipationInfo{info})
	if err != nil {
		return GuestView{}, err
	}

	return guestView(info, l.guestMember(info.Conv), ends[info.PID]), nil
}
func (s *Server) changeHuman(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var c GuestAction
		if !readJSON(w, r, &c) {
			return
		}
		c.Action = action
		valid := false
		switch action {
		case "invite":
			valid = protocol.ValidHash(c.Conv) && c.Host != "" && c.PID == "" && c.Accept == nil
		case "decide":
			valid = protocol.ValidID(c.PID) && c.Accept != nil && c.Conv == "" && c.Host == "" && len(c.Share) == 0 && c.Note == ""
		case "end":
			valid = protocol.ValidID(c.PID) && c.Accept == nil && c.Conv == "" && c.Host == "" && len(c.Share) == 0 && c.Note == ""
		}
		if !valid {
			writeErr(w, Refuse("Choose one exact human participation action."))
			return
		}
		p, ok := s.p.(HumanParticipationProvider)
		if !ok {
			writeErr(w, Refuse("Human participation is unavailable on this provider."))
			return
		}
		result, err := p.ChangeHuman(r.Context(), c)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, result)
	}
}

type GuestCheckPerson struct {
	Label string `json:"label"`
	Me    bool   `json:"me"`
	Role  string `json:"role"`
}
type GuestCheck struct {
	Ready       bool               `json:"ready"`
	NeedsUpdate []GuestCheckPerson `json:"needs_update"`
	Offline     []string           `json:"offline"`
	Text        string             `json:"text"`
}
type GuestCheckRequest struct {
	Conv string `json:"conv"`
	Host string `json:"host"`
}
type HumanInviteChecker interface {
	CheckHuman(context.Context, GuestCheckRequest) (GuestCheck, error)
}

func (l *Live) CheckHuman(ctx context.Context, c GuestCheckRequest) (GuestCheck, error) {
	support, err := l.a.HumanInviteSupport(ctx, c.Conv, c.Host)
	if err != nil {
		return GuestCheck{}, Refuse(l.updateSentence(err))
	}
	return guestCheckOf(support), nil
}

func guestCheckOf(support []client.HumanSupport) GuestCheck {
	v := GuestCheck{Ready: true, NeedsUpdate: []GuestCheckPerson{}, Offline: []string{}}
	var text []string
	for _, p := range support {
		if p.State == "update" || p.State == "not set up" {
			v.Ready = false
			v.NeedsUpdate = append(v.NeedsUpdate, GuestCheckPerson{Label: p.Label, Me: p.Me, Role: p.Role})
			action := "an AgentNet update"
			if p.State == "not set up" {
				action = "AgentNet set up"
			}
			switch p.Role {
			case "me":
				text = append(text, "One of your other devices needs "+action+".")
			case "member":
				text = append(text, p.Label+"'s app needs "+action+" to keep this chat working with a guest. That person's copy waits until then.")
			case "guest":
				text = append(text, p.Label+"'s app needs "+action+" before joining. The invitation waits until then.")
			}
		} else if p.State == "offline" && p.Role == "guest" {
			v.Offline = append(v.Offline, p.Label)
			text = append(text, p.Label+" is not connected now; the invitation reaches them when their app reconnects.")
		}
	}
	v.Text = strings.Join(text, " ")
	return v
}

func (s *Server) checkHuman(w http.ResponseWriter, r *http.Request) {
	var c GuestCheckRequest
	if !readJSON(w, r, &c) {
		return
	}
	p, ok := s.p.(HumanInviteChecker)
	if !ok {
		writeErr(w, Refuse("This app cannot check the invitation yet."))
		return
	}
	v, err := p.CheckHuman(r.Context(), c)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}
