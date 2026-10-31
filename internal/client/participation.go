package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Agent participation in a DM: signed invite, accept, decline and dismiss
// records, their resolution into a state, and views. Running a request to
// a participation's agent is agentjob.go's.
//
// Resolution: a participation's state is computed from the set of events
// held, never from the order they arrived in, and with the DM's persons as
// pinned now. An event counts only if its author is the device of a member
// person pinned (or this installation's own) with the roster the root names
// and not frozen; an invite also needs its host to be such a device and its
// task keys to be member keys, or in a DM own devices its author's signed
// roster step listed and removed since (those count for no task). Anything
// else is held: it has no effect until the evidence is there, and then
// counts without being delivered again.

// ErrNoParticipation means no event of that participation is held here.
var ErrNoParticipation = errors.New("no such agent participation here")

// Participation states.
const (
	PartPending   = "pending"   // events held, but no invite that counts (yet)
	PartInvited   = "invited"   // invited; the host's person has not decided
	PartActive    = "active"    // accepted by the host, with exactly the invite's scope
	PartDeclined  = "declined"  // declined by the host (or it both accepted and declined)
	PartConflict  = "conflict"  // different invites for one id, or more than one host decision: nothing runs
	PartDismissed = "dismissed" // ended by a member; for good (a new invite is a new participation)
)

// ParticipationInfo is one participation as this installation resolves it.
type ParticipationInfo struct {
	Parent      string                   `json:"parent,omitempty"`
	Snapshots   []protocol.GrantSnapshot `json:"snapshots,omitempty"`
	Topic       *string                  `json:"topic,omitempty"`
	NeedsUpdate []string                 `json:"needs_update,omitempty"`
	Role        string                   `json:"role,omitempty"`
	PID         string                   `json:"pid"`
	Conv        string                   `json:"conv"`
	State       string                   `json:"state"`
	Host        PersonInfo               `json:"host"`               // the host device's person (its device runs the agent)
	HostHere    bool                     `json:"host_here"`          // this installation is the host
	External    bool                     `json:"external,omitempty"` // host person is outside the unchanged DM membership
	AgentID     string                   `json:"agent_id,omitempty"` // host-signed agent selected by the invite
	Inviters    []PersonInfo             `json:"inviters,omitempty"`
	Shares      []string                 `json:"-"`
	Member      bool                     `json:"member,omitempty"`
	Inviter     PersonInfo               `json:"inviter"`
	Grant       []protocol.GrantRef      `json:"grant,omitempty"`     // earlier messages the agent may be given, each exactly
	TaskKeys    []string                 `json:"task_keys,omitempty"` // member keys allowed follow-up tasks here
	Note        string                   `json:"note,omitempty"`
	Invite      string                   `json:"invite,omitempty"`    // the invite's hash
	Scope       string                   `json:"scope,omitempty"`     // its counted public projection (protocol.EventScope), if held
	Decision    string                   `json:"decision,omitempty"`  // the accept or decline that decided it
	Dismissal   string                   `json:"dismissal,omitempty"` // the dismiss that ended it
	Conflict    string                   `json:"conflict,omitempty"`  // why a conflict (or a declined fork) was resolved so
	Held        int                      `json:"held"`                // events that do not count here now
	HeldDismiss int                      `json:"held_dismiss"`        // of those, dismissals
	Invited     int64                    `json:"invited,omitempty"`   // the inviter's claim, unix seconds
	Audience    string                   `json:"audience,omitempty"`  // protocol.AudienceConversation or AudienceRoom, as invited
	Until       int64                    `json:"until,omitempty"`     // a room's end time as invited; past it here, it counts as ended

	OwnerInvited bool `json:"owner_invited,omitempty"` // current human owner already supplied consent; presentation, never execution authority
}

// dmMembers are a DM's member persons as pinned here now, by person id: a
// person frozen or missing, or whose pinned chain does not contain the
// roster the root binds it to, is left out. Their devices are the current
// ones; chains holds each person's pinned roster steps.
type dmMembers struct {
	historyEvents     []protocol.ParticipationEvent // verified history-only evidence, never persisted as live authority
	historyTaskEpochs map[string]string             // original signed task keys, only while verifying inert history
	root              protocol.ConvRoot
	persons           map[string]personRow
	hosts             map[string]personRow // verified invite hosts; never member/asker/task authority
	group             *GroupContext        // verified current context; never ordinary visitor authority
	groupInvites      map[string]bool
	roomEvents        map[string]bool
	shareGrants       map[string][]protocol.GrantRef
	roomAuthors       map[string]personRow
	guestParents      map[string]ParticipationInfo // parent acceptance hash -> exact resolved human parent, operation-local
	chains            map[string]map[string]bool
	departedTaskKeys  map[string]map[string]bool // DM invite hash -> task keys its author's signed step listed, removed since (loadDepartedTaskKeys)
}

func (a *Agent) dmMembers(conv string) (dmMembers, error) { return membersIn(a.store.db, conv) }

// membersIn reads a DM's members from q (the store, or a transaction that
// must decide on the members as they are within it).
func membersIn(q dbq, conv string) (dmMembers, error) {
	root, _, found, err := conversationIn(q, conv)
	if err != nil {
		return dmMembers{}, err
	}
	if !found {
		return dmMembers{}, fmt.Errorf("no conversation %s here", conv)
	}
	if root.Kind == protocol.ConvKindGroup {
		return controlMembers(q, conv)
	}
	return memberRowsIn(q, root, root.Members)
}

// memberRowsIn shares pinned-roster resolution; its caller supplies verified
// membership, which is immutable for DMs and current effective state for groups.
func memberRowsIn(q dbq, root protocol.ConvRoot, members []protocol.ConvMember) (dmMembers, error) {
	return memberRowsKnownIn(q, root, members, nil)
}

// known belongs only to the caller's current read snapshot; other callers reread.
func memberRowsKnownIn(q dbq, root protocol.ConvRoot, members []protocol.ConvMember, known map[string]personRow) (dmMembers, error) {
	q, closeReads := prepareMembershipReads(q)
	defer closeReads()
	m := dmMembers{root: root, persons: map[string]personRow{}, hosts: map[string]personRow{}, chains: map[string]map[string]bool{}}
	for _, mem := range members {
		p, ok := known[mem.Person]
		if known == nil {
			var err error
			p, ok, err = personByIDIn(q, mem.Person)
			if err != nil {
				return dmMembers{}, err
			}
		}
		if !ok || p.info.State != personSelf && p.info.State != personPinned {
			continue
		}
		chain := map[string]bool{}
		rows, err := q.Query(`SELECT hash FROM person_chain WHERE person = ?`, mem.Person)
		if err != nil {
			return dmMembers{}, err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return dmMembers{}, err
			}
			chain[h] = true
		}
		rows.Close()
		if chain[mem.Roster] {
			m.persons[mem.Person], m.chains[mem.Person] = p, chain
		}
	}
	return m, nil
}

// author reports whether au names a current device of a member person,
// with a roster step of its pinned chain.
func (m dmMembers) author(au protocol.EventAuthor) (personRow, bool) {
	p, ok := m.persons[au.Person]
	return p.at(au.Address), ok && m.chains[au.Person][au.Roster] && p.has(au.Address, au.Fingerprint) && m.authorEpoch(au)
}

// host reports whether h names a current device of a member person.
func (m dmMembers) host(h *protocol.ParticipationHost) (personRow, bool) {
	if h == nil {
		return personRow{}, false
	}
	p, ok := m.persons[h.Person]
	if !ok {
		p, ok = m.hosts[h.Person]
	}
	return p.at(h.Address), ok && p.has(h.Address, h.Fingerprint)
}

// device reports whether address with key fingerprint fp is a current
// device of a member person as pinned here now.
func (m dmMembers) device(address, fp string) bool {
	for _, p := range m.persons {
		if p.has(address, fp) {
			return true
		}
	}
	return false
}

// memberKey reports whether fp is the key of a current device of a member.
func (m dmMembers) memberKey(fp string) bool {
	for _, p := range m.persons {
		if _, ok := p.roster.Device(fp); ok {
			return true
		}
	}
	return m.historyTaskEpochs[fp] != ""
}

