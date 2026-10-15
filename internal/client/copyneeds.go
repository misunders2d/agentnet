package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// copyNeeds is what one stored copy needs of its reader's signed
// capabilities, derived from the stored row. It is the one list deliver
// hands a copy over by and releaseConv releases a waiting copy by: a copy
// released on a narrower list was parked again by deliver on every members
// push, and one waiting on a requirement only deliver checked was never
// released. Nothing here grants anything: delivery stays per exact
// captured key and every authority check still runs at handover.
const topicContextArchiveProactive = protocol.CapTopicContext + "+" + protocol.CapProactiveAgent

type copyNeeds struct {
	required                                                                       string // the stored requirement, or the one its shape derives
	stored                                                                         string // required_cap as stored
	conv, sub, body, pid, humanRaw, status, agentID, capturedFP                    string
	v                                                                              int
	proactive, progress, receiverCap, room, topicScoped, organizationCap, followup bool
	proposal, groupedWire                                                          bool
	mentionCap, agentOrganizationCap                                               bool
	snapshotCap                                                                    bool
	conversationAudienceCap                                                        bool
	steer                                                                          bool
	// A history copy's carried item. A copy kept as ciphertext only (empty
	// body) carries none: its requirement is the stored column.
	item               HistoryItem
	historical, helper bool // item parsed; an assistant's reaction as history
}

// copyNeedsOf derives what the stored copy id needs (v: its envelope
// version).
func copyNeedsOf(q dbq, id string, v int) (copyNeeds, error) {
	n := copyNeeds{v: v}
	var kind, origin, reply, target string
	if err := q.QueryRow(`SELECT coalesce(required_cap, ''), coalesce(conv, ''),coalesce(sub,''),coalesce(body,''),coalesce(pid,''),coalesce(human,''),coalesce(status,''),coalesce(agent_id,''),coalesce(recipient_fp,''),wire_send_group,coalesce(kind,''),coalesce(origin,''),coalesce(reply_to,''),coalesce(target,''),mention_cap,organization_cap FROM outbox WHERE id=?`, id).
		Scan(&n.stored, &n.conv, &n.sub, &n.body, &n.pid, &n.humanRaw, &n.status, &n.agentID, &n.capturedFP, &n.groupedWire, &kind, &origin, &reply, &target, &n.mentionCap, &n.agentOrganizationCap); err != nil {
		return n, err
	}
	n.required, n.conversationAudienceCap = splitConversationAudienceRequirement(n.stored)
	n.required, n.steer = splitSteerRequirement(n.required)
	n.conversationAudienceCap = n.conversationAudienceCap || conversationAudienceCopy(n.sub, n.body, n.humanRaw)
	if n.required == "" && n.conversationAudienceCap {
		n.required = protocol.CapHumanParticipation
	}
	var snapshotErr error
	n.snapshotCap, snapshotErr = snapshotCopy(q, n.conv, n.pid, n.sub, n.body, n.humanRaw)
	if snapshotErr != nil {
		return n, snapshotErr
	}
	n.snapshotCap = n.snapshotCap || (n.required == protocol.CapTopicContext || n.required == topicContextArchiveProactive)
	if target == "" {
		n.proactive = envelope.ProactiveAgent(envelope.Inner{Conv: n.conv, Kind: kind, Origin: origin, ReplyTo: reply, PID: n.pid, Sub: n.sub, Status: n.status})
	}
	n.progress = n.status == envelope.StatusProgress
	if n.progress && n.required == "" {
		n.required = protocol.CapProgress
	}
	var err error
	if n.receiverCap, err = receiverCopyNeedsCapability(q, id, n.sub, n.body); err != nil {
		return n, err
	}
	if n.required == "" && n.receiverCap {
		n.required = protocol.CapReplyReceiver
	}
	if n.room, err = roomCopy(q, n.conv, n.sub, n.body, n.humanRaw); err != nil {
		return n, err
	}
	if n.required == "" && n.room {
		n.required = protocol.CapRoom
	}
	if n.topicScoped, err = topicCopy(q, n.conv, n.pid, n.sub, n.body, n.humanRaw); err != nil {
		return n, err
	}
	if n.snapshotCap && (n.sub == envelope.SubGroupProof || n.sub == envelope.SubGroupContext) {
		n.required = protocol.CapTopicContext
	}
	if n.required == "" && n.topicScoped {
		n.required = protocol.CapTopicParticipation
	}
	if n.organizationCap, err = topicOrganizationCopy(q, id, n.sub, n.body); err != nil {
		return n, err
	}
	if n.required == "" && n.organizationCap {
		n.required = protocol.CapTopicOrganization
	}
	followup, err := requestFollowupCopyRef(q, id, n.sub, n.body)
	if err != nil {
		return n, err
	}
	n.followup = followup != nil
	n.steer = n.steer || followup != nil && followup.Attempt > 0
	if n.required == "" && n.steer {
		n.required = protocol.CapRequestSteer
	}
	if n.required == "" && n.followup {
		n.required = protocol.CapRequestFollowup
	}
	if n.proposal, err = proposalCopyNeedsCapability(q, id); err != nil {
		return n, err
	}
	if n.required == "" && n.proposal {
		n.required = protocol.CapOwnSyncV3
	}
	if n.sub == envelope.SubHistory && n.body != "" {
		n.historical = json.Unmarshal([]byte(n.body), &n.item) == nil
		_, n.helper = historyAssistant(n.body, n.conv)
		if n.historical {
			n.proactive = envelope.ProactiveAgent(n.item.inner(n.conv))
		}
	}
	if n.required == "" && n.mentionCap {
		n.required = protocol.CapMentionReaction
	}
	if n.required == "" && n.agentOrganizationCap {
		n.required = protocol.CapAgentOrganization
	}
	return n, nil
}

