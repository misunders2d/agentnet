package client

import (
	"context"
	"database/sql"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// SendAgentMessage sends an ordinary turn as one exact locally hosted agent.
// Its current invitation is authority; no incoming request or reply is invented.
func (a *Agent) SendAgentMessage(ctx context.Context, pid, topic, body string, files ...OutgoingFile) (ConvSent, error) {
	return a.sendAgentMessage(ctx, pid, topic, body, nil, files...)
}

// sendAgentMessage is SendAgentMessage with before run in the store's
// transaction, after the ordinary authority checks (SendReplyHandle).
func (a *Agent) sendAgentMessage(ctx context.Context, pid, topic, body string, before beforeConvStore, files ...OutgoingFile) (ConvSent, error) {
	p, err := a.Participation(pid)
	if err != nil {
		return ConvSent{}, err
	}
	if !p.HostHere || !p.Claimable() || p.Role != "" {
		return ConvSent{}, errors.New("choose an active agent participation hosted on this device")
	}
	if enabled, err := a.agentEnabled(p.AgentID); err != nil || !enabled {
		return ConvSent{}, errors.New("the invited agent is not enabled on this device")
	}
	if p.Topic != nil {
		if topic != "" && topic != *p.Topic {
			return ConvSent{}, errParticipationTopic
		}
		topic = *p.Topic
	}
	root, raw, found, err := a.store.conversation(p.Conv)
	if err != nil {
		return ConvSent{}, err
	}
	if !found {
		return ConvSent{}, errors.New("the admitted conversation is not held here")
	}
	var audience *envelope.HumanTurn
	if p.Member {
		audience, err = a.roomAudience(p.Conv, p.PID, topic)
	} else if root.Kind == protocol.ConvKindDM {
		audience, err = a.livePlan(ctx, p.Conv, p.PID, p.PID, topic)
	}
	if err != nil {
		return ConvSent{}, err
	}
	out := ConvOutgoing{Kind: envelope.KindMessage, Body: body, Topic: topic, Files: files, PID: p.PID, AgentID: p.AgentID, Origin: envelope.OriginAgentPrefix + "proactive", Emotion: "neutral", human: audience, beforeStore: before}
	out.claim = func(tx *sql.Tx, _ string) error {
		return a.proactiveAuthority(tx, envelope.Inner{Conv: p.Conv, PID: p.PID, AgentID: p.AgentID, Topic: topic})
	}
	if root.Kind == protocol.ConvKindGroup || p.External || audience != nil {
		return a.sendExternalParticipation(ctx, root, raw, p, out)
	}
	return a.SendConv(ctx, p.Conv, out)
}

func (a *Agent) proactiveAuthority(q dbq, in envelope.Inner) error {
	m, err := membersIn(q, in.Conv)
	if err != nil {
		return err
	}
	p, err := participationIn(q, in.Conv, in.PID, m, a.Address)
	if err != nil {
		return err
	}
	if !p.Claimable() || p.Role != "" || p.Host.Address != a.Address || p.Host.Fingerprint != a.Self().Fingerprint() || p.AgentID != in.AgentID {
		return errors.New("the exact agent participation is no longer active on this host")
	}
	return checkParticipationTopic(q, p.Topic, in)
}

// Recheck current signed admission at handover, including copies queued before
// removal. Normal answers retain their separate immutable-request checks.
func (a *Agent) proactiveDeliveryGate(env envelope.Envelope) (bool, error) {
	var in envelope.Inner
	err := a.store.db.QueryRow(`SELECT conv,pid,coalesce(agent_id,''),coalesce(topic,'') FROM outbox WHERE id=? AND kind='message' AND origin='agent:proactive' AND sub IS NULL AND status IS NULL AND reply_to IS NULL AND target IS NULL`, env.ID).Scan(&in.Conv, &in.PID, &in.AgentID, &in.Topic)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err = a.proactiveAuthority(a.store.db, in); err != nil {
		return false, a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: "+err.Error(), "")
	}
	return true, nil
}