// loadDepartedTaskKeys finds, for each DM invite whose author counts here,
// the task keys that are no current member's but were devices of the
// author's person in the very roster step the invite was signed under (a
// verified step of the pinned chain): own devices removed since. resolve
// lets such an invite count without them, so its participation's history
// stays readable while the removed key has no task authority. A key that
// step does not list (another person's, one never the author's, one added
// later or not pinned here yet) still holds the invite. Groups bind task
// keys to admissions instead (verifyInviteEpoch, room events).
func (m *dmMembers) loadDepartedTaskKeys(q dbq, events []protocol.ParticipationEvent) error {
	m.departedTaskKeys = map[string]map[string]bool{}
	if m.group != nil {
		return nil
	}
	steps := map[string]*protocol.PersonRoster{}
	for _, ev := range events {
		if ev.Type != protocol.EventInvite {
			continue
		}
		if _, ok := m.author(ev.Author); !ok {
			continue
		}
		for _, fp := range ev.TaskKeys {
			if m.memberKey(fp) {
				continue
			}
			step, read := steps[ev.Author.Person+"/"+ev.Author.Roster]
			if !read {
				var raw string
				switch err := q.QueryRow(`SELECT record FROM person_chain WHERE person=? AND hash=?`, ev.Author.Person, ev.Author.Roster).Scan(&raw); {
				case err == nil:
					if r, e := protocol.ParsePersonRoster([]byte(raw)); e == nil && r.Person == ev.Author.Person && r.Hash() == ev.Author.Roster {
						step = &r
					}
				case !errors.Is(err, sql.ErrNoRows):
					return err
				}
				steps[ev.Author.Person+"/"+ev.Author.Roster] = step
			}
			if step == nil {
				continue
			}
			if _, listed := step.Device(fp); listed {
				if m.departedTaskKeys[ev.Hash()] == nil {
					m.departedTaskKeys[ev.Hash()] = map[string]bool{}
				}
				m.departedTaskKeys[ev.Hash()][fp] = true
			}
		}
	}
	return nil
}

// resolve computes a participation from its events and the DM's members,
// independently of the order the events arrived in:
//
//   - events whose author does not count here (unknown, not a member,
//     frozen, another roster or key) are held and change nothing;
//   - no invite that counts: pending; more than one: conflict;
//   - accept or decline counts only by the invite's host, answering that
//     invite; one decision decides it; more than one distinct decision (any
//     mix, even two accepts) is a conflict, which never runs;
//   - a dismiss by either member counts if it follows an event that counts
//     here; it ends the participation for good, whatever else is held;
//   - an event following one not held (yet) is held until it arrives;
//   - a room invitation's Until, by this device's clock, ends it once past
//     (ROOM_V1 §2.2); it orders nothing.
//
// Held events are counted (Held; HeldDismiss for dismissals): while any is
// held the participation is not Claimable, since the missing evidence may
// end or change it.
func resolve(conv, pid string, events []protocol.ParticipationEvent, m dmMembers) ParticipationInfo {
	info := ParticipationInfo{PID: pid, Conv: conv, State: PartPending}
	hold := func(ev protocol.ParticipationEvent) {
		info.Held++
		if ev.Type == protocol.EventDismiss {
			info.HeldDismiss++
		}
	}
	invites := map[string]protocol.ParticipationEvent{}
	var decisions, dismisses, scopes, shares []protocol.ParticipationEvent
	for _, ev := range events {
		_, author := m.author(ev.Author)
		if ev.Parent != "" {
			_, author = m.guestAuthor(ev)
		}
		if !author && m.roomEvents[ev.Hash()] {
			_, author = m.roomAuthor(ev.Author)
		}
		if ev.Type == protocol.EventAccept || ev.Type == protocol.EventDecline {
			p, ok := m.hosts[ev.Author.Person]
			author = author || ok && m.chains[ev.Author.Person][ev.Author.Roster] && p.has(ev.Author.Address, ev.Author.Fingerprint) && ev.Author.GroupAdmission == ""
		}
		if ev.Type == protocol.EventDismiss && !author {
			for _, invite := range events {
				if invite.Type != protocol.EventInvite && invite.Type != protocol.EventScope || invite.Host == nil || invite.Role != protocol.RoleHuman && !(invite.Audience == protocol.AudienceRoom && invite.Group != nil && invite.Group.HostRole == "visitor") {
					continue
				}
				_, inviter := m.author(invite.Author)
				if !inviter && m.roomEvents[invite.Hash()] {
					_, inviter = m.roomAuthor(invite.Author)
				}
				_, host := m.host(invite.Host)
				author = author || inviter && host && ev.Author.Person == invite.Host.Person && ev.Author.Address == invite.Host.Address && ev.Author.Fingerprint == invite.Host.Fingerprint && m.chains[ev.Author.Person][ev.Author.Roster]
			}
		}
		if len(ev.Snapshots) > 0 {
			person, ok := m.author(ev.Author)
			if ev.Parent != "" {
				person, ok = m.guestAuthor(ev)
			}
			author = author && ok && person.roster.Human(ev.Author.Fingerprint)
		}
		if !author {
			hold(ev)
			continue
		}
		switch ev.Type {
		case protocol.EventInvite:
			keysOK := true
			for _, fp := range ev.TaskKeys {
				keysOK = keysOK && (m.memberKey(fp) || m.departedTaskKeys[ev.Hash()][fp])
			}
			if _, ok := m.host(ev.Host); !ok || !keysOK && !m.roomEvents[ev.Hash()] || !m.inviteEpoch(ev) {
				hold(ev)
				continue
			}
			invites[ev.Hash()] = ev
		case protocol.EventShare:
			shares = append(shares, ev)
		case protocol.EventDismiss:
			dismisses = append(dismisses, ev)
		case protocol.EventScope:
			if m.group == nil || m.groupInvites[ev.Hash()] { // an invite's public projection, by its own author; in a group, a room scope whose binding verifies
				scopes = append(scopes, ev)
			}
		default:
			decisions = append(decisions, ev)
		}
	}
	// Without the invite itself (another guest, an outside assistant host), its
	// author's scope stands for it: host, agent and role, never grant, task
	// keys or note. Holding the invite, only its exact projection counts; an
	// invite held here that does not count is never stood in for.
	if len(invites) == 0 && len(scopes) > 0 {
		var s protocol.ParticipationEvent
		for _, x := range scopes {
			if s.Type == "" || x.Hash() < s.Hash() {
				s = x
			}
		}
		agree := true
		for _, x := range scopes {
			agree = agree && protocol.SameScope(x, s)
		}
		held := false
		for _, ev := range events {
			held = held || ev.Type == protocol.EventInvite && ev.Hash() == s.Prev
		}
		if _, ok := m.host(s.Host); ok && agree && !held {
			invites[s.Prev] = protocol.ParticipationEvent{V: 1, Conv: s.Conv, PID: s.PID, Type: protocol.EventInvite, Author: s.Author, TS: s.TS, Host: s.Host, Audience: s.Audience, Group: s.Group, Role: s.Role, Until: s.Until, Topic: s.Topic, Parent: s.Parent}
			info.Scope = s.Hash()
		} else if !agree {
			info.State, info.Conflict = PartConflict, "different invitation scopes share this participation id"
			return info
		} else {
			hold(s)
		}
	}
	known := map[string]bool{}
	for h := range invites {
		known[h] = true
	}
	var inv protocol.ParticipationEvent
	switch len(invites) {
	case 0:
	case 1:
		for info.Invite, inv = range invites {
		}
		if info.Scope == "" {
			for _, s := range scopes {
				if s.Projects(inv) && (info.Scope == "" || s.Hash() < info.Scope) {
					info.Scope = s.Hash()
				}
			}
		}
		host, _ := m.host(inv.Host)
		inviter, valid := m.author(inv.Author)
		if inv.Parent != "" {
			inviter, valid = m.guestAuthor(inv)
		}
		if !valid {
			inviter, _ = m.roomAuthor(inv.Author)
		}
		info.Host, info.Inviter = host.info, inviter.info
		info.OwnerInvited = inv.Role == "" && valid && inv.Author.Person == inv.Host.Person && inviter.roster.Human(inv.Author.Fingerprint)
		info.AgentID, info.Role = inv.Host.AgentID, inv.Role
		_, member := m.persons[inv.Host.Person]
		info.External = !member
		info.Grant, info.TaskKeys, info.Note, info.Invited = inv.Grant, inv.TaskKeys, inv.Note, inv.TS
		info.Snapshots = slices.Clone(inv.Snapshots)
		if inv.Group != nil && m.roomEvents[inv.Hash()] {
			info.TaskKeys = nil
			for i, key := range inv.TaskKeys {
				if i < len(inv.Group.TaskAdmissions) && m.keyEpoch(key) == inv.Group.TaskAdmissions[i] {
					info.TaskKeys = append(info.TaskKeys, key)
				}
			}
		} else if departed := m.departedTaskKeys[info.Invite]; len(departed) > 0 {
			// The signed invite still counts; a device removed since asks nothing.
			info.TaskKeys = slices.DeleteFunc(slices.Clone(inv.TaskKeys), func(fp string) bool { return departed[fp] })
		} // durable room consent never revives a standing task grant after re-admission
		info.Audience, info.Until, info.Topic, info.Parent = inv.Audience, inv.Until, inv.Topic, inv.Parent
		info.State = PartInvited
	default:
		info.State, info.Conflict = PartConflict, "different invites share this participation id"
	}
	if inv.Host != nil {
		info.Member = m.group != nil && inv.Role == "" && inv.Audience == protocol.AudienceRoom && inv.Until == 0 && inv.Parent == ""
		info.Inviters = []PersonInfo{info.Inviter}
		for _, ev := range shares {
			if ev.Prev != info.Invite || ev.Host == nil || *ev.Host != *inv.Host || !m.inviteEpoch(ev) || ev.Audience != inv.Audience || !sameParticipationTopic(ev.Topic, inv.Topic) {
				hold(ev)
				continue
			}
			info.Shares = append(info.Shares, ev.Hash())
			mergeGrantSnapshots(&info, ev.Snapshots)
			p, valid := m.author(ev.Author)
			if !valid {
				p, _ = m.roomAuthor(ev.Author)
			}
			if !slices.ContainsFunc(info.Inviters, func(x PersonInfo) bool { return x.Person == p.info.Person }) {
				info.Inviters = append(info.Inviters, p.info)
			}
			for _, g := range m.shareGrants[ev.Hash()] {
				if !slices.Contains(info.Grant, g) {
					info.Grant = append(info.Grant, g)
				}
			}
		}
	}
	var decided []protocol.ParticipationEvent
	for _, ev := range decisions {
		if info.State != PartInvited || ev.Prev != info.Invite || ev.Author.Person != inv.Host.Person ||
			ev.Author.Address != inv.Host.Address || ev.Author.Fingerprint != inv.Host.Fingerprint {
			hold(ev) // for an invite not held (yet), or not by its host
			continue
		}
		decided = append(decided, ev)
		known[ev.Hash()] = true
	}
	slices.SortFunc(decided, func(x, y protocol.ParticipationEvent) int { return strings.Compare(x.Hash(), y.Hash()) })
	switch {
	case len(decided) == 1 && decided[0].Type == protocol.EventAccept:
		info.State, info.Decision = PartActive, decided[0].Hash()
	case len(decided) == 1:
		info.State, info.Decision = PartDeclined, decided[0].Hash()
	case len(decided) > 1:
		info.State, info.Decision, info.Conflict = PartConflict, decided[0].Hash(), "the host decided more than once"
	}
	for _, ev := range dismisses {
		_, memberAuthor := m.author(ev.Author)
		hostEnd := (inv.Role == protocol.RoleHuman || info.Member && info.External) && ev.Author.Person == inv.Host.Person && ev.Author.Address == inv.Host.Address && ev.Author.Fingerprint == inv.Host.Fingerprint
		if !memberAuthor && !m.roomEvents[ev.Hash()] && !hostEnd || !m.roomEvents[ev.Hash()] && !m.mayRemoveAgent(info, ev.Author) {
			hold(ev)
			continue
		}
		if !known[ev.Prev] {
			hold(ev) // follows an event not held here (yet)
			continue
		}
		if info.State != PartDismissed || ev.Hash() < info.Dismissal {
			info.Dismissal = ev.Hash()
		}
		info.State = PartDismissed
	}
	if info.Until > 0 && (info.State == PartInvited || info.State == PartActive) && time.Now().Unix() > info.Until {
		info.State = PartDismissed // ended by its own end time, here: no dismissal names it
	}
	if inv.Parent != "" && !m.guestParents[inv.Parent].HumanActive() && (info.State == PartInvited || info.State == PartActive) {
		info.State = PartDismissed
	}
	return info
}

