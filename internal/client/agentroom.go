package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Owner decision, October 29, 2026 (MEL-713): an agent working in a terminal
// talks to its person's other agents in that person's own "My agents" room,
// one topic per job, posting as itself. It never speaks as the person and
// never uses device-to-device messages. Helpers join only their job's topic.

// MyAgentsRoomTitle names the person's own agent room.
const MyAgentsRoomTitle = "My agents"

const myAgentsTopicPrefix = "my_agents_topic/"

// ErrNoSessionAgent: this terminal's harness has no agent on this computer,
// so nothing here may post as it.
var ErrNoSessionAgent = errors.New("this terminal's harness has no AgentNet agent on this computer")

// SessionHarness names the native harness and session this process runs in,
// from that harness's own session environment; "" when none is recognized
// or when two harnesses are named (one runs inside the other).
func SessionHarness(getenv func(string) string) (harness, session string) {
	claude, codex := getenv(ClaudeSessionEnv), getenv("CODEX_THREAD_ID")
	switch {
	case claude != "" && codex != "":
		return "", ""
	case claude != "":
		return "claude", claude
	case codex != "":
		return "codex", codex
	}
	return "", ""
}

// SessionAgent resolves the agent hosted here that a terminal speaks as: the
// exact agentID when given, otherwise the one agent of harness.
func (a *Agent) SessionAgent(harness, agentID string) (LocalAgentInfo, error) {
	agents, err := a.LocalAgents()
	if err != nil {
		return LocalAgentInfo{}, err
	}
	var match []LocalAgentInfo
	for _, e := range agents {
		if agentID != "" {
			if e.Record.ID == agentID {
				return e, nil
			}
			continue
		}
		if harness != "" && e.Responder != nil && e.Responder.Harness == harness {
			match = append(match, e)
		}
	}
	switch {
	case agentID != "":
		return LocalAgentInfo{}, ErrUnknownAgent
	case harness == "":
		return LocalAgentInfo{}, fmt.Errorf("%w: run this from a Claude Code or Codex session, or name the agent with --from-agent ID", ErrNoSessionAgent)
	case len(match) == 0:
		return LocalAgentInfo{}, fmt.Errorf("%w: no %s agent here; add one in the AgentNet app under Agents", ErrNoSessionAgent, harness)
	case len(match) > 1:
		return LocalAgentInfo{}, fmt.Errorf("several %s agents are on this computer; name one with --from-agent ID", harness)
	}
	return match[0], nil
}

// MyAgentsRoom returns this person's "My agents" room: the oldest current
// group of theirs, with no other person, by that title, so every own device
// converges on one room; created here when none has reached this device.
func (a *Agent) MyAgentsRoom(ctx context.Context) (string, error) {
	me, found, err := a.Person()
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("this device has no person yet")
	}
	convs, err := a.Conversations()
	if err != nil {
		return "", err
	}
	var room *ConversationInfo
	for i, c := range convs {
		if c.Kind != protocol.ConvKindGroup || c.Title != MyAgentsRoomTitle || c.Deleted || c.Frozen != "" ||
			len(c.Members) != 1 || c.Members[0].Person != me.Person {
			continue
		}
		if room == nil || c.Created < room.Created || c.Created == room.Created && c.ID < room.ID {
			room = &convs[i]
		}
	}
	if room != nil {
		return room.ID, nil
	}
	packet, err := a.CreateGroup(ctx, MyAgentsRoomTitle)
	if err != nil {
		return "", err
	}
	return packet.State.Conv, nil
}

