package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Agent participation in a DM, first checkpoint: signed invite, accept and
// decline records, their resolution into a state, and read-only views.
// Nothing here runs anything: a request to a participation's agent is held
// for the host's person like any other DM question or task (stateConvHeld),
// and the worker's claim excludes every conversation row. Dismissal is not
// here yet.
//
// Resolution: a participation's state is computed from the set of events
// held, never from the order they arrived in, and with the DM's persons as
// pinned now. An event counts only if its author is the device of a member
// person pinned (or this installation's own) with the roster the root names
// and not frozen; an invite also needs its host to be such a device and its
// task keys to be member keys. Anything else is held: it has no effect until
// the evidence is there, and then counts without being delivered again.

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
	PID         string              `json:"pid"`
	Conv        string              `json:"conv"`
	State       string              `json:"state"`
	Host        PersonInfo          `json:"host"`      // the host device's person (its device runs the agent)
	HostHere    bool                `json:"host_here"` // this installation is the host
	Inviter     PersonInfo          `json:"inviter"`
	Grant       []protocol.GrantRef `json:"grant,omitempty"`     // earlier messages the agent may be given, each exactly
	TaskKeys    []string            `json:"task_keys,omitempty"` // member keys allowed follow-up tasks here
	Note        string              `json:"note,omitempty"`
	Invite      string              `json:"invite,omitempty"`    // the invite's hash
	Decision    string              `json:"decision,omitempty"`  // the accept or decline that decided it
	Dismissal   string              `json:"dismissal,omitempty"` // the dismiss that ended it
	Conflict    string              `json:"conflict,omitempty"`  // why a conflict (or a declined fork) was resolved so
	Held        int                 `json:"held"`                // events that do not count here now
	HeldDismiss int                 `json:"held_dismiss"`        // of those, dismissals
	Invited     int64               `json:"invited,omitempty"`   // the inviter's claim, unix seconds
}

// dmMembers are a DM's member persons as pinned here now, by person id;
// a person frozen, missing or with another roster than the root names is
// left out.
type dmMembers struct {
	root    protocol.ConvRoot
	persons map[string]personRow
}

func (a *Agent) dmMembers(conv string) (dmMembers, error) {
	root, _, found, err := a.store.conversation(conv)
	if err != nil {
		return dmMembers{}, err
	}
	if !found {
		return dmMembers{}, fmt.Errorf("no conversation %s here", conv)
	}
	m := dmMembers{root: root, persons: map[string]personRow{}}
	for _, mem := range root.Members {
		p, ok, err := a.store.personByID(mem.Person)
		if err != nil {
			return dmMembers{}, err
		}
		if ok && (p.info.State == personSelf || p.info.State == personPinned) && p.info.Roster == mem.Roster {
			m.persons[mem.Person] = p
		}
	}
	return m, nil
}

func (m dmMembers) author(au protocol.EventAuthor) (personRow, bool) {
	p, ok := m.persons[au.Person]
	return p, ok && p.info.Roster == au.Roster && p.info.Address == au.Address && p.info.Fingerprint == au.Fingerprint
}

func (m dmMembers) host(h *protocol.ParticipationHost) (personRow, bool) {
	if h == nil {
		return personRow{}, false
	}
	p, ok := m.persons[h.Person]
	return p, ok && p.info.Address == h.Address && p.info.Fingerprint == h.Fingerprint
}

