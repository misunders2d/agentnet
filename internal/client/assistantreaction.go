package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An assistant may react to the request addressed to it, once, by choosing
// to: an optional `reaction: EMOJI` line (or `reaction: -EMOJI` to take its
// mark off) just before the reply's last line. The reaction names the
// assistant, never its host person: a device thread's named executor
// (agent_id) or default responder (agent origin), or a conversation
// participation (pid). It goes to the same audience as the assistant's
// reply, needs signed agr1 besides that reply's own capability, and gives
// no execution, tool or job authority.

// reactionChoice is what the reply chose.
type reactionChoice struct {
	emoji  string
	remove bool
}

// The optional reaction, offered to an eligible run: as the reply's last
// line (device requests) or just before its emotion line (conversations).
const (
	reactionPromptText     = "You may also react to this request with one emoji, if you want to: put a line at the end of your reply `reaction: EMOJI` (or `reaction: -EMOJI` to take your earlier reaction off). It is optional; nothing else is sent for it.\n"
	reactionConvPromptText = "You may also react to this request with one emoji, if you want to: put a line `reaction: EMOJI` (or `reaction: -EMOJI` to take your earlier reaction off) just before the emotion line. It is optional; nothing else is sent for it.\n"
)

// splitReaction takes an optional reaction line from the end of text. A
// malformed line is left in the text, never turned into a reaction; a
// reply that would be left empty keeps its text.
func splitReaction(text string) (string, *reactionChoice) {
	text = strings.TrimRight(text, " \t\r\n")
	i := strings.LastIndexByte(text, '\n')
	name, value, found := strings.Cut(text[i+1:], ":")
	if !found || !strings.EqualFold(strings.TrimSpace(name), "reaction") {
		return text, nil
	}
	value = strings.TrimSpace(value)
	choice := &reactionChoice{emoji: strings.TrimPrefix(value, "-"), remove: strings.HasPrefix(value, "-")}
	rest := ""
	if i >= 0 {
		rest = strings.TrimSpace(text[:i])
	}
	if !envelope.OneEmoji(choice.emoji) || rest == "" {
		return text, nil
	}
	return rest, choice
}

// reactionTarget is the stored request an assistant may react to.
type reactionTarget struct {
	ref                      ControlRef
	sender, key, kind, lid   string
	conv, pid                string
	target                   *envelope.Target
	local, replica, answered bool
}

func reactionTargetIn(q dbq, id string) (reactionTarget, error) {
	var t reactionTarget
	var conv, pid, lid, target sql.NullString
	err := q.QueryRow(`SELECT sender, coalesce(verified_by,''), kind, conv, pid, lid, target, local, replica FROM inbox WHERE id=? AND ref_id IS NULL`, id).
		Scan(&t.sender, &t.key, &t.kind, &conv, &pid, &lid, &target, &t.local, &t.replica)
	if err != nil {
		return t, err
	}
	t.conv, t.pid, t.lid = conv.String, pid.String, lid.String
	if target.String != "" {
		t.target = &envelope.Target{}
		if json.Unmarshal([]byte(target.String), t.target) != nil {
			return t, errors.New("stored request has an invalid target")
		}
	}
	t.ref = ControlRef{ID: id, Fingerprint: t.key}
	if t.conv != "" {
		t.ref = ControlRef{Conv: t.conv, ID: t.lid, Fingerprint: t.key}
	}
	if t.kind != envelope.KindQuestion && t.kind != envelope.KindTask || t.local || t.replica || t.key == "" || t.conv != "" && (t.pid == "" || t.lid == "") {
		return t, errors.New("only a question or task sent to this device's assistant takes its reaction")
	}
	return t, nil
}

// reactorOf is the assistant a reaction from this device names for job j,
// as wire fields: agent_id, pid and the default responder's origin.
func reactorOf(t reactionTarget, self, selfFP, harness string) (agentID, pid, origin string, err error) {
	switch {
	case t.conv != "":
		if t.target == nil || t.target.Address != self || t.target.Fingerprint != selfFP {
			return "", "", "", errors.New("the request does not address this device's assistant")
		}
		return t.target.AgentID, t.pid, "", nil
	case t.target != nil:
		if t.target.Address != self || t.target.Fingerprint != selfFP || t.target.AgentID == "" {
			return "", "", "", errors.New("the request names another executor")
		}
		return t.target.AgentID, "", "", nil
	default:
		return "", "", envelope.OriginAgentPrefix + harness, nil
	}
}