// Claimable reports whether the participation could have work run now: it
// is active and no event of it is held (a held one may end or change it).
func (p ParticipationInfo) Claimable() bool {
	return p.Role != protocol.RoleHuman && p.State == PartActive && p.Held == 0
}
func (p ParticipationInfo) HumanActive() bool {
	return p.Role == protocol.RoleHuman && p.State == PartActive && p.Held == 0
}

// follows reports whether the participation is of the room's captured
// audience, in whatever state: a human guest, a group participant, or an
// invited DM helper. This is read access, never conversation membership.
func (p ParticipationInfo) follows() bool {
	return p.Role == protocol.RoleHuman || p.Audience == protocol.AudienceRoom || p.Role == "" && p.Invite != "" && p.Audience == protocol.AudienceConversation
}

// Following reports whether the participation is in the room's captured
// audience now (ROOM_V1 §1): it follows, is active, and no event of it is
// held.
func (p ParticipationInfo) Following() bool {
	return p.follows() && p.State == PartActive && p.Held == 0
}

// Participation resolves one participation.
func (a *Agent) Participation(pid string) (ParticipationInfo, error) {
	conv, err := a.store.participationConv(pid)
	if err != nil {
		return ParticipationInfo{}, err
	}
	return a.participation(conv, pid)
}

func (a *Agent) participation(conv, pid string) (ParticipationInfo, error) {
	m, err := a.dmMembers(conv)
	if err != nil {
		return ParticipationInfo{}, err
	}
	return participationIn(a.store.db, conv, pid, m, a.Address)
}

// participationIn resolves a participation from the events q holds, with
// the DM's members m; self is this installation's address.
func participationIn(q dbq, conv, pid string, m dmMembers, self string) (ParticipationInfo, error) {
	var events []protocol.ParticipationEvent
	var err error
	if m.historyEvents != nil {
		events = mergeHistoryEvents(nil, m.historyEvents, pid)
	} else {
		events, err = participationEventsIn(q, conv, pid)
		if err != nil {
			return ParticipationInfo{}, err
		}
	}
	if len(events) == 0 {
		return ParticipationInfo{}, ErrNoParticipation
	}
	if err := m.loadHosts(q, events); err != nil {
		return ParticipationInfo{}, err
	}
	info := resolve(conv, pid, events, m)
	if m.historyEvents == nil {
		info, err = closedParticipation(q, conv, pid, events, m, info)
		if err != nil {
			return ParticipationInfo{}, err
		}
	}
	info.HostHere = info.Host.Address == self && info.Host.State == personSelf
	if info.Role == protocol.RoleHuman {
		rows, err := q.Query(`SELECT DISTINCT coalesce(p.label,'Someone') FROM outbox o LEFT JOIN person_devices d ON d.address=o.recipient LEFT JOIN persons p ON p.person=d.person WHERE o.pid=? AND o.state='waiting' AND o.required_cap=? AND instr(coalesce(o.error,''),?)=1`, pid, protocol.CapHumanParticipation, WaitPeerUpdate)
		if err != nil {
			return ParticipationInfo{}, err
		}
		for rows.Next() {
			var label string
			if err = rows.Scan(&label); err != nil {
				rows.Close()
				return ParticipationInfo{}, err
			}
			info.NeedsUpdate = append(info.NeedsUpdate, label)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return ParticipationInfo{}, err
		}
	}
	return info, nil
}