// agentRoomTopic is the job's topic: the replied-to message's topic, else
// the asking session's topic, else a new one for that session.
func (a *Agent) agentRoomTopic(conv, sessionKey, replyTo string) (string, error) {
	if replyTo != "" {
		topic, err := a.outgoingTopic(conv, "", replyTo)
		if err != nil || topic != "" {
			return topic, err
		}
	}
	if sessionKey != "" {
		if topic, err := a.store.config(myAgentsTopicPrefix + sessionKey); err == nil && protocol.ValidID(topic) {
			return topic, nil
		}
	}
	topic := protocol.NewID()
	if sessionKey == "" {
		return topic, nil
	}
	return topic, a.store.setConfig(map[string]string{myAgentsTopicPrefix + sessionKey: topic})
}

// topicParticipation finds or invites the exact agent into conv's topic
// only. Own agents accept without a click (selfConsent); taskKeys are the
// person's own device keys, so own agents may task each other (owner rule).
func (a *Agent) topicParticipation(ctx context.Context, conv, topic, host, agentID string, taskKeys []string) (ParticipationInfo, error) {
	infos, err := a.Participations(conv)
	if err != nil {
		return ParticipationInfo{}, err
	}
	for _, p := range infos {
		if p.Topic != nil && *p.Topic == topic && p.Host.Address == host && p.AgentID == agentID && p.Role == "" && (p.Claimable() || p.State == PartInvited) {
			return p, nil
		}
	}
	return a.InviteAgentInScope(ctx, conv, host, agentID, nil, taskKeys, "", &topic)
}

// awaitClaimable waits, woken by the local daemon, until pid is active.
func (a *Agent) awaitClaimable(ctx context.Context, pid string) (ParticipationInfo, error) {
	conn, woke, gone, err := a.subscribeLocalChanges()
	if err != nil {
		return ParticipationInfo{}, err
	}
	defer conn.Close()
	for {
		p, err := a.Participation(pid)
		if err != nil || p.Claimable() {
			return p, err
		}
		if p.State != PartInvited {
			return p, fmt.Errorf("the agent's invitation is %s", p.State)
		}
		select {
		case <-ctx.Done():
			return p, fmt.Errorf("the agent has not joined yet: %w", ctx.Err())
		case <-woke:
		case <-gone:
			return p, ErrDaemonNotRunning
		}
	}
}

// AgentRoomAsk is a terminal agent's question or task to another agent:
// one of its person's own, or another person's visiting the room.
type AgentRoomAsk struct {
	From       LocalAgentInfo // the asking agent, hosted here
	SessionKey string         // the asking native session; one topic per session
	Host       string         // the asked agent's own device
	AgentID    string         // the asked agent
	Kind       string
	Body       string
	ReplyTo    string // only picks the topic: a terminal ask has no upstream request
	Cause      string // a running request in the room this ask serves (RunAgentRoomAsk)
	Files      []OutgoingFile
	Receiver   *ReplyReceiver
	Join       time.Duration // how long to wait for both agents to join the topic
}

// AgentRoomSent says where the request went.
type AgentRoomSent struct {
	ConvSent
	Conv, Topic, From, To string // the room, topic and the two participations
}