func (m dmMembers) memberKey(fp string) bool {
	for _, p := range m.persons {
		if p.info.Fingerprint == fp {
			return true
		}
	}
	return false
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
//   - an event following one not held (yet) is held until it arrives.
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
	var decisions, dismisses []protocol.ParticipationEvent
	for _, ev := range events {
		if _, ok := m.author(ev.Author); !ok {
			hold(ev)
			continue
		}
		switch ev.Type {
		case protocol.EventInvite:
			keysOK := true
			for _, fp := range ev.TaskKeys {
				keysOK = keysOK && m.memberKey(fp)
			}
			if _, ok := m.host(ev.Host); !ok || !keysOK {
				hold(ev)
				continue
			}
			invites[ev.Hash()] = ev
		case protocol.EventDismiss:
			dismisses = append(dismisses, ev)
		default:
			decisions = append(decisions, ev)
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
		host, _ := m.host(inv.Host)
		inviter, _ := m.author(inv.Author)
		info.Host, info.Inviter = host.info, inviter.info
		info.Grant, info.TaskKeys, info.Note, info.Invited = inv.Grant, inv.TaskKeys, inv.Note, inv.TS
		info.State = PartInvited
	default:
		info.State, info.Conflict = PartConflict, "different invites share this participation id"
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
		if !known[ev.Prev] {
			hold(ev) // follows an event not held here (yet)
			continue
		}
		if info.State != PartDismissed || ev.Hash() < info.Dismissal {
			info.Dismissal = ev.Hash()
		}
		info.State = PartDismissed
	}
	return info
}

// Claimable reports whether the participation could have work run now: it
// is active and no event of it is held (a held one may end or change it).
// Nothing runs participation work in this checkpoint either way.
func (p ParticipationInfo) Claimable() bool { return p.State == PartActive && p.Held == 0 }

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
	events, err := a.store.participationEvents(conv, pid)
	if err != nil {
		return ParticipationInfo{}, err
	}
	if len(events) == 0 {
		return ParticipationInfo{}, ErrNoParticipation
	}
	info := resolve(conv, pid, events, m)
	info.HostHere = info.Host.Address == a.Address && info.Host.State == personSelf
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
// participation. The host's person decides; nothing runs before that.
func (a *Agent) InviteAgent(ctx context.Context, conv, hostAddress string, grant, taskKeys []string, note string) (ParticipationInfo, error) {
	m, err := a.dmMembers(conv)
	if err != nil {
		return ParticipationInfo{}, err
	}
	me, ok, err := a.store.selfPerson()
	if err != nil {
		return ParticipationInfo{}, err
	}
	if _, member := m.persons[me.info.Person]; !ok || !member {
		return ParticipationInfo{}, errors.New("this installation does not speak for a member of that conversation")
	}
	var host *protocol.ParticipationHost
	for _, p := range m.persons {
		if p.info.Address == hostAddress {
			host = &protocol.ParticipationHost{Person: p.info.Person, Address: p.info.Address, Fingerprint: p.info.Fingerprint}
		}
	}
	if host == nil {
		return ParticipationInfo{}, fmt.Errorf("%s is not the device of a member of that conversation (or its person is frozen)", hostAddress)
	}
	var refs []protocol.GrantRef
	for _, lid := range grant {
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
	for _, fp := range taskKeys {
		if !m.memberKey(fp) {
			return ParticipationInfo{}, fmt.Errorf("%s is not the key of a member of that conversation", fp)
		}
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: me.info.Fingerprint},
		Host:   host, Grant: refs, Audience: protocol.AudienceConversation, TaskKeys: taskKeys, Note: note}
	if err := a.recordAndSend(ctx, ev); err != nil {
		return ParticipationInfo{}, err
	}
	return a.participation(conv, ev.PID)
}

// AcceptParticipation accepts an invite for the agent on this installation,
// exactly as invited; DeclineParticipation declines it. Only the host's
// own person does this, here; receiving an invite never accepts it.
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
		if err == nil {
			err = a.resend(ctx, *own) // a retry sends the same signed event again, never a new one
		}
		if err != nil {
			return info, err
		}
		return a.Participation(pid)
	}
	if info.State != PartInvited {
		return info, fmt.Errorf("the invite is %s, not waiting for a decision", info.State)
	}
	return a.sign(ctx, info, typ, info.Invite)
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
	me, ok, err := a.store.selfPerson()
	if err != nil {
		return info, err
	}
	if _, member := m.persons[me.info.Person]; !ok || !member {
		return info, errors.New("this installation does not speak for a member of that conversation")
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
	me, _, err := a.store.selfPerson()
	if err != nil {
		return info, err
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: info.Conv, PID: info.PID, Type: typ, Prev: prev, TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: me.info.Fingerprint}}
	if err := a.recordAndSend(ctx, ev); err != nil {
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
	_, err := a.SendConv(ctx, ev.Conv, ConvOutgoing{Kind: envelope.KindMessage, Body: string(raw), PID: ev.PID, sub: envelope.SubEvent})
	return err
}

// recordAndSend signs ev, stores it and sends it to the DM's other member
// (kept as waiting if that device cannot read it now).
func (a *Agent) recordAndSend(ctx context.Context, ev protocol.ParticipationEvent) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	ev.Sign(a.id.Sign)
	raw, _ := json.Marshal(ev)
	if len(raw) > protocol.MaxParticipationEvent {
		return errors.New("participation: event too large")
	}
	// Stored first: if sending fails, a retry resends these very bytes.
	if err := a.store.addParticipationEvent(ev, raw); err != nil {
		return err
	}
	return a.resend(ctx, ev)
}