// sendAssistantReaction sends the reaction an assistant chose for the
// request of j, after its reply: a failure is logged and changes nothing
// about the reply or the job.
func (a *Agent) sendAssistantReaction(ctx context.Context, j job, harness string, choice *reactionChoice) {
	if choice == nil {
		return
	}
	if _, err := a.reactAsAssistant(ctx, j.ID, harness, choice.emoji, choice.remove); err != nil {
		a.Logf("%s %s: chosen reaction not sent: %v", j.Kind, j.ID, err)
	}
}

func (a *Agent) reactAsAssistant(ctx context.Context, id, harness, emoji string, remove bool) (ControlSent, error) {
	if !envelope.OneEmoji(emoji) {
		return ControlSent{}, errors.New("a reaction is one emoji")
	}
	t, err := reactionTargetIn(a.store.db, id)
	if err != nil {
		return ControlSent{}, err
	}
	self, selfFP := a.Address, a.Self().Fingerprint()
	agentID, pid, origin, err := reactorOf(t, self, selfFP, harness)
	if err != nil {
		return ControlSent{}, err
	}
	op := "add"
	if remove {
		op = "remove"
	}
	n, err := a.store.nextCounter(t.ref, envelope.SubReaction, emoji)
	if err != nil {
		return ControlSent{}, err
	}
	body, _ := json.Marshal(envelope.Reaction{Emoji: emoji, Op: op, N: n})
	feats, err := a.relayFeatures(ctx)
	if err != nil {
		return ControlSent{}, err
	}
	eref := &envelope.Ref{ID: t.ref.ID, Fingerprint: t.ref.Fingerprint}
	base := envelope.Inner{V: envelope.Version3, From: self, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubReaction,
		Body: string(body), Ref: eref, AgentID: agentID, PID: pid, Origin: origin}
	type dest struct {
		dev     identity.Public
		replica bool
		fan     []envelope.Fan
	}
	var dests []dest
	var group *GroupContext
	if t.conv == "" {
		key, err := a.sendKey(ctx, t.sender)
		if err != nil {
			return ControlSent{}, err
		}
		if key.Fingerprint() != t.key { // never to a replacement of the key that sent the request
			return ControlSent{}, errors.New("the requester's key changed since the request")
		}
		dests = append(dests, dest{dev: key})
	} else {
		root, _, found, err := a.store.conversation(t.conv)
		if err != nil {
			return ControlSent{}, err
		}
		if !found {
			return ControlSent{}, fmt.Errorf("no conversation %s here", t.conv)
		}
		m, err := controlMembers(a.store.db, t.conv)
		if err != nil {
			return ControlSent{}, err
		}
		me, _, err := a.store.selfPerson(a.Address)
		if err != nil {
			return ControlSent{}, err
		}
		if root.Kind == protocol.ConvKindGroup {
			packet, e := groupTurnPacketIn(a.store.db, t.conv)
			if e != nil {
				return ControlSent{}, e
			}
			group = &packet
		}
		var dmFan []envelope.Fan
		for person, p := range m.persons {
			dmFan = append(dmFan, envelope.Fan{Person: person, Roster: p.info.Roster})
		}
		for person, p := range m.persons {
			own := person == me.info.Person
			for _, d := range p.roster.Devices {
				if d.Address == a.Address {
					continue
				}
				fan := dmFan
				if group != nil { // as a group control names them: the recipient's person (and this one's, if a member)
					fan = []envelope.Fan{{Person: person, Roster: p.roster.Hash()}}
					if mine, ok := m.persons[me.info.Person]; ok && !own {
						fan = append(fan, envelope.Fan{Person: me.info.Person, Roster: mine.roster.Hash()})
					}
				}
				dests = append(dests, dest{dev: d, replica: own, fan: fan})
			}
		}
		base.Conv, base.LID = t.conv, protocol.NewID()
		if group == nil {
			// With guests, the request's captured audience still active sees the
			// reaction too, as its output: the same Human bytes on every copy.
			h, err := a.reactionAudience(ctx, id, t.conv)
			if err != nil {
				return ControlSent{}, err
			}
			if h != nil {
				base.Human = h
				guests, err := a.humanHostDevices(h)
				if err != nil {
					return ControlSent{}, err
				}
				for _, d := range guests {
					dests = append(dests, dest{dev: d, fan: dmFan})
				}
			}
		}
	}
	var copies []outCopy
	sent := ControlSent{State: protocol.StateDelivered}
	for _, d := range dests {
		key, err := a.sendKey(ctx, d.dev.Address)
		if err == nil && key.Fingerprint() != d.dev.Fingerprint() {
			err = fmt.Errorf("%s's key is not the one its person's roster names", d.dev.Address)
		}
		if err != nil {
			sent.Skipped = append(sent.Skipped, d.dev.Address+": "+err.Error())
			continue
		}
		if ok, why := a.capSupport(ctx, d.dev.Address, key, feats, protocol.CapControl); !ok {
			sent.Skipped = append(sent.Skipped, why)
			continue
		}
		recipient, err := key.Recipient()
		if err != nil {
			return ControlSent{}, err
		}
		in := base
		in.ID, in.To, in.Replica, in.Fan = protocol.NewID(), d.dev.Address, d.replica, d.fan
		env, err := envelope.Seal(in, a.id.Sign, recipient)
		if err != nil {
			return ControlSent{}, err
		}
		c := outCopy{env: env, in: in, state: stateQueued, required: protocol.CapAgentReaction, recipientFP: key.Fingerprint()}
		if group != nil {
			adm, e := groupMemberAdmission(a.store.db, *group, d.dev.Address, key.Fingerprint())
			if e != nil {
				sent.Skipped = append(sent.Skipped, d.dev.Address+": "+e.Error())
				continue
			}
			c.groupAdmission = adm.Hash()
		}
		copies = append(copies, c)
	}
	if len(copies) == 0 {
		return sent, fmt.Errorf("%w: %s", ErrNoControls, strings.Join(sent.Skipped, "; "))
	}
	guard := func(tx *sql.Tx, _ string) error {
		if err := a.assistantReactionCurrent(tx, t, copies[0].in, group); err != nil {
			return err
		}
		if base.Human != nil { // each reader exactly as captured: a member, or a guest still active
			for _, c := range copies {
				if err := humanAuthority(tx, t.conv, base.Human, a.Address, a.Self().Fingerprint(), c.env.To, c.recipientFP, false, true, false); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := a.store.addConvOutbox(copies, envelope.Inner{}, guard, ""); err != nil {
		return ControlSent{}, err
	}
	defer notifyDaemon(a.home)
	sent.ID = copies[0].env.ID
	for _, c := range copies {
		state := c.state
		res, err := a.deliver(ctx, c.env, nil)
		switch {
		case err == nil:
			state = res.State
		case retryable(err):
			sent.Detail = err.Error()
		default:
			state, sent.Detail = stateFailed, err.Error()
		}
		if rank(state) < rank(sent.State) {
			sent.State = state
		}
	}
	return sent, nil
}

// reactionAudience is the guest audience of this assistant's reaction to
// request id in DM conv: the request's captured audience (its Human), as
// the assistant's output takes it now (humanPlan), narrowed to the scopes
// the request named. nil: a request without guests, or none still active.
// A guest who joined after the request, or has ended, is never in it.
func (a *Agent) reactionAudience(ctx context.Context, id, conv string) (*envelope.HumanTurn, error) {
	req, err := storedHuman(a.store.db, "in", id)
	if err != nil || req == nil {
		return nil, err
	}
	now, err := a.humanPlan(ctx, conv, "")
	if err != nil || now == nil {
		return nil, err
	}
	named := map[envelope.HumanScope]bool{} // the exact scope: participation, invitation and acceptance
	for _, s := range req.Audience {
		named[s] = true
	}
	h := &envelope.HumanTurn{Audience: []envelope.HumanScope{}, Proof: []protocol.ParticipationEvent{}}
	kept := map[string]bool{}
	for _, s := range now.Audience {
		if named[s] {
			h.Audience = append(h.Audience, s)
			kept[s.PID] = true
		}
	}
	for _, e := range now.Proof {
		if kept[e.PID] {
			h.Proof = append(h.Proof, e)
		}
	}
	if len(h.Audience) == 0 {
		return nil, nil
	}
	return h, h.Validate(conv)
}

// humanHostDevices are the exact accepted host devices of h's scopes, as
// a human-audience turn goes to them (humansend.go).
func (a *Agent) humanHostDevices(h *envelope.HumanTurn) ([]identity.Public, error) {
	var out []identity.Public
	for _, scope := range h.Audience {
		p, err := a.Participation(scope.PID)
		if err != nil {
			return nil, err
		}
		host, ok, err := a.store.personByID(p.Host.Person)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("human host proof missing")
		}
		d, ok := host.device(p.Host.Address)
		if !ok || d.Fingerprint() != p.Host.Fingerprint {
			return nil, errors.New("human host key changed")
		}
		if d.Address != a.Address {
			out = append(out, d)
		}
	}
	return out, nil
}

// assistantReactionCurrent decides, from what q holds now, whether this
// device's assistant may still react on t as in names it: the request is
// unchanged and, for a participation, the agent may still give output.
func (a *Agent) assistantReactionCurrent(q dbq, t reactionTarget, in envelope.Inner, group *GroupContext) error {
	now, err := reactionTargetIn(q, t.ref.ID)
	if t.conv != "" {
		var id string
		if e := q.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND verified_by=? AND ref_id IS NULL AND local=0 AND replica=0`, t.conv, t.lid, t.key).Scan(&id); e != nil {
			return e
		}
		now, err = reactionTargetIn(q, id)
	}
	if err != nil {
		return err
	}
	agentID, pid, origin, err := reactorOf(now, a.Address, a.Self().Fingerprint(), strings.TrimPrefix(in.Origin, envelope.OriginAgentPrefix))
	if err != nil || agentID != in.AgentID || pid != in.PID || origin != in.Origin {
		return errors.New("the request no longer addresses this assistant")
	}
	if now.conv == "" {
		return nil
	}
	req := agentReq{ID: now.ref.ID, Sender: now.sender, Key: now.key, Kind: now.kind, Conv: now.conv, PID: now.pid, Target: now.target}
	if e := q.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND verified_by=? AND ref_id IS NULL AND local=0 AND replica=0`, now.conv, now.lid, now.key).Scan(&req.ID); e != nil {
		return e
	}
	v, why, err := agentVerdict(q, req, a.Address, a.Self().Fingerprint(), true, map[string]*partView{})
	if err != nil {
		return err
	}
	if v != verdictRun {
		return &heldBack{why}
	}
	if group != nil {
		current, e := groupTurnPacketIn(q, now.conv)
		if e != nil {
			return e
		}
		if current.State.Hash() != group.State.Hash() {
			return ErrGroupContextPending
		}
	}
	return nil
}

// assistantReactionCaps are what a reader needs besides agr1: the
// capability the assistant's own reply needs there.
func (a *Agent) assistantReactionCaps(ctx context.Context, key identity.Public, conv, pid, agentID string) error {
	if conv == "" {
		if agentID != "" {
			return a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
		}
		return nil
	}
	root, _, found, err := a.store.conversation(conv)
	if err != nil || !found {
		return fmt.Errorf("%w: conversation %s not here", errAgentIdentityUnsupported, conv)
	}
	if root.Kind == protocol.ConvKindGroup {
		return a.requireParticipationCaps(ctx, key, protocol.CapGroup)
	}
	if p, err := a.participation(conv, pid); err == nil && p.External {
		return a.requireParticipationCaps(ctx, key, protocol.CapExternalParticipation)
	}
	if agentID != "" {
		return a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
	}
	return nil
}

// mayDeliverAssistantReaction fences a queued assistant reaction at every
// send and retry: the captured reader key, the request still addressing
// this assistant, its participation still allowing output and, in a group,
// the reader's admission unchanged. handled is false for anything else.
func (a *Agent) mayDeliverAssistantReaction(env envelope.Envelope) (handled, allowed bool, err error) {
	var sub, state, conv, pid, agentID, origin, fp, fence, refID, refFP, humanRaw string
	err = a.store.db.QueryRow(`SELECT coalesce(sub,''),state,coalesce(conv,''),coalesce(pid,''),coalesce(agent_id,''),coalesce(origin,''),coalesce(recipient_fp,''),coalesce(group_admission,''),coalesce(ref_id,''),coalesce(ref_fp,''),coalesce(human,'')
		FROM outbox WHERE id=?`, env.ID).Scan(&sub, &state, &conv, &pid, &agentID, &origin, &fp, &fence, &refID, &refFP, &humanRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	in := envelope.Inner{V: envelope.Version3, Sub: sub, Conv: conv, PID: pid, AgentID: agentID, Origin: origin}
	if !envelope.AssistantReaction(in) {
		return false, false, nil
	}
	if state != stateQueued {
		return true, false, nil
	}
	stop := func(why string) (bool, bool, error) {
		return true, false, a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: "+why, "")
	}
	if recipientFP(a.store, env.To) != fp {
		return stop("the reader's key changed")
	}
	if humanRaw != "" { // the captured audience, for this exact reader key: a guest who has ended gets nothing
		var h envelope.HumanTurn
		if err := json.Unmarshal([]byte(humanRaw), &h); err != nil {
			return true, false, err
		}
		if err := humanAuthority(a.store.db, conv, &h, a.Address, a.Self().Fingerprint(), env.To, fp, false, true, false); err != nil {
			return stop(err.Error())
		}
	}
	id := refID
	if conv != "" {
		if e := a.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND verified_by=? AND ref_id IS NULL AND local=0 AND replica=0`, conv, refID, refFP).Scan(&id); e != nil {
			return stop("its request is not held here")
		}
	}
	t, err := reactionTargetIn(a.store.db, id)
	if err != nil {
		return stop(err.Error())
	}
	var group *GroupContext
	if conv != "" {
		if root, _, found, _ := a.store.conversation(conv); found && root.Kind == protocol.ConvKindGroup {
			packet, e := groupTurnPacketIn(a.store.db, conv)
			if e != nil {
				return true, false, nil // evidence may still come
			}
			adm, e := groupMemberAdmission(a.store.db, packet, env.To, fp)
			if e != nil || adm.Hash() != fence {
				return stop("the reader's group admission changed")
			}
			group = &packet
		}
	}
	err = a.assistantReactionCurrent(a.store.db, t, in, group)
	var hb *heldBack
	switch {
	case err == nil:
		return true, true, nil
	case errors.Is(err, ErrGroupContextPending):
		return true, false, nil
	case errors.As(err, &hb):
		return stop(hb.why)
	default:
		return stop(err.Error())
	}
}

// admitAssistantReaction admits a received assistant reaction only as its
// assistant's reply would be: from the exact device holding the request
// addressed to that assistant, about that very request.
func (a *Agent) admitAssistantReaction(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	self, selfFP := a.Address, a.Self().Fingerprint()
	if in.Conv == "" {
		// This device's own request to the sender, to its named executor or
		// its default responder; the origin is only the sender's claim.
		if in.Ref.Fingerprint != selfFP {
			return hold(reasonInvalid, "an assistant reacts only to the request it was sent")
		}
		var kind, target, sealedTo string
		// A device request's kind is its signed envelope's (the row keeps none);
		// recipient_fp is the exact key it was sealed to.
		err := a.store.db.QueryRow(`SELECT coalesce(kind, json_extract(envelope, '$.kind'), ''), coalesce(target,''), coalesce(recipient_fp,'') FROM outbox WHERE id=? AND conv IS NULL AND recipient=? AND ref_id IS NULL`, in.Ref.ID, env.From).Scan(&kind, &target, &sealedTo)
		if errors.Is(err, sql.ErrNoRows) {
			return hold(reasonInvalid, "no request of this device to that assistant")
		}
		if err != nil {
			return err
		}
		if kind != envelope.KindQuestion && kind != envelope.KindTask {
			return hold(reasonInvalid, "an assistant reacts only to a question or task")
		}
		if sealedTo == "" || sealedTo != sender.Fingerprint() { // a replacement key never speaks for an older request
			return hold(reasonInvalid, "the request's exact recipient key is not the reacting key (or was not recorded)")
		}
		if in.AgentID != "" {
			var t envelope.Target
			if json.Unmarshal([]byte(target), &t) != nil || t.Address != env.From || t.Fingerprint != sender.Fingerprint() || t.AgentID != in.AgentID {
				return hold(reasonInvalid, "the reaction's agent is not the one the request named on that device")
			}
		} else if target != "" {
			return hold(reasonInvalid, "a default responder reacts only to a request with no named executor")
		}
		return a.store.addControlInbox(in, sender.Fingerprint(), fromQuarantine)
	}
	if _, _, found, err := a.store.conversation(in.Conv); err != nil {
		return err
	} else if !found {
		return hold(reasonProof, "the conversation is not here (yet)")
	}
	check := func(q dbq) (string, error) {
		m, err := controlMembers(q, in.Conv)
		if err != nil {
			return reasonProof, err
		}
		if !m.device(self, selfFP) && in.Human == nil {
			return reasonInvalid, errors.New("an assistant reaction goes only to the conversation's members and its request's captured guests")
		}
		p, err := participationIn(q, in.Conv, in.PID, m, self)
		if errors.Is(err, ErrNoParticipation) {
			return reasonProof, err
		}
		if err != nil {
			return "", err
		}
		if p.Host.Address != env.From || p.Host.Fingerprint != sender.Fingerprint() || p.AgentID != in.AgentID {
			return reasonInvalid, errors.New("the reaction is not from the exact assistant of that participation")
		}
		if !p.Claimable() {
			return reasonInvalid, errors.New("the assistant's participation is not active")
		}
		if m.group != nil {
			packet, e := groupTurnPacketIn(q, in.Conv)
			if e != nil {
				return reasonProof, e
			}
			if e = groupTurnCheck(q, packet, self, selfFP); e != nil {
				return reasonInvalid, e
			}
		}
		if in.Human != nil {
			// The captured audience: its author is the participation's exact host
			// (checked above), this device a member or a guest still active in it.
			if err := humanAuthority(q, in.Conv, in.Human, env.From, sender.Fingerprint(), self, selfFP, false, true, false); err != nil {
				if errors.Is(err, ErrNoParticipation) {
					return reasonProof, err
				}
				return reasonInvalid, err
			}
			return humanReactionRequest(q, in, p, selfFP)
		}
		request := envelope.Inner{Conv: in.Conv, PID: in.PID, Kind: envelope.KindMessage, Status: envelope.StatusProgress, ReplyTo: in.Ref.ID, AgentID: in.AgentID}
		if reason, err := externalOutputRequest(q, request, p, m, self, selfFP); err != nil {
			return reason, err
		}
		var n int
		if err := q.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE conv=? AND (id=? OR lid=?) AND coalesce(verified_by, claimed_fp)=? AND ref_id IS NULL)
			+ (SELECT count(*) FROM outbox WHERE conv=? AND (id=? OR lid=?) AND ? = ? AND ref_id IS NULL)`,
			in.Conv, in.Ref.ID, in.Ref.ID, in.Ref.Fingerprint, in.Conv, in.Ref.ID, in.Ref.ID, in.Ref.Fingerprint, selfFP).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return reasonInvalid, errors.New("the reaction's request key differs")
		}
		return "", nil
	}
	if reason, err := check(a.store.db); err != nil {
		if reason == "" {
			return err
		}
		return hold(reason, err.Error())
	}
	// This person's devices the sender did not know get it as history. In a
	// group it is stored only with this device's own exact live admission,
	// stamped and vouched for in the same transaction, as a group control
	// is (controls.go); a group read that fails here defers it, never
	// stores it unstamped.
	root, raw, _, err := a.store.conversation(in.Conv)
	if err != nil {
		return err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return hold(reasonInvalid, "this installation has no person")
	}
	group := root.Kind == protocol.ConvKindGroup
	var forward []outCopy
	var stamp string
	if !group {
		forward = a.forwardStale(me, in, sender.Fingerprint(), raw)
	} else {
		packet, err := groupTurnPacketIn(a.store.db, in.Conv)
		if err != nil {
			return hold(reasonProof, err.Error())
		}
		own, err := groupMemberAdmission(a.store.db, packet, self, selfFP)
		if err != nil {
			return err
		}
		stamp = own.Hash()
		forward = a.forwardStaleWithControlProof(me, in, sender.Fingerprint(), raw, &groupControlIngressProof{in: in, key: sender.Fingerprint(), admission: stamp}, stamp)
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		if _, err := check(tx); err != nil {
			return err
		}
		if group {
			packet, e := groupTurnPacketIn(tx, in.Conv)
			if e != nil {
				return e
			}
			own, e := groupMemberAdmission(tx, packet, self, selfFP)
			if e != nil {
				return e
			}
			if stamp == "" || own.Hash() != stamp {
				return ErrGroupContextPending
			}
			r, e := tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, stamp, in.ID)
			if e != nil {
				return e
			}
			if n, e := r.RowsAffected(); e != nil || n != 1 {
				return errors.New("group: assistant reaction not stamped with its live admission")
			}
			for _, c := range forward {
				if c.groupAdmission != stamp {
					return ErrGroupContextPending
				}
			}
		}
		return insertCopies(tx, forward)
	})
	if err == nil && res == admitConflict { // stored nowhere: held, so it has a receipt (MIXED-1)
		return hold(reasonDuplicate, "a reaction with the same key and logical id but other content is stored")
	}
	if err == nil && res == admitted && len(forward) > 0 {
		a.kickNow()
	}
	return err
}