// SendAgentRoomAsk posts the request in the person's "My agents" room as
// the asking agent, inviting both agents into the job's topic first.
func (a *Agent) SendAgentRoomAsk(ctx context.Context, r AgentRoomAsk) (AgentRoomSent, error) {
	var out AgentRoomSent
	if r.Kind != envelope.KindQuestion && r.Kind != envelope.KindTask {
		return out, errors.New("choose question or task")
	}
	if r.From.Record.ID == "" || r.From.Record.ID == r.AgentID && r.Host == a.Address {
		return out, errors.New("an agent asks another agent, not itself")
	}
	keys, err := a.ownHumanTaskKeys()
	if err != nil {
		return out, err
	}
	if out.Conv, err = a.MyAgentsRoom(ctx); err != nil {
		return out, err
	}
	topicRef := r.ReplyTo
	if r.Cause != "" {
		topicRef = r.Cause
	}
	if out.Topic, err = a.agentRoomTopic(out.Conv, r.SessionKey, topicRef); err != nil {
		return out, err
	}
	from, err := a.topicParticipation(ctx, out.Conv, out.Topic, a.Address, r.From.Record.ID, keys)
	if err != nil {
		return out, err
	}
	// Another person's agent joins the topic as a visitor (owner, October 29,
	// 2026): its host decides by its own person's permissions, so this
	// invite names no task keys and grants nothing there.
	own, err := a.OwnAgentHost(r.Host)
	if err != nil {
		return out, err
	}
	own = own || r.Host == a.Address
	toKeys := keys
	if !own {
		toKeys = nil
	}
	to, err := a.topicParticipation(ctx, out.Conv, out.Topic, r.Host, r.AgentID, toKeys)
	if err != nil {
		return out, err
	}
	out.From, out.To = from.PID, to.PID
	join := r.Join
	if join <= 0 {
		join = 30 * time.Second
	}
	jctx, cancel := context.WithTimeout(ctx, join)
	defer cancel()
	if from, err = a.awaitClaimable(jctx, from.PID); err != nil {
		return out, err
	}
	if to, err = a.awaitClaimable(jctx, to.PID); err != nil {
		if !own && to.State == PartInvited {
			return out, fmt.Errorf("%s's agent has not joined the topic yet: its person accepts the invitation there unless they approved you; ask again after it joins (%w)", r.Host, err)
		}
		return out, err
	}
	harness := "room"
	if r.From.Responder != nil && r.From.Responder.Harness != "" {
		harness = r.From.Responder.Harness
	}
	out.ConvSent, err = a.postAgentAsk(ctx, out.Conv, out.Topic, r.Cause, from, to, r.Kind, r.Body, harness, r.Files, r.Receiver)
	return out, err
}

// postAgentAsk posts a request as agent from, a participation hosted here,
// to agent to in conv. With no cause the receiving host checks it as a
// request from this device; with one, through that cause (roomChain).
func (a *Agent) postAgentAsk(ctx context.Context, conv, topic, cause string, from, to ParticipationInfo, kind, body, harness string, files []OutgoingFile, receiver *ReplyReceiver) (ConvSent, error) {
	h, err := a.roomAudience(conv, from.PID, topic)
	if err != nil {
		return ConvSent{}, err
	}
	target := &envelope.Target{Address: to.Host.Address, Fingerprint: to.Host.Fingerprint, AgentID: to.AgentID}
	if m, e := a.dmMembers(conv); e != nil {
		return ConvSent{}, e
	} else if m.group != nil {
		adm, e := groupMemberAdmission(a.store.db, *m.group, a.Address, a.Self().Fingerprint())
		if e != nil {
			return ConvSent{}, e
		}
		target.GroupAdmission = adm.Hash()
	}
	return a.SendConv(ctx, conv, ConvOutgoing{Kind: kind, PID: to.PID, Body: body, Topic: topic, ReplyTo: cause, Files: files,
		Origin: envelope.OriginAgentPrefix + strings.ToLower(harness), Emotion: "neutral", Target: target, human: h, selfJob: to.HostHere, ReplyReceiver: receiver})
}