// Participations resolves every participation of conv.
func (a *Agent) Participations(conv string) ([]ParticipationInfo, error) {
	pids, err := a.store.participationIDs(conv)
	if err != nil {
		return nil, err
	}
	out := make([]ParticipationInfo, 0, len(pids))
	for _, pid := range pids {
		info, err := a.participation(conv, pid)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// InviteAgent invites the agent hosted on hostAddress, a member's device,
// into conv. grant names earlier messages of conv held here, by logical id;
// each is recorded exactly (with its sender's key) and must be one message:
// nothing else earlier is given. taskKeys names the member keys
// (fingerprints) that may ask it for follow-up tasks within this
// participation. The host's person decides; nothing runs before that (their
// own agent's invite from a device they trust is their decision: selfConsent).
func (a *Agent) InviteAgent(ctx context.Context, conv, hostAddress string, grant, taskKeys []string, note string) (ParticipationInfo, error) {
	return a.inviteAgent(ctx, conv, hostAddress, "", grant, taskKeys, note)
}

// InviteNamedAgent selects a host-signed identity. The host still accepts the
// participation and resolves its own local program; the caller chooses no program.
func (a *Agent) InviteNamedAgent(ctx context.Context, conv, hostAddress, agentID string, grant, taskKeys []string, note string) (ParticipationInfo, error) {
	if !protocol.ValidID(agentID) {
		return ParticipationInfo{}, ErrUnknownAgent
	}
	ctx = withRoundTrips(ctx) // the catalog's key check is the invitation's
	records, err := a.AgentCatalog(ctx, hostAddress)
	if err != nil {
		return ParticipationInfo{}, err
	}
	for _, record := range records {
		if record.ID == agentID {
			return a.inviteAgent(ctx, conv, hostAddress, agentID, grant, taskKeys, note)
		}
	}
	return ParticipationInfo{}, ErrUnknownAgent
}

func (a *Agent) inviteAgent(ctx context.Context, conv, hostAddress, agentID string, grant, taskKeys []string, note string) (ParticipationInfo, error) {
	return a.inviteParticipation(ctx, conv, hostAddress, agentID, grant, taskKeys, note, "")
}
func (a *Agent) InviteHuman(ctx context.Context, conv, hostAddress string, grant []string, note string) (ParticipationInfo, error) {
	return a.inviteParticipation(ctx, conv, hostAddress, "", grant, nil, note, protocol.RoleHuman)
}
func (a *Agent) inviteParticipation(ctx context.Context, conv, hostAddress, agentID string, grant, taskKeys []string, note, role string, topics ...*string) (ParticipationInfo, error) {
	ctx = withRoundTrips(ctx)
	var topic *string
	if len(topics) > 0 {
		topic = topics[0]
	}
	if topic != nil && *topic != "" && !protocol.ValidID(*topic) {
		return ParticipationInfo{}, errors.New("invalid participation topic")
	}
	m, err := a.dmMembers(conv)
	if err != nil {
		return ParticipationInfo{}, err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return ParticipationInfo{}, err
	}
	var parent ParticipationInfo
	if _, member := m.persons[me.info.Person]; !ok || !member {
		if !ok || role != "" || topic == nil || *topic == "" || len(taskKeys) != 0 || !me.roster.Human(me.info.Fingerprint) {
			return ParticipationInfo{}, errors.New("this installation does not speak for a member or exact human topic guest")
		}
		infos, err := a.Participations(conv)
		if err != nil {
			return ParticipationInfo{}, err
		}
		for _, p := range infos {
			if p.HumanActive() && p.HostHere && p.Host.Fingerprint == me.info.Fingerprint && sameParticipationTopic(p.Topic, topic) {
				if parent.PID != "" {
					return ParticipationInfo{}, errors.New("human topic guest is ambiguous")
				}
				parent = p
			}
		}
		if parent.PID == "" {
			return ParticipationInfo{}, errors.New("this installation has no exact active human topic participation")
		}
	}
	var host *protocol.ParticipationHost
	for _, p := range m.persons {
		if d, ok := p.device(hostAddress); ok {
			host = &protocol.ParticipationHost{Person: p.info.Person, Address: d.Address, Fingerprint: d.Fingerprint(), AgentID: agentID}
		}
	}
	if host == nil {
		key, err := a.sendKey(ctx, hostAddress)
		if err != nil {
			return ParticipationInfo{}, err
		}
		p, err := a.personOfKey(ctx, hostAddress, key)
		if err != nil {
			return ParticipationInfo{}, err
		}
		if !p.has(hostAddress, key.Fingerprint()) || p.info.State != personPinned && p.info.State != personSelf {
			return ParticipationInfo{}, errors.New("host has no current pinned person/device proof")
		}
		if role != protocol.RoleHuman {
			if err := a.requireParticipationCaps(ctx, key, protocol.CapExternalParticipation); err != nil {
				return ParticipationInfo{}, err
			}
		}
		host = &protocol.ParticipationHost{Person: p.info.Person, Address: hostAddress, Fingerprint: key.Fingerprint(), AgentID: agentID}
	}
	if parent.PID != "" && host.Person != me.info.Person {
		return ParticipationInfo{}, errors.New("a human guest can bring only their own agent")
	}
	if parent.PID != "" && host.Address != a.Address {
		key, e := a.sendKey(ctx, host.Address)
		if e != nil {
			return ParticipationInfo{}, e
		}
		if e = a.requireParticipationCaps(ctx, key, protocol.CapGuestAgent); e != nil {
			return ParticipationInfo{}, e
		}
	}
	if role == protocol.RoleHuman {
		if !humanRoom(m) {
			return ParticipationInfo{}, errors.New("human guests require a verified DM or group")
		}
		if _, original := m.persons[host.Person]; original {
			return ParticipationInfo{}, errors.New("that person already belongs to this DM")
		}
		key, err := a.sendKey(ctx, host.Address)
		if err != nil {
			return ParticipationInfo{}, err
		}
		if key.Fingerprint() != host.Fingerprint {
			return ParticipationInfo{}, errors.New("human host key changed")
		}
		if m.group != nil {
			if err := a.requireParticipationCaps(ctx, key, protocol.CapGroupHumanParticipation); err != nil {
				return ParticipationInfo{}, err
			}
		}
		if err := a.requireParticipationCaps(ctx, key, protocol.CapHumanParticipation); err != nil && !errors.Is(err, errAgentIdentityUnsupported) {
			return ParticipationInfo{}, err
		}
		// Only the invited host must support joining. Other readers' exact
		// copies wait independently at their existing capability gates.
		infos, err := a.Participations(conv)
		if err != nil {
			return ParticipationInfo{}, err
		}
		count := 0
		for _, existing := range infos {
			if existing.Role == protocol.RoleHuman && (existing.State == PartActive || existing.State == PartInvited) {
				count++
				if existing.Host.Address == host.Address && sameParticipationTopic(existing.Topic, topic) {
					return ParticipationInfo{}, errors.New("this human already has a pending or active invitation; end it before inviting again")
				}
			}
		}
		if count >= envelope.MaxHumanAudience {
			return ParticipationInfo{}, errors.New("human audience limit reached")
		}
	}
	if topic != nil {
		if err := a.requireTopicHost(ctx, m, *host); err != nil {
			return ParticipationInfo{}, err
		}
	}
	var refs []protocol.GrantRef
	for _, lid := range grant {
		if parent.PID != "" {
			h, e := a.guestSelectedItem(a.store.db, parent, lid)
			if e != nil {
				return ParticipationInfo{}, e
			}
			refs = append(refs, protocol.GrantRef{LID: h.LID, Fingerprint: h.FromKey})
			continue
		}
		keys, err := a.store.convLIDKeys(conv, lid, me.info.Fingerprint)
		if err != nil {
			return ParticipationInfo{}, err
		}
		switch len(keys) {
		case 0:
			return ParticipationInfo{}, fmt.Errorf("no message %s of this conversation here to share", lid)
		case 1:
			refs = append(refs, protocol.GrantRef{LID: lid, Fingerprint: keys[0]})
		default:
			return ParticipationInfo{}, fmt.Errorf("more than one message here has logical id %s; it cannot be shared by that id", lid)
		}
	}
	var snapshots []protocol.GrantSnapshot
	var snapshotClaim func(*sql.Tx) error
	if parent.PID == "" {
		snapshots, snapshotClaim, err = a.prepareGrantSnapshots(conv, topic, refs)
	}
	if err != nil {
		return ParticipationInfo{}, err
	}
	if len(snapshots) > 0 && host.Address != a.Address {
		key, e := a.sendKey(ctx, host.Address)
		if e != nil {
			return ParticipationInfo{}, e
		}
		if e = a.requireParticipationCaps(ctx, key, protocol.CapTopicContext); e != nil {
			return ParticipationInfo{}, e
		}
	}
	if err := a.checkTopicGrants(a.store.db, conv, topic, refs, snapshots); parent.PID == "" && err != nil {
		return ParticipationInfo{}, err
	}
	for _, fp := range taskKeys {
		if !m.memberKey(fp) {
			return ParticipationInfo{}, fmt.Errorf("%s is not the key of a member of that conversation", fp)
		}
	}
	if parent.PID != "" {
		infos, e := a.Participations(conv)
		if e != nil {
			return ParticipationInfo{}, e
		}
		for _, existing := range infos {
			if existing.Parent == parent.Decision && existing.Host.Address == host.Address && existing.Host.Fingerprint == host.Fingerprint && existing.AgentID == agentID && sameParticipationTopic(existing.Topic, topic) && (existing.State == PartInvited || existing.State == PartActive) {
				if !slices.Equal(existing.Grant, refs) {
					return ParticipationInfo{}, errors.New("that own helper is already invited with different selected context")
				}
				events, e := a.store.participationEvents(conv, existing.PID)
				if e != nil {
					return ParticipationInfo{}, e
				}
				for _, ev := range events {
					if ev.Hash() == existing.Invite {
						if e = a.resend(ctx, ev); e != nil {
							return ParticipationInfo{}, e
						}
						return a.Participation(existing.PID)
					}
				}
				return existing, nil
			}
		}
	}
	if m.group != nil && role == "" && parent.PID == "" {
		infos, err := a.Participations(conv)
		if err != nil {
			return ParticipationInfo{}, err
		}
		for _, existing := range infos {
			if existing.Role == "" && existing.Host.Address == host.Address && existing.Host.Fingerprint == host.Fingerprint && existing.AgentID == agentID && sameParticipationTopic(existing.Topic, topic) && (existing.State == PartActive || existing.State == PartInvited) {
				for _, ref := range refs {
					if old, ok := grantSnapshot(ref, existing.Snapshots); ok {
						item, e := a.snapshotSourceItem(a.store.db, conv, ref)
						if e != nil {
							return ParticipationInfo{}, e
						}
						controls, e := a.controlsOf(ControlRef{Conv: conv, ID: ref.LID, Fingerprint: ref.Fingerprint})
						if e != nil {
							return ParticipationInfo{}, e
						}
						item.Body = controls.Shown(item.Body)
						if controls.Deleted || contentHash(item.inner(conv)) != old.Hash {
							return ParticipationInfo{}, errors.New("previously shared context is frozen; choose a new participation for changed content")
						}
					}
				}
				ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: existing.PID, Type: protocol.EventShare, Prev: existing.Invite, TS: time.Now().Unix(), Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: me.info.Fingerprint}, Host: host, Grant: refs, Audience: existing.Audience, Topic: topic, Snapshots: snapshots}
				if err := m.bindGroupInvite(&ev); err != nil {
					return ParticipationInfo{}, err
				}
				if err := a.recordAndSendWith(ctx, ev, snapshotClaim); err != nil {
					return ParticipationInfo{}, err
				}
				if existing.External || len(snapshots) > 0 {
					if err := a.sendGrantedExcerpts(ctx, ev); err != nil {
						return ParticipationInfo{}, err
					}
				}
				return a.Participation(existing.PID)
			}
		}
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: me.info.Fingerprint},
		Host:   host, Grant: refs, Audience: protocol.AudienceConversation, TaskKeys: taskKeys, Note: note, Role: role, Topic: topic, Snapshots: snapshots}
	if parent.PID != "" {
		ev.Parent, ev.Until, ev.Audience = parent.Decision, parent.Until, protocol.AudienceRoom
	}
	if m.group != nil {
		if role == "" || role == protocol.RoleHuman {
			ev.Audience = protocol.AudienceRoom
		}
		if err := m.bindGroupInvite(&ev); err != nil {
			return ParticipationInfo{}, err
		}
	}
	if err := a.recordAndSendWith(ctx, ev, snapshotClaim); err != nil {
		return ParticipationInfo{}, err
	}
	if role == protocol.RoleHuman && m.group == nil || ev.Audience == protocol.AudienceRoom { // its public projection, for participants who never hold the invite (an assistant's is signed once guests need it)
		if err := a.recordAndSend(ctx, protocol.ScopeOf(ev, time.Now().Unix())); err != nil {
			return ParticipationInfo{}, err
		}
	}
	if _, member := m.persons[host.Person]; (!member || len(snapshots) > 0) && role != protocol.RoleHuman {
		if err := a.sendGrantedExcerpts(ctx, ev); err != nil {
			return ParticipationInfo{}, err
		}
	}
	return a.participation(conv, ev.PID)
}