// cacheKey names everything readerCheck depends on besides the reader's
// profile, so one pass checks each such shape once per recipient.
func (n copyNeeds) cacheKey(to string) string {
	item := n.item
	return fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%v%v%v%v%v%v%v%v%v%v%v\x00%s\x00%s\x00%s\x00%s",
		to, n.v, n.required, n.stored, n.conv, n.sub, n.pid, n.humanRaw, n.status, n.agentID,
		n.progress, n.receiverCap, n.room, n.topicScoped, n.organizationCap, n.followup, n.proposal, n.groupedWire, n.historical, n.helper, item.GroupHistory != nil,
		item.Sub, item.Status, item.PID, item.AgentID) + fmt.Sprint(n.proactive, n.mentionCap, n.agentOrganizationCap, n.snapshotCap, n.conversationAudienceCap, n.steer)
}

// copyWait is a reason a copy waits that capSupport words itself.
type copyWait struct{ why string }

func (e *copyWait) Error() string { return e.why }

// readerCheck reports whether key's signed capabilities read the copy n
// describes: nil, a *copyWait or an error wrapping
// errAgentIdentityUnsupported when it must wait for its reader, or another
// error (the Hub out of reach, ...). A copy with no requirement needs
// nothing here (the conversation itself was checked when it was stored).
func (a *Agent) readerCheck(ctx context.Context, to string, key identity.Public, n copyNeeds) error {
	required := n.required
	if required == topicContextArchiveProactive {
		required = protocol.CapTopicContext
		n.proactive = true
	}
	if required == "" {
		return nil
	}
	capWait := func(cap string) error {
		features, err := a.relayFeatures(ctx)
		if err != nil {
			return err
		}
		if ok, why := a.capSupport(ctx, to, key, features, cap); !ok {
			return &copyWait{why: why}
		}
		return nil
	}
	var err error
	if n.conversationAudienceCap {
		if err = a.requireParticipationCaps(ctx, key, protocol.CapConversationAudience); err != nil {
			return err
		}
		if err = a.requireParticipationCaps(ctx, key, protocol.CapHumanParticipation); err != nil {
			return err
		}
	}
	if n.snapshotCap {
		if n.sub == envelope.SubGroupContext || n.sub == envelope.SubGroupProof {
			if err = a.requireParticipationCaps(ctx, key, protocol.CapGroup); err != nil {
				return err
			}
		}
		if err = a.requireParticipationCaps(ctx, key, protocol.CapTopicContext); err != nil {
			return err
		}
	}
	if n.mentionCap {
		if err = a.requireParticipationCaps(ctx, key, protocol.CapMentionReaction); err != nil {
			return err
		}
	}
	if n.agentOrganizationCap {
		if err = a.requireParticipationCaps(ctx, key, protocol.CapAgentOrganization); err != nil {
			return err
		}
	}
	if n.proactive {
		if err = capWait(protocol.CapProactiveAgent); err != nil {
			return err
		}
	}
	if required == protocol.CapControl {
		err = capWait(protocol.CapControl)
	} else {
		err = a.requireParticipationCaps(ctx, key, required)
	}
	if err == nil && n.sub == envelope.SubHistoryArchive && required != protocol.CapHistoryArchive {
		err = a.requireParticipationCaps(ctx, key, protocol.CapHistoryArchive)
	}
	// Captured human controls retain their participation requirement,
	// and also need the reader's control capability before handover.
	if err == nil && n.v == envelope.Version3 && groupControlSub(n.sub) && required != protocol.CapControl && required != protocol.CapGroup {
		err = capWait(protocol.CapControl)
	}
	if err == nil && n.organizationCap && required != protocol.CapTopicOrganization {
		err = a.requireParticipationCaps(ctx, key, protocol.CapTopicOrganization)
	}
	if err == nil && n.steer && required != protocol.CapRequestSteer {
		err = a.requireParticipationCaps(ctx, key, protocol.CapRequestSteer)
	}
	if err == nil && n.followup && required != protocol.CapRequestFollowup && required != protocol.CapRequestSteer {
		err = a.requireParticipationCaps(ctx, key, protocol.CapRequestFollowup)
	}
	if err == nil && n.proposal && required != protocol.CapOwnSyncV3 {
		err = a.requireParticipationCaps(ctx, key, protocol.CapOwnSyncV3)
	}
	if err == nil && n.receiverCap && required != protocol.CapReplyReceiver {
		err = a.requireParticipationCaps(ctx, key, protocol.CapReplyReceiver)
	}
	// Progress needs prg1 and, besides, whatever its author's identity or
	// participation already needs: never one without the other.
	if err == nil && n.progress && required != protocol.CapProgress {
		err = a.requireParticipationCaps(ctx, key, protocol.CapProgress)
	}
	if err == nil && n.progress && required == protocol.CapProgress && n.agentID != "" {
		err = a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
	}
	if err == nil && (required == protocol.CapAgentReaction || required == protocol.CapMentionReaction) { // and what the assistant's own reply needs there
		err = a.assistantReactionCaps(ctx, key, n.conv, n.pid, n.agentID)
	}
	if err == nil && (required == protocol.CapAgentReaction || required == protocol.CapMentionReaction) && n.humanRaw != "" { // to a captured audience: as a human-audience turn
		err = a.requireParticipationCaps(ctx, key, protocol.CapHumanParticipation)
	}
	if err == nil && n.groupedWire {
		err = a.requireParticipationCaps(ctx, key, protocol.CapSendGroup)
	}
	if err == nil && n.topicScoped {
		err = a.requireParticipationCaps(ctx, key, protocol.CapTopicParticipation)
	}
	// A room shape needs rm1 besides its primary requirement (ROOM_V1 §2.5).
	if err == nil && n.room && required != protocol.CapRoom {
		err = a.requireParticipationCaps(ctx, key, protocol.CapRoom)
	}
	control := n.v == envelope.Version3 && groupControlSub(n.sub)
	status := n.sub == envelope.SubStatus
	pid := n.pid
	if n.historical {
		control = groupControlSub(n.item.Sub)
		status = n.item.Sub == envelope.SubStatus
		pid = n.item.PID
	}
	if err == nil && n.historical && n.item.GroupHistory != nil {
		err = a.requireParticipationCaps(ctx, key, protocol.CapOwnSyncV2)
	}
	if err == nil && required == protocol.CapGroup && control {
		err = a.requireGroupControlCapability(ctx, key)
	}
	if err == nil && required == protocol.CapGroup && status {
		err = a.requireParticipationCaps(ctx, key, protocol.CapHeadless)
	}
	if err == nil && required == protocol.CapGroup && n.historical && n.item.PID != "" {
		err = a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
	}
	if err == nil && required == protocol.CapGroup && pid != "" {
		cap := protocol.CapAgentIdentity
		if n.sub == envelope.SubGroupProof || n.sub == envelope.SubGroupContext {
			cap = protocol.CapExternalParticipation
		} else if p, e := a.participation(n.conv, pid); e == nil && p.External && (p.Host.Address == to || n.historical && n.item.PID != "") {
			cap = protocol.CapExternalParticipation
		}
		err = a.requireParticipationCaps(ctx, key, cap)
	}
	// An assistant's reaction as history needs agr1 and what that
	// assistant's own output needs there, besides the copy's own
	// participation or group requirement: never to an older reader.
	if err == nil && n.helper {
		if required != protocol.CapAgentReaction {
			err = a.requireParticipationCaps(ctx, key, protocol.CapAgentReaction)
		}
		if err == nil {
			err = a.assistantReactionCaps(ctx, key, n.conv, n.item.PID, n.item.AgentID)
		}
	}
	return err
}

// errRosterKey: a device's key is not the one its person's roster names.
var errRosterKey = errors.New("its key is not the one its person's roster names")

// deviceKeyUnusable reports whether err from sendKey (or errRosterKey)
// says that one device cannot be sent a copy now: removed by a Hub admin,
// unknown to the Hub, or a changed key not trusted here. That device gets
// nothing (fail closed for it alone); the others still do.
func deviceKeyUnusable(err error) bool {
	var changed *KeyChangedError
	var he *HubError
	return errors.Is(err, ErrPeerRevoked) || errors.Is(err, errRosterKey) || errors.As(err, &changed) || errors.As(err, &he) && he.Status == http.StatusNotFound
}