// AskAgent sends a question (or task) to a participation's agent: it names
// the host device as its one execution target. Nothing runs it yet: the
// host holds it for its person like any other DM question or task.
func (a *Agent) AskAgent(ctx context.Context, pid, kind, body string) (ConvSent, error) {
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
	return a.SendConv(ctx, info.Conv, ConvOutgoing{Kind: kind, Body: body, PID: pid,
		Target: &envelope.Target{Address: info.Host.Address, Fingerprint: info.Host.Fingerprint}})
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

// ParticipationContext is what would be given to a participation's agent
// (read-only; nothing uses it to run anything yet).
type ParticipationContext struct {
	PID       string              `json:"pid"`
	Note      string              `json:"note,omitempty"`
	Messages  []ConvMessage       `json:"messages"`  // oldest first: granted earlier messages, then requests to and outputs of this participation
	Missing   int                 `json:"missing"`   // granted messages not held here
	Omitted   int                 `json:"omitted"`   // left out, oldest first, to stay within the size bound
	Addressed int                 `json:"addressed"` // of Messages, requests to or outputs of this participation
	State     string              `json:"state"`     // the participation's state now
	Grant     []protocol.GrantRef `json:"grant"`     // as invited
	Bytes     int                 `json:"bytes"`     // body bytes included
	Limit     int                 `json:"limit"`     // the bound used
	Replicas  int                 `json:"replicas"`  // history copies left out (never context)
	Events    int                 `json:"events"`    // event messages left out (records, not context)
	Unrelated int                 `json:"unrelated"` // other messages of the DM left out (not granted, not addressed)
}

// defaultContextBytes bounds the message bodies given to an agent.
const defaultContextBytes = 64 << 10

// ParticipationContext selects, for an invited or active participation,
// what would be given to its agent: the granted earlier messages held here,
// each matched exactly (logical id and sender key, whatever participation it
// was part of), then the requests to this participation (questions and
// tasks naming its host) and its agent's outputs (answers and results sent
// by the host device itself), newest kept first within limit bytes of
// bodies (limit <= 0: the default). Nothing else of the DM, no replicas or
// event records, nothing of any other conversation; an output claimed by
// any other device is not the agent's.
func (a *Agent) ParticipationContext(pid string, limit int) (ParticipationContext, error) {
	info, err := a.Participation(pid)
	if err != nil {
		return ParticipationContext{}, err
	}
	if info.State != PartInvited && info.State != PartActive {
		return ParticipationContext{}, fmt.Errorf("the participation is %s: it has no context", info.State)
	}
	if limit <= 0 {
		limit = defaultContextBytes
	}
	msgs, err := a.ConversationMessages(info.Conv)
	if err != nil {
		return ParticipationContext{}, err
	}
	c := ParticipationContext{PID: pid, Note: info.Note, State: info.State, Grant: info.Grant, Limit: limit}
	granted := map[protocol.GrantRef]bool{}
	for _, g := range info.Grant {
		granted[g] = true
	}
	found := map[protocol.GrantRef]bool{}
	var selected []ConvMessage
	for _, m := range msgs {
		ref := protocol.GrantRef{LID: m.LID, Fingerprint: m.Key}
		switch {
		case m.Replica:
			c.Replicas++
		case m.Sub != "":
			c.Events++
		case granted[ref] && !found[ref]:
			selected = append(selected, m)
			found[ref] = true
		case m.PID == pid && (m.Kind == envelope.KindQuestion || m.Kind == envelope.KindTask) &&
			m.Target != nil && m.Target.Address == info.Host.Address && m.Target.Fingerprint == info.Host.Fingerprint:
			selected = append(selected, m)
			c.Addressed++
		case m.PID == pid && (m.Kind == envelope.KindAnswer || m.Kind == envelope.KindResult) &&
			m.From == info.Host.Address && m.Key == info.Host.Fingerprint:
			selected = append(selected, m)
			c.Addressed++
		default:
			c.Unrelated++
		}
	}
	c.Missing = len(info.Grant) - len(found)
	// Keep the newest within the bound.
	keep := len(selected)
	for i := len(selected) - 1; i >= 0; i-- {
		if c.Bytes+len(selected[i].Body) > limit {
			break
		}
		c.Bytes += len(selected[i].Body)
		keep = i
	}
	c.Omitted = keep
	c.Messages = selected[keep:]
	return c, nil
}