// humanReactionRequest checks that the request an assistant's reaction with
// a captured audience names is held here exactly: a question or task of
// that key and logical id addressed to that participation's host and agent,
// itself sent to a captured audience (a guest's, or a member's beside
// guests), whose exact scopes include every scope the reaction names: the
// sender's audience is never trusted beyond the request's. Not held yet:
// proof pending.
func humanReactionRequest(q dbq, in envelope.Inner, p ParticipationInfo, selfFP string) (string, error) {
	if err := checkParticipationTopic(q, p.Topic, in); err != nil {
		if errors.Is(err, errParticipationTopicPending) {
			return reasonProof, err
		}
		return reasonInvalid, err
	}
	for _, scope := range in.Human.Proof {
		if scope.Topic != nil {
			if err := checkParticipationTopic(q, scope.Topic, in); err != nil {
				if errors.Is(err, errParticipationTopicPending) {
					return reasonProof, err
				}
				return reasonInvalid, err
			}
		}
	}
	var target, kind, raw string
	err := q.QueryRow(`SELECT coalesce(target,''), kind, human FROM inbox WHERE conv=? AND lid=? AND coalesce(verified_by, claimed_fp)=? AND pid=? AND ref_id IS NULL AND human IS NOT NULL
		UNION ALL SELECT coalesce(target,''), kind, human FROM outbox WHERE conv=? AND lid=? AND ?=? AND pid=? AND ref_id IS NULL AND human IS NOT NULL LIMIT 1`,
		in.Conv, in.Ref.ID, in.Ref.Fingerprint, in.PID, in.Conv, in.Ref.ID, in.Ref.Fingerprint, selfFP, in.PID).Scan(&target, &kind, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return reasonProof, errors.New("the reaction's request is not held here (yet)")
	}
	if err != nil {
		return "", err
	}
	var t envelope.Target
	if kind != envelope.KindQuestion && kind != envelope.KindTask || json.Unmarshal([]byte(target), &t) != nil ||
		t.Address != p.Host.Address || t.Fingerprint != p.Host.Fingerprint || t.AgentID != p.AgentID {
		return reasonInvalid, errors.New("the reaction's request is not addressed to that assistant")
	}
	var req envelope.HumanTurn
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", err
	}
	captured := map[envelope.HumanScope]bool{}
	for _, s := range req.Audience {
		captured[s] = true
	}
	for _, s := range in.Human.Audience {
		if !captured[s] {
			return reasonInvalid, errors.New("the reaction names a scope its request's captured audience does not")
		}
	}
	return "", nil
}