// scopeMatchesInvite refuses a scope that is not the exact projection of the
// invite it names, when that invite is held here.
func (a *Agent) scopeMatchesInvite(ev protocol.ParticipationEvent) error {
	if ev.Type != protocol.EventScope {
		return nil
	}
	events, err := a.store.participationEvents(ev.Conv, ev.PID)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e.Type == protocol.EventInvite && e.Hash() == ev.Prev && !ev.Projects(e) {
			return errors.New("participation: scope differs from the invitation it names")
		}
		if e.Type == protocol.EventInvite && e.Hash() != ev.Prev {
			return errors.New("participation: scope names another invitation")
		}
	}
	return nil
}

// participationScope is info's counted public projection (protocol.ScopeOf),
// signed now by this device when it authored the invite and none is held.
func (a *Agent) participationScope(ctx context.Context, info ParticipationInfo) (protocol.ParticipationEvent, error) {
	events, err := a.store.participationEvents(info.Conv, info.PID)
	if err != nil {
		return protocol.ParticipationEvent{}, err
	}
	for _, e := range events {
		if info.Scope != "" && e.Hash() == info.Scope {
			return e, nil
		}
	}
	for _, e := range events {
		if e.Type == protocol.EventInvite && e.Hash() == info.Invite && (e.Group == nil || e.Audience == protocol.AudienceRoom) && e.Author.Address == a.Address && e.Author.Fingerprint == a.Self().Fingerprint() {
			s := protocol.ScopeOf(e, time.Now().Unix())
			s.Sign(a.id.Sign)
			// Lazy public proof is a separate control send, never the UI's
			// queued message identity. Preserve that identity for its own turn.
			if queuedSend(ctx) || ctx.Value(proposalSendIDKey{}) != nil {
				ctx = context.WithValue(ctx, proposalSendIDKey{}, protocol.NewID())
			}
			return s, a.recordAndSend(ctx, s)
		}
	}
	return protocol.ParticipationEvent{}, errors.New("participation: its public scope is not held here yet")
}

// AcceptParticipation accepts an invite for the agent on this installation,
// exactly as invited; DeclineParticipation declines it. Only the host's
// own person does this, here; receiving an invite never accepts it, except
// one of that person's own agent from a device they trust (selfConsent).
func (a *Agent) AcceptParticipation(ctx context.Context, pid string) (ParticipationInfo, error) {
	return a.decide(ctx, pid, protocol.EventAccept)
}

// DeclineParticipation declines an invite for the agent on this installation.
func (a *Agent) DeclineParticipation(ctx context.Context, pid string) (ParticipationInfo, error) {
	return a.decide(ctx, pid, protocol.EventDecline)
}

func (a *Agent) decide(ctx context.Context, pid, typ string) (ParticipationInfo, error) {
	info, err := a.Participation(pid)
	if err != nil {
		return info, err
	}
	if !info.HostHere {
		return info, errors.New("only the host installation's person accepts or declines an invite for its agent")
	}
	if own, err := a.ownEvent(info.Conv, pid, typ, protocol.EventAccept, protocol.EventDecline); err != nil || own != nil {
		return a.resendOwn(ctx, info, own, err)
	}
	if info.State != PartInvited {
		return info, fmt.Errorf("the invite is %s, not waiting for a decision", info.State)
	}
	decided, err := a.signWith(ctx, info, typ, info.Invite, undecidedHere(info.Conv, pid, a.Self().Fingerprint()))
	if errors.Is(err, errDecidedHere) { // decided meanwhile, without a click or by another process: as a retry, once
		own, e := a.ownEvent(info.Conv, pid, typ, protocol.EventAccept, protocol.EventDecline)
		if e == nil && own == nil { // a decision of this key that is not this installation's own event
			e = errors.New("this device's key decided that invite already, as another address: not decided again")
		}
		return a.resendOwn(ctx, info, own, e)
	}
	return decided, err
}

// resendOwn retries this installation's own stored decision own: a retry
// sends the same signed event again, never a new one. err, from finding
// it, is returned as is.
func (a *Agent) resendOwn(ctx context.Context, info ParticipationInfo, own *protocol.ParticipationEvent, err error) (ParticipationInfo, error) {
	if err == nil {
		err = a.resend(ctx, *own)
	}
	if err != nil {
		return info, err
	}
	return a.Participation(info.PID)
}

// errDecidedHere stops a decision when one of this device's key is stored
// already (signed meanwhile without a click, or by another process).
var errDecidedHere = errors.New("participation: decided here already")