// RunAgentRoomAsk is SendAgentRoomAsk from inside running request cause on
// this device: as that run's own agent, one topic per cause. Chain rule
// (owner, October 29, 2026): a run may task the person's other agents only
// when its requester may task this agent (its own person, a task admitted
// here, or a standing task grant); a requester who may only ask makes it
// only ask. Receiving own devices cannot check this; this host does.
func (a *Agent) RunAgentRoomAsk(ctx context.Context, cause string, r AgentRoomAsk) (AgentRoomSent, error) {
	var sender, key, kind, state, conv, lid, agentID, responder string
	err := a.store.db.QueryRow(`SELECT sender, coalesce(verified_by,''), kind, state, coalesce(conv,''), coalesce(lid,''),
		coalesce(nullif(agent_id,''), json_extract(target,'$.agent_id'), ''), coalesce(responder,'') FROM inbox WHERE id=? AND replica=0`, cause).
		Scan(&sender, &key, &kind, &state, &conv, &lid, &agentID, &responder)
	if errors.Is(err, sql.ErrNoRows) || err == nil && state != stateRunning {
		return AgentRoomSent{}, errors.New("this run's request is not running here")
	}
	if err != nil {
		return AgentRoomSent{}, err
	}
	if r.Kind == envelope.KindTask {
		if ok, err := a.runMayTask(sender, key, kind); err != nil {
			return AgentRoomSent{}, err
		} else if !ok {
			return AgentRoomSent{}, fmt.Errorf("%s may only ask this agent questions, so this run may only ask other agents questions for them", sender)
		}
	}
	if r.From, err = a.SessionAgent(responder, agentID); err != nil {
		return AgentRoomSent{}, err
	}
	room, err := a.MyAgentsRoom(ctx)
	if err != nil {
		return AgentRoomSent{}, err
	}
	r.SessionKey = "run/" + cause
	if conv == room && lid != "" {
		r.Cause = lid // continues the cause's chain and topic
	}
	return a.SendAgentRoomAsk(ctx, r)
}

// runMayTask: may a run serving a kind request from sender's key task the
// person's other agents? A running task was admitted as one here; otherwise
// only for the person's own device or a standing task grant.
func (a *Agent) runMayTask(sender, key, kind string) (bool, error) {
	if kind == envelope.KindTask {
		return true, nil
	}
	if own, err := a.ownHumanAuthor(a.store.db, sender, key); err != nil || own {
		return own, err
	}
	return taskGranted(a.store.db, sender, key)
}

// OwnAgentHost reports whether host is one of this person's own current
// verified human devices, whose agents a terminal agent reaches in the room.
func (a *Agent) OwnAgentHost(host string) (bool, error) {
	devices, err := a.OwnHumanDevices()
	if err != nil {
		return false, err
	}
	for _, d := range devices {
		if d.Address == host {
			return true, nil
		}
	}
	return false, nil
}

// RoomTargetAgent picks the asked agent on host: agentID when given, else
// host's only agent other than the asker. Several agents need --agent.
func (a *Agent) RoomTargetAgent(ctx context.Context, host, agentID, asker string) (string, error) {
	type choice struct{ id, label, harness string }
	var all []choice
	if host == a.Address {
		agents, err := a.LocalAgents()
		if err != nil {
			return "", err
		}
		for _, e := range agents {
			h := ""
			if e.Responder != nil {
				h = e.Responder.Harness
			}
			all = append(all, choice{e.Record.ID, e.Record.Label, h})
		}
	} else {
		catalog, err := a.AgentCatalog(ctx, host)
		if err != nil {
			return "", err
		}
		for _, r := range catalog {
			all = append(all, choice{r.ID, r.Label, r.Harness})
		}
	}
	var left []choice
	for _, c := range all {
		if agentID != "" && c.id == agentID {
			return c.id, nil
		}
		if c.id != asker {
			left = append(left, c)
		}
	}
	switch {
	case agentID != "":
		return "", ErrUnknownAgent
	case len(left) == 0:
		return "", fmt.Errorf("%s has no other agent to ask", host)
	case len(left) > 1:
		names := make([]string, len(left))
		for i, c := range left {
			names[i] = fmt.Sprintf("%s (%s %s)", c.id, c.label, c.harness)
		}
		return "", fmt.Errorf("%s has several agents; choose one with --agent ID: %s", host, strings.Join(names, ", "))
	}
	return left[0].id, nil
}

// ownHumanTaskKeys are this person's current verified human device keys.
func (a *Agent) ownHumanTaskKeys() ([]string, error) {
	devices, err := a.OwnHumanDevices()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(devices))
	for _, d := range devices {
		keys = append(keys, d.Fingerprint)
	}
	return keys, nil
}