// assistantHistoryCheck is admitHistory's check of an assistant reaction
// carried as history: the participation's exact host key and agent, and
// the request it reacts to being the one addressed to that participation.
// The reason tells a proof still missing here (reasonProof: participation,
// acceptance or request not yet held, or a participation conflict not yet
// resolved) from a proven mismatch (reasonInvalid); "" is any other error.
func assistantHistoryCheck(q dbq, orig envelope.Inner, from, fromFP, self, selfFP string) (string, error) {
	if orig.Conv == "" || orig.PID == "" {
		return reasonInvalid, errors.New("a device thread's assistant reaction is not history")
	}
	m, err := membersIn(q, orig.Conv)
	if err != nil {
		return "", err
	}
	p, err := participationIn(q, orig.Conv, orig.PID, m, self)
	if errors.Is(err, ErrNoParticipation) {
		return reasonProof, err
	}
	if err != nil {
		return "", err
	}
	if p.Invite == "" || p.State == PartConflict {
		return reasonProof, errors.New("the assistant's participation is not settled here yet")
	}
	if p.Host.Address != from || p.Host.Fingerprint != fromFP || p.AgentID != orig.AgentID {
		return reasonInvalid, errors.New("history assistant reaction differs from its participation's exact host and agent")
	}
	request := envelope.Inner{Conv: orig.Conv, PID: orig.PID, Kind: envelope.KindMessage, Status: envelope.StatusProgress, ReplyTo: orig.Ref.ID, AgentID: orig.AgentID}
	return externalOutputRequest(q, request, p, m, self, selfFP)
}