// undecidedHere refuses, within the transaction that stores a decision of
// pid, when a decision of the key fp is stored already: a host decides once.
func undecidedHere(conv, pid, fp string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM participation_events WHERE conv = ? AND pid = ? AND author = ? AND type IN (?, ?)`,
			conv, pid, fp, protocol.EventAccept, protocol.EventDecline).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errDecidedHere
		}
		return nil
	}
}

// selfConsent accepts an invite of this person's own agent here without the
// person's click (owner decision D3; selfconsent.go) when all of these hold,
// and otherwise leaves it to the click as before:
//
//   - it is invited, no event of it is held, and this installation hosts it;
//   - it invites an agent (no role: never a human guest), and this device
//     has decided nothing of it;
//   - its author, its host and this installation speak for one person,
//     this one, as pinned here (never frozen);
//   - the author is a current verified own human device; its signed invitation
//     is the owner's decision, including any task keys explicitly selected;
//   - its agent is the default responder, selected, or an enabled agent of
//     this host's catalog;
//   - in a group, its invite epoch holds.
//
// The accept is the ordinary signed one, so old peers resolve it as the
// person's. It is stored with its local notice in one transaction that
// first checks that no decision of this device's key is stored. It reports
// whether it accepted now, also when the accept is stored and only sending
// it failed (the error is then an unsentError).
func (a *Agent) selfConsent(ctx context.Context, pid string) (bool, error) {
	info, err := a.Participation(pid)
	if errors.Is(err, ErrNoParticipation) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.State != PartInvited || info.Held != 0 || !info.HostHere || info.Role != "" {
		return false, nil
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok || me.info.State != personSelf || info.Host.Person != me.info.Person || info.Inviter.Person != me.info.Person {
		return false, err
	}
	m, err := a.dmMembers(info.Conv)
	if err != nil {
		return false, err
	}
	events, err := a.store.participationEvents(info.Conv, pid)
	if err != nil {
		return false, err
	}
	if err := m.loadHosts(a.store.db, events); err != nil {
		return false, err
	}
	self := a.Self().Fingerprint()
	var inv protocol.ParticipationEvent
	for _, ev := range events {
		if ev.Author.Fingerprint == self && (ev.Type == protocol.EventAccept || ev.Type == protocol.EventDecline) {
			return false, nil // decided here already: a retry is the person's
		}
		if ev.Type == protocol.EventInvite && ev.Hash() == info.Invite {
			inv = ev
		}
	}
	if inv.Type == "" || !m.inviteEpoch(inv) {
		return false, nil // the invite itself, never a scope standing for it
	}
	if !slices.Equal(info.TaskKeys, inv.TaskKeys) {
		return false, nil // the signed invitation names a device removed since
	}
	author, ownAuthor := m.author(inv.Author)
	if inv.Parent != "" {
		author, ownAuthor = m.guestAuthor(inv)
	}
	if !ownAuthor || author.info.Person != me.info.Person {
		return false, nil
	}
	trusted, err := a.ownHumanAuthor(a.store.db, inv.Author.Address, inv.Author.Fingerprint)
	if err != nil || !trusted {
		return false, err
	}
	if ok, err := a.agentEnabled(info.AgentID); err != nil || !ok {
		return false, err
	}
	notice := SelfConsentNotice{PID: pid, Conv: info.Conv, AgentID: info.AgentID, Inviter: inv.Author.Address, At: time.Now().Unix()}
	_, err = a.signWith(ctx, info, protocol.EventAccept, info.Invite, func(tx *sql.Tx) error {
		if err := undecidedHere(info.Conv, pid, self)(tx); err != nil {
			return err
		}
		// Recheck the exact author in the accept transaction: a roster/key
		// change between projection and signing must not authorize execution.
		own, err := a.ownHumanAuthor(tx, inv.Author.Address, inv.Author.Fingerprint)
		if err != nil {
			return err
		}
		if !own {
			return errors.New("inviter is no longer a verified own human device")
		}
		return addSelfConsentNoticeIn(tx, notice)
	})
	if errors.Is(err, errDecidedHere) {
		return false, nil
	}
	var unsent unsentError
	return err == nil || errors.As(err, &unsent), err // accepted, though maybe not sent yet
}

// grantConsent (owner rule, October 29, 2026; MEL-713) accepts another
// person's invite of an agent hosted here to visit their room without a
// click when this person already approved the exact inviting device, or its
// person, for questions or tasks. Accepting only lets that room's requests
// reach the agent; each still needs its own permission here (roomChain).
// An invite naming task keys, a two-person chat's or a room this person
// belongs to, a guest's invite or a pending key change waits for the click.
func (a *Agent) grantConsent(ctx context.Context, pid string) (bool, error) {
	info, err := a.Participation(pid)
	if errors.Is(err, ErrNoParticipation) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.State != PartInvited || info.Held != 0 || !info.HostHere || info.Role != "" || len(info.TaskKeys) != 0 {
		return false, nil
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok || me.info.State != personSelf || info.Host.Person != me.info.Person || info.Inviter.Person == me.info.Person {
		return false, err
	}
	m, err := a.dmMembers(info.Conv)
	if err != nil || m.group == nil || !info.External {
		return false, err
	}
	events, err := a.store.participationEvents(info.Conv, pid)
	if err != nil {
		return false, err
	}
	if err := m.loadHosts(a.store.db, events); err != nil {
		return false, err
	}
	self := a.Self().Fingerprint()
	var inv protocol.ParticipationEvent
	for _, ev := range events {
		if ev.Author.Fingerprint == self && (ev.Type == protocol.EventAccept || ev.Type == protocol.EventDecline) {
			return false, nil // decided here already: a retry is the person's
		}
		if ev.Type == protocol.EventInvite && ev.Hash() == info.Invite {
			inv = ev
		}
	}
	if inv.Type == "" || inv.Parent != "" || len(inv.TaskKeys) != 0 {
		return false, nil
	}
	if author, member := m.author(inv.Author); !member || author.info.Person == me.info.Person {
		return false, nil
	}
	granted := func(q querier) (bool, error) {
		var pending int
		if err := q.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, inv.Author.Address).Scan(&pending); err != nil || pending != 0 {
			return false, err
		}
		if ok, err := questionApproved(q, inv.Author.Address, inv.Author.Fingerprint); err != nil || ok {
			return ok, err
		}
		return taskGranted(q, inv.Author.Address, inv.Author.Fingerprint)
	}
	if ok, err := granted(a.store.db); err != nil || !ok {
		return false, err
	}
	if ok, err := a.agentEnabled(info.AgentID); err != nil || !ok {
		return false, err
	}
	notice := SelfConsentNotice{PID: pid, Conv: info.Conv, AgentID: info.AgentID, Inviter: inv.Author.Address, At: time.Now().Unix(), Granted: true}
	_, err = a.signWith(ctx, info, protocol.EventAccept, info.Invite, func(tx *sql.Tx) error {
		if err := undecidedHere(info.Conv, pid, self)(tx); err != nil {
			return err
		}
		// Recheck the grant in the accept transaction: a revoke or key change
		// between projection and signing must not let the invite in.
		if ok, err := granted(tx); err != nil {
			return err
		} else if !ok {
			return errors.New("the inviter is no longer approved here")
		}
		return addSelfConsentNoticeIn(tx, notice)
	})
	if errors.Is(err, errDecidedHere) {
		return false, nil
	}
	var unsent unsentError
	return err == nil || errors.As(err, &unsent), err
}

// agentEnabled reports whether agentID names an agent this host runs: the
// default responder ("") once one is selected, or a catalog agent with a
// responder (a disabled one has none).
func (a *Agent) agentEnabled(agentID string) (bool, error) {
	if agentID == "" {
		r, err := a.Responder()
		return r != nil, err
	}
	entries, err := a.LocalAgents()
	return slices.ContainsFunc(entries, func(e LocalAgentInfo) bool { return e.Record.ID == agentID && e.Responder != nil }), err
}

// trySelfConsent runs selfConsent where its failure must not undo what came
// before it (an invite sent, an event admitted): the failure is logged as
// what it is. An invite not accepted waits for the click or the next
// daemon start; an accept stored whose sending failed is sent again by
// the next daemon start's sweep, or now by dm accept-agent PID.
func (a *Agent) trySelfConsent(ctx context.Context, pid string) {
	accepted, err := a.selfConsent(ctx, pid)
	if err == nil && !accepted {
		accepted, err = a.grantConsent(ctx, pid) // another person this one approved
	}
	switch {
	case err == nil:
	case accepted:
		a.Logf("participation %s: accepted without a click; sending the accept failed (the next daemon start sends it again, agentnet dm accept-agent %s now): %v", pid, pid, err)
	default:
		a.Logf("participation %s: not accepted without a click: %v", pid, err)
	}
}

// sweepSelfConsent tries selfConsent for every agent invite hosted here
// that this device has not decided: one whose evidence, trust or agent came
// after it was admitted, or whose accept a stopped process never signed.
// It first sends again each accept of this installation's stored with no
// copy queued (sending failed before any was, or the process stopped in
// between), so the other members do not see the invite waiting for good.
// The daemon runs it at start.
func (a *Agent) sweepSelfConsent(ctx context.Context) {
	self := a.Self().Fingerprint()
	a.resendUnsentAccepts(ctx, self)
	rows, err := a.store.db.Query(`SELECT DISTINCT pid FROM participation_events i WHERE type = ?
		AND json_extract(event, '$.host.address') = ? AND json_extract(event, '$.host.fingerprint') = ? AND coalesce(json_extract(event, '$.role'), '') = ''
		AND NOT EXISTS (SELECT 1 FROM participation_events d WHERE d.conv = i.conv AND d.pid = i.pid AND d.author = ? AND d.type IN (?, ?))`,
		protocol.EventInvite, a.Address, self, self, protocol.EventAccept, protocol.EventDecline)
	if err != nil {
		a.Logf("participations: %v", err)
		return
	}
	var pids []string
	for rows.Next() {
		var pid string
		if rows.Scan(&pid) == nil {
			pids = append(pids, pid)
		}
	}
	rows.Close()
	for _, pid := range pids {
		a.trySelfConsent(ctx, pid)
	}
}

// resendUnsentAccepts sends again, byte for byte, this installation's own
// stored accepts (key self) of which no copy was ever queued.
func (a *Agent) resendUnsentAccepts(ctx context.Context, self string) {
	rows, err := a.store.db.Query(`SELECT event FROM participation_events d WHERE d.author = ? AND d.type = ?
		AND json_extract(d.event, '$.author.address') = ?
		AND (EXISTS (SELECT 1 FROM conversations c WHERE c.id = d.conv) OR EXISTS (SELECT 1 FROM group_context g WHERE g.conv = d.conv))
		AND NOT EXISTS (SELECT 1 FROM outbox o WHERE o.conv = d.conv AND o.pid = d.pid AND o.sub = ? AND o.body = d.event)`,
		self, protocol.EventAccept, a.Address, envelope.SubEvent)
	if err != nil {
		a.Logf("participations: %v", err)
		return
	}
	var unsent []protocol.ParticipationEvent
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		if ev, err := protocol.ParseParticipationEvent([]byte(raw)); err == nil {
			unsent = append(unsent, ev)
		}
	}
	rows.Close()
	for _, ev := range unsent {
		// Same-host consent can be stored before the invitation's fanout.
		// Recover that exact prerequisite if the process failed before it
		// queued any copy. Received invitations are never re-authored.
		invite, err := a.ownEvent(ev.Conv, ev.PID, protocol.EventInvite, protocol.EventInvite)
		if err != nil {
			a.Logf("participation %s: reading stored invite: %v", ev.PID, err)
			continue
		}
		if invite != nil && invite.Hash() == ev.Prev {
			raw, _ := json.Marshal(*invite)
			var queued bool
			if err = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE conv=? AND pid=? AND sub=? AND body=?)`, ev.Conv, ev.PID, envelope.SubEvent, string(raw)).Scan(&queued); err == nil && !queued {
				err = a.resend(ctx, *invite)
			}
			if err != nil {
				a.Logf("participation %s: sending stored invite: %v", ev.PID, err)
				continue
			}
		}
		if err := a.resend(ctx, ev); err != nil {
			a.Logf("participation %s: sending this device's stored accept again failed (the next daemon start tries again, agentnet dm accept-agent %s now): %v", ev.PID, ev.PID, err)
		}
	}
}

// DismissParticipation ends a participation of a DM this installation's
// person is a member of; either member may (owner decision 2026-09-29). A
// retry sends the same dismissal again.
func (a *Agent) DismissParticipation(ctx context.Context, pid string) (ParticipationInfo, error) {
	info, err := a.Participation(pid)
	if err != nil {
		return info, err
	}
	if own, err := a.ownEvent(info.Conv, pid, protocol.EventDismiss, protocol.EventDismiss); err != nil || own != nil {
		if err == nil {
			err = a.resend(ctx, *own)
		}
		if err != nil {
			return info, err
		}
		return a.Participation(pid)
	}
	m, err := a.dmMembers(info.Conv)
	if err != nil {
		return info, err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return info, err
	}
	if _, member := m.persons[me.info.Person]; !ok || !member && !((info.HumanActive() || info.Member && info.External) && info.HostHere) {
		return info, errors.New("only an original member or exact accepted human guest ends this participation")
	}
	if !m.mayRemoveAgent(info, protocol.EventAuthor{Person: me.info.Person, Address: a.Address, Fingerprint: me.info.Fingerprint, GroupAdmission: m.keyEpoch(me.info.Fingerprint)}) {
		return info, errors.New("only a group administrator, the person who added this outside agent, or its owner can remove it")
	}
	prev := info.Decision
	switch info.State {
	case PartInvited:
		prev = info.Invite
	case PartActive, PartDeclined, PartConflict:
		if prev == "" {
			return info, fmt.Errorf("the participation is %s: there is nothing it can follow here", info.State)
		}
	default:
		return info, fmt.Errorf("the participation is %s", info.State)
	}
	return a.sign(ctx, info, protocol.EventDismiss, prev)
}

// sign makes this installation's event of typ following prev, stores it and
// sends it.
func (a *Agent) sign(ctx context.Context, info ParticipationInfo, typ, prev string) (ParticipationInfo, error) {
	return a.signWith(ctx, info, typ, prev, nil)
}

// signWith is sign storing the event in one transaction with also
// (recordAndSendWith).
func (a *Agent) signWith(ctx context.Context, info ParticipationInfo, typ, prev string, also func(*sql.Tx) error) (ParticipationInfo, error) {
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		return info, err
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: info.Conv, PID: info.PID, Type: typ, Prev: prev, TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: me.info.Fingerprint}}
	if m, e := a.dmMembers(info.Conv); e != nil {
		return info, e
	} else if m.group != nil {
		if mem, ok := m.group.State.Member(me.info.Person); ok && m.device(a.Address, me.info.Fingerprint) {
			ev.Author.GroupAdmission = mem.Admission.Hash()
		}
	}
	if err := a.recordAndSendWith(ctx, ev, also); err != nil {
		return info, err
	}
	return a.Participation(info.PID)
}

// ownEvent returns this installation's own stored event of pid of one of
// the types given, if any; want is the type asked for now, and another of
// types already stored is an error (a host decides once).
func (a *Agent) ownEvent(conv, pid, want string, types ...string) (*protocol.ParticipationEvent, error) {
	events, err := a.store.participationEvents(conv, pid)
	if err != nil {
		return nil, err
	}
	pub := a.id.Public(a.Address)
	for _, ev := range events {
		if ev.Author.Address != a.Address || ev.Author.Fingerprint != pub.Fingerprint() || !slices.Contains(types, ev.Type) {
			continue
		}
		if ev.Type != want {
			return nil, fmt.Errorf("already %s here", ev.Type+"d")
		}
		return &ev, nil
	}
	return nil, nil
}