// historyAssistant returns the item of a retained history copy body that
// carries an assistant's own reaction (ok false for anything else).
func historyAssistant(body, conv string) (HistoryItem, bool) {
	var item HistoryItem
	if body == "" || json.Unmarshal([]byte(body), &item) != nil {
		return HistoryItem{}, false
	}
	return item, envelope.AssistantReaction(item.inner(conv))
}

// assistantWho is the reactor key of an assistant's control row: distinct
// from its host person or device and from any other assistant, and never
// derived from the default responder's harness.
func assistantWho(c controlRow) (string, bool) {
	switch {
	case c.pid != "":
		return "assistant:" + c.pid, true
	case c.agentID != "":
		return "assistant:" + c.author + "/" + c.agentID, true
	case envelope.AgentOrigin(c.origin):
		return "assistant:" + c.author + "/default", true
	}
	return "", false
}

// assistantLabel names an assistant reactor when no catalog label is held
// here: its host and, for a named agent, a short form of its id, so two
// assistants on one host never look alike.
func assistantLabel(host, agentID string) string {
	if agentID != "" {
		return host + " assistant " + agentID[:min(8, len(agentID))]
	}
	return host + " assistant"
}

// labelOwnAssistants gives this device's own named assistants their
// catalog labels, the one authoritative label held here; others keep the
// fallback and carry host, agent and PID for a catalog lookup.
func (a *Agent) labelOwnAssistants(resolved map[ControlRef]Controls) {
	agents, err := a.localAgentsIn(a.store.db)
	if err != nil || len(agents) == 0 {
		return
	}
	labels := map[string]string{}
	for _, l := range agents {
		labels[l.Record.ID] = l.Record.Label
	}
	for ref, c := range resolved {
		for i := range c.Reactions {
			for j, r := range c.Reactions[i].By {
				if l, ok := labels[r.AgentID]; ok && r.Assistant && r.Host == a.Address && l != "" {
					c.Reactions[i].By[j].Label = l
				}
			}
		}
		resolved[ref] = c
	}
}