// resend sends a stored event again, byte for byte.
func (a *Agent) resend(ctx context.Context, ev protocol.ParticipationEvent) error {
	raw, _ := json.Marshal(ev)
	// Each event is a separate control send, never the queued caller's
	// single-use identity: one queued invite stores its invite, scope and
	// consent sends independently.
	if queuedSend(ctx) {
		ctx = context.WithValue(ctx, proposalSendIDKey{}, protocol.NewID())
	}
	// Once a human end has encrypted recipient copies, retry those copies,
	// never reconstruct a wider control audience after another guest joins.
	if ev.Type == protocol.EventDismiss {
		if p, e := a.Participation(ev.PID); e == nil && p.Role == protocol.RoleHuman {
			rows, e := a.store.db.Query(`SELECT envelope,state FROM outbox WHERE conv=? AND pid=? AND sub=? AND body=? ORDER BY created_ms,id`, ev.Conv, ev.PID, envelope.SubEvent, string(raw))
			if e != nil {
				return e
			}
			var pending []envelope.Envelope
			found := false
			for rows.Next() {
				var data, state string
				if e := rows.Scan(&data, &state); e != nil {
					rows.Close()
					return e
				}
				found = true
				if state == stateQueued {
					var env envelope.Envelope
					if e := json.Unmarshal([]byte(data), &env); e != nil {
						rows.Close()
						return e
					}
					pending = append(pending, env)
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if found {
				for _, env := range pending {
					if _, e := a.deliver(ctx, env, nil); e != nil {
						return e
					}
				}
				return nil
			}
		}
	}
	out := ConvOutgoing{Kind: envelope.KindMessage, Body: string(raw), PID: ev.PID, sub: envelope.SubEvent}
	if ev.Parent != "" {
		info, err := a.Participation(ev.PID)
		if err != nil {
			return err
		}
		out.human, err = a.guestParentProof(ctx, info)
		if err != nil {
			return err
		}
	}
	_, err := a.SendConv(ctx, ev.Conv, out)
	return err
}

// recordAndSend signs ev, stores it and sends it to the DM's other member
// (kept as waiting if that device cannot read it now).
func (a *Agent) recordAndSend(ctx context.Context, ev protocol.ParticipationEvent) error {
	return a.recordAndSendWith(ctx, ev, nil)
}

// recordAndSendWith is recordAndSend storing ev in one transaction with also
// (addParticipationEventWith): an error from also stores and sends nothing.
func (a *Agent) recordAndSendWith(ctx context.Context, ev protocol.ParticipationEvent, also func(*sql.Tx) error) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	ev.Sign(a.id.Sign)
	raw, _ := json.Marshal(ev)
	if len(raw) > protocol.MaxParticipationEvent {
		return errors.New("participation: event too large")
	}
	if len(ev.Snapshots) > 0 && also == nil {
		return errors.New("reviewed snapshots require a current source commit guard")
	}
	// Stored first: if sending fails, a retry resends these very bytes.
	humanEnd := false
	if ev.Type == protocol.EventDismiss {
		if p, e := a.Participation(ev.PID); e == nil {
			humanEnd = p.Role == protocol.RoleHuman
		}
	}
	if humanEnd {
		a.humanMu.Lock()
	}
	err := a.store.addParticipationEventWith(ev, raw, func(tx *sql.Tx) error {
		if ev.Parent != "" {
			m, e := membersIn(tx, ev.Conv)
			if e != nil {
				return e
			}
			p, e := guestParentIn(tx, ev.Conv, ev.Parent, m)
			if e != nil {
				return e
			}
			if !p.HumanActive() {
				return errors.New("human parent ended before invitation commit")
			}
		}
		if ev.Parent != "" {
			if err := a.checkGuestGrants(tx, ev); err != nil {
				return err
			}
		} else if err := a.checkTopicGrants(tx, ev.Conv, ev.Topic, ev.Grant, ev.Snapshots); err != nil {
			return err
		}
		if also != nil {
			return also(tx)
		}
		return nil
	})
	if humanEnd {
		a.humanMu.Unlock()
	}
	if err != nil {
		return err
	}
	// This stored invitation is already the owner's decision. Record the
	// host's signed acceptance before any unrelated reader's fanout can fail
	// or block. Shares also retry an existing pending invitation; the normal
	// exact-once decision guard still wins against a concurrent manual click.
	if ev.Type == protocol.EventInvite || ev.Type == protocol.EventShare {
		a.trySelfConsent(ctx, ev.PID)
	}
	a.convWork.due(convRetry) // accepted guests learn this record's public scope without waiting for other traffic
	if err := a.resend(ctx, ev); err != nil {
		return unsentError{err}
	}
	return nil
}

// unsentError is the error of sending an event recordAndSendWith stored:
// the event stays stored, and a retry sends it again. It reads as the
// error it wraps.
type unsentError struct{ err error }

func (e unsentError) Error() string { return e.err.Error() }
func (e unsentError) Unwrap() error { return e.err }

// AskAgent sends a question (or task) to a participation's agent: it names
// the host device as its one execution target, whose worker decides when
// it may run (agentjob.go). Asked on the host itself, the message and its
// local job are recorded together.
func (a *Agent) AskAgent(ctx context.Context, pid, kind, body string, files ...OutgoingFile) (ConvSent, error) {
	return a.AskAgentWithReceiver(ctx, pid, kind, body, nil, files...)
}

// AskAgentWithReceiver retains AskAgent's own-host job/authority semantics
// while capturing an independent local return delegation.
func (a *Agent) AskAgentWithReceiver(ctx context.Context, pid, kind, body string, receiver *ReplyReceiver, files ...OutgoingFile) (ConvSent, error) {
	return a.AskAgentInTopic(ctx, pid, kind, body, "", receiver, files...)
}

func (a *Agent) AskAgentInTopic(ctx context.Context, pid, kind, body, topic string, receiver *ReplyReceiver, files ...OutgoingFile) (ConvSent, error) {
	info, err := a.Participation(pid)
	if err != nil {
		return ConvSent{}, err
	}
	if !info.Claimable() {
		return ConvSent{}, fmt.Errorf("the agent's participation is %s (%d records not counted here): not active", info.State, info.Held)
	}
	if kind != envelope.KindQuestion && kind != envelope.KindTask {
		return ConvSent{}, errors.New("an agent is asked a question or given a task")
	}
	target := &envelope.Target{Address: info.Host.Address, Fingerprint: info.Host.Fingerprint, AgentID: info.AgentID}
	if m, e := a.dmMembers(info.Conv); e != nil {
		return ConvSent{}, e
	} else if m.group != nil {
		adm, e := groupMemberAdmission(a.store.db, *m.group, a.Address, a.Self().Fingerprint())
		if e != nil {
			return ConvSent{}, e
		}
		target.GroupAdmission = adm.Hash()
	}
	return a.SendConv(ctx, info.Conv, ConvOutgoing{Kind: kind, Body: body, Topic: topic, Files: files, PID: pid, selfJob: info.HostHere, ReplyReceiver: receiver, Target: target})
}

// checkParticipationEvent checks an event received in a DM message from
// sender, a member device (admitConv checked that): the event must be the
// message's own (same conversation and participation), be authored and
// signed by that very device, and be well formed. It stores nothing: the
// caller stores it with its message, in one step.
func checkParticipationEvent(in envelope.Inner, senderFP string, senderKey []byte) (protocol.ParticipationEvent, error) {
	if in.Kind != envelope.KindMessage || in.PID == "" {
		return protocol.ParticipationEvent{}, errors.New("a participation event is a message naming its participation")
	}
	ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
	if err != nil {
		return ev, err
	}
	if ev.Conv != in.Conv || ev.PID != in.PID || ev.Author.Address != in.From || ev.Author.Fingerprint != senderFP {
		return ev, errors.New("participation: the event is not the sending device's own, for this conversation")
	}
	return ev, ev.Verify(senderKey)
}

// ParticipationContext is what is given to a participation's agent with a
// request (agentjob.go), besides the request itself.
type ParticipationContext struct {
	PID       string              `json:"pid"`
	Note      string              `json:"note,omitempty"`
	Messages  []ConvMessage       `json:"messages"`  // oldest first: granted earlier messages, then requests to and outputs of this participation
	Missing   int                 `json:"missing"`   // granted messages not held here
	Omitted   int                 `json:"omitted"`   // left out, oldest first, to stay within the size bound
	Addressed int                 `json:"addressed"` // of Messages, requests to or outputs of this participation
	State     string              `json:"state"`     // the participation's state now
	Grant     []protocol.GrantRef `json:"grant"`     // as invited
	Bytes     int                 `json:"bytes"`     // bytes of the messages as rendered for the agent
	Limit     int                 `json:"limit"`     // the bound used
	Replicas  int                 `json:"replicas"`  // history copies left out (never context)
	Events    int                 `json:"events"`    // event messages left out (records, not context)
	Unrelated int                 `json:"unrelated"` // other messages of the DM left out (not granted, not addressed)

	lines []string // Messages as rendered for the agent
}

// defaultContextBytes bounds the message bodies given to an agent.
const defaultContextBytes = 64 << 10

// ParticipationContext shows, for an invited or active participation, what
// its agent would be given with a new request now (agentContext): the
// granted earlier messages held here and admitted live turns within its
// active chat/topic, including verified replies by other agents. Newest
// turns fit within limit bytes (limit <= 0: the default). Event records,
// ungranted snapshots, other chats/topics and native sessions stay outside.
func (a *Agent) ParticipationContext(pid string, limit int) (ParticipationContext, error) {
	info, err := a.Participation(pid)
	if err != nil {
		return ParticipationContext{}, err
	}
	if info.Held != 0 || info.State != PartInvited && info.State != PartActive {
		return ParticipationContext{}, fmt.Errorf("the participation is %s: it has no context", info.State)
	}
	return a.agentContext(info, "", limit)
}
