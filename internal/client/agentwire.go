package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Capability requirements survive offline queuing and recipient-encrypted
// history copies: the sender cannot decrypt that ciphertext when retrying.
const agentCapabilitySchema = `ALTER TABLE outbox ADD COLUMN required_cap TEXT;`

var errAgentIdentityUnsupported = fmt.Errorf("%w: named agent capability unavailable", errPermanent)

// agentRequirement is a copy's primary requirement. A room shape keeps its
// conversation's primary (a group's grp1, a DM's hgp1); delivery adds rm1
// to it (roomCopy, ROOM_V1 §2.5).
func agentRequirement(in envelope.Inner) string {
	if in.Sub == envelope.SubReadSync {
		r, err := protocol.ParseReadSync([]byte(in.Body))
		if err == nil && len(r.Marks) > 0 {
			return protocol.CapReadSyncV2
		}
		return protocol.CapReadSync
	}
	if in.Followup != nil && in.Conv == "" {
		if in.Followup.Attempt > 0 {
			return protocol.CapRequestSteer
		}
		return protocol.CapRequestFollowup
	}
	if isResponderProgress(in) && in.Conv == "" { // a conversation's progress keeps its own requirement; delivery adds prg1
		return protocol.CapProgress
	}
	if in.Human != nil {
		if root, err := protocol.ParseConvRoot(in.Root); err == nil && root.Kind == protocol.ConvKindGroup {
			return protocol.CapGroup // a captured audience in a group: a group copy
		}
		return protocol.CapHumanParticipation
	}
	if in.ReceiverRoute != nil && in.ReceiverRoute.Op != "request" {
		return protocol.CapReplyReceiver
	}
	if in.Sub == envelope.SubGroupProof || in.Sub == envelope.SubGroupContext {
		return protocol.CapGroup
	}
	if root, err := protocol.ParseConvRoot(in.Root); err == nil && root.Kind == protocol.ConvKindGroup {
		return protocol.CapGroup
	}
	if in.Sub == envelope.SubExcerpt && in.PID != "" {
		return protocol.CapExternalParticipation
	}
	if in.Sub == envelope.SubHistory {
		var h HistoryItem
		if json.Unmarshal([]byte(in.Body), &h) != nil {
			return ""
		}
		in = h.inner(in.Conv)
	}
	if envelope.MentionReaction(in) {
		return protocol.CapMentionReaction
	}
	if envelope.OrganizationReaderExtension(in) {
		return protocol.CapAgentOrganization
	}
	if envelope.AssistantReaction(in) { // as history too: never stored by an older reader as its host's mark
		return protocol.CapAgentReaction
	}
	if in.Sub == envelope.SubExcerpt && in.PID != "" {
		return protocol.CapExternalParticipation
	}
	if in.Human != nil {
		return protocol.CapHumanParticipation
	}
	if namedAgentFields(in) {
		return protocol.CapAgentIdentity
	}
	if in.Sub == envelope.SubEvent {
		var e protocol.ParticipationEvent
		if json.Unmarshal([]byte(in.Body), &e) == nil {
			if e.Role == protocol.RoleHuman || e.Type == protocol.EventScope || e.Audience == protocol.AudienceRoom { // a scope exists only for human and room audiences
				return protocol.CapHumanParticipation
			}
			if e.Host != nil && e.Host.AgentID != "" {
				return protocol.CapAgentIdentity
			}
		}
	}
	return ""
}

// roomCopy reports whether a stored copy is a room shape (ROOM_V1 §2.5),
// which delivery sends only to a reader of rm1, besides its primary
// requirement: an event of a room participation, a captured audience on a
// group root, with a room scope or an agent author, or on an edit; a
// history copy by the item it carries. It reads the copy's stored columns
// (conv, sub, body, human).
func roomCopy(q dbq, conv, sub, body, human string) (bool, error) {
	if sub == envelope.SubHistory {
		var item HistoryItem
		if body == "" || json.Unmarshal([]byte(body), &item) != nil {
			return false, nil
		}
		sub, body, human = item.Sub, item.Body, humanJSON(item.Human)
	}
	if human != "" {
		var h envelope.HumanTurn
		if err := json.Unmarshal([]byte(human), &h); err != nil {
			return false, err
		}
		if sub == envelope.SubRevision || sub == envelope.SubRetraction || h.AgentAuthor() {
			return true, nil
		}
		for _, e := range h.Proof {
			if e.Audience == protocol.AudienceRoom {
				return true, nil
			}
		}
		root, _, found, err := conversationIn(q, conv)
		return found && root.Kind == protocol.ConvKindGroup, err
	}
	var e protocol.ParticipationEvent
	if sub != envelope.SubEvent || json.Unmarshal([]byte(body), &e) != nil {
		return false, nil
	}
	if e.Audience == protocol.AudienceRoom {
		return true, nil
	}
	var n int // a decision or end of a room participation held here
	err := q.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND pid=? AND json_extract(event,'$.audience')=?`, conv, e.PID, protocol.AudienceRoom).Scan(&n)
	return n > 0, err
}

func namedAgentFields(in envelope.Inner) bool {
	return in.AgentID != "" || in.Target != nil && in.Target.AgentID != ""
}

func (a *Agent) requireAgentIdentity(ctx context.Context, key identity.Public) error {
	return a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
}

func copyRequirement(c outCopy) string {
	required := c.required
	if required == "" {
		required = agentRequirement(c.in)
	}
	guest, _ := guestAgentCopy(nil, c.in.Conv, c.in.PID, c.in.Sub, c.in.Body, humanJSON(c.in.Human))
	required = guestAgentRequirement(required, guest)
	followup := c.in.Followup
	if followup == nil {
		followup, _ = carriedFollowup(c.in.Sub, c.in.Body)
	}
	required = steerRequirement(required, followup != nil && followup.Attempt > 0)
	needed := c.in.Human != nil && c.in.Human.ConversationAudience()
	if !needed && c.in.Sub == envelope.SubHistory {
		needed = conversationAudienceCopy(c.in.Sub, c.in.Body, "")
	}
	return conversationAudienceRequirement(required, needed)
}

type readerCapsSnapshot struct {
	profile protocol.Profile
	loaded  bool
}

func (a *Agent) requireParticipationCaps(ctx context.Context, key identity.Public, required string) error {
	var snapshot readerCapsSnapshot
	return a.requireParticipationCapsSnapshot(ctx, key, required, &snapshot)
}

// A snapshot belongs to one readerCheck decision, never to a device or flush:
// a member list may clear the shared profile cache between its checks.
func (a *Agent) requireParticipationCapsSnapshot(ctx context.Context, key identity.Public, required string, snapshot *readerCapsSnapshot) error {
	switch required {
	case protocol.CapHistoryArchive, protocol.CapSharedTopicTitle, protocol.CapConversationResources, protocol.CapGuestAgent,
		protocol.CapConversationAudience, protocol.CapTopicContext, protocol.CapModelSync,
		protocol.CapModelSyncV2, protocol.CapDirectTopicSync, protocol.CapTopicStateSync, protocol.CapTopicStateSyncV2, protocol.CapReminderSync, protocol.CapTopicOrganization,
		protocol.CapRequestFollowup, protocol.CapRequestSteer, protocol.CapTopicParticipation,
		protocol.CapOwnSyncV3, protocol.CapOwnSyncV2, protocol.CapContinuation,
		protocol.CapSendGroup, protocol.CapReadSync, protocol.CapReadSyncV2,
		protocol.CapGroupInvitationManage, protocol.CapGroupInvitationControl,
		protocol.CapGroupHumanParticipation, protocol.CapRootSync, protocol.CapHumanParticipation,
		protocol.CapAgentIdentity, protocol.CapExternalParticipation, protocol.CapGroup,
		protocol.CapHeadless, protocol.CapReplyReceiver, protocol.CapProgress,
		protocol.CapAgentReaction, protocol.CapConvClear, protocol.CapRoom,
		protocol.CapProactiveAgent, protocol.CapMentionReaction, protocol.CapAgentOrganization:
	default:
		return errors.New("unknown queued capability requirement")
	}
	if !snapshot.loaded {
		profile, err := a.peerProfile(ctx, key.Address)
		if err != nil {
			return err
		}
		snapshot.profile, snapshot.loaded = profile, true
	}
	if !snapshot.profile.Supports(key.Address, key.SignKey, required) || required == protocol.CapExternalParticipation && !snapshot.profile.Supports(key.Address, key.SignKey, protocol.CapAgentIdentity) {
		return &NeedsUpdateError{Address: key.Address, Cap: required}
	}
	return nil
}

// A device output may assert only the agent on the request sent to that exact
// host key. Agent names and catalog labels never supply this binding.
func (a *Agent) checkDeviceAgent(in envelope.Inner, sender identity.Public) error {
	if t := in.Target; t != nil && (t.Address != a.Address || t.Fingerprint != a.Self().Fingerprint()) {
		return errors.New("named request targets another device key")
	}
	if in.AgentID == "" {
		return nil
	}
	var raw string
	err := a.store.db.QueryRow(`SELECT coalesce(target, '') FROM outbox WHERE id=? AND recipient=? AND conv IS NULL`, in.ReplyTo, sender.Address).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		originals, e := importedReceiverOriginals(a.store.db, in)
		if e != nil {
			return e
		}
		if len(originals) != 1 {
			return errors.New("named answer has no unambiguous imported request")
		}
		ok, e := importedReceiverMatches(a.store.db, originals[0], in, sender.Fingerprint())
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("named answer differs from imported request identity")
		}
		return nil
	}
	if err != nil {
		return err
	}
	var target envelope.Target
	if json.Unmarshal([]byte(raw), &target) != nil || target.Address != sender.Address || target.Fingerprint != sender.Fingerprint() || target.AgentID != in.AgentID {
		return errors.New("named answer does not match the requested agent and host key")
	}
	return nil
}

// agentTurn reports whether an ordinary conversation turn says an agent
// wrote it: a participation's output (answer, result or progress), named or
// not, or any turn whose origin says so. Only the exact host device of its
// participation sends one (checkConversationAgent).
func agentTurn(sub, kind, status, replyTo, pid, origin string) bool {
	if sub != "" {
		return false
	}
	output := kind == envelope.KindAnswer || kind == envelope.KindResult || kind == envelope.KindMessage && status == envelope.StatusProgress && replyTo != ""
	return pid != "" && output || envelope.AgentOrigin(origin)
}

// Named conversation identities are bound by the signed invitation, not by
// an origin label, and so is every agent's turn: it comes from its
// participation's exact host address and key, while the participation is
// active. Historical copies use their sibling-vouched original key, and may
// outlive the participation. Missing participation evidence stays on the
// existing proof retry path.
func (a *Agent) checkConversationAgent(in envelope.Inner, sender identity.Public, historical bool) (string, error) {
	if envelope.AgentTopicOrganization(in) {
		return a.topicOrganizationAdmission(a.store.db, in, sender.Address, sender.Fingerprint(), historical)
	}
	agent := agentTurn(in.Sub, in.Kind, in.Status, in.ReplyTo, in.PID, in.Origin)
	if !namedAgentFields(in) && !agent && !(in.Sub == "" && in.PID != "" && in.Target != nil) || envelope.AssistantReaction(in) { // a reaction is bound by assistantHistoryCheck / admitAssistantReaction
		return "", nil
	}
	if in.PID == "" || !protocol.ValidID(in.PID) {
		return reasonInvalid, errors.New("an agent's or named conversation turn has no participation")
	}
	p, err := a.participation(in.Conv, in.PID)
	if errors.Is(err, ErrNoParticipation) {
		return reasonProof, err
	}
	if err != nil {
		return "", err
	}
	if p.Invite == "" || p.State == PartConflict {
		return reasonProof, errors.New("named participation has no unambiguous invitation")
	}
	if err := checkParticipationTopic(a.store.db, p.Topic, in); err != nil {
		if errors.Is(err, errParticipationTopicPending) {
			return reasonProof, err
		}
		return reasonInvalid, err
	}
	if in.Target != nil && (in.Target.AgentID != p.AgentID || in.Target.Address != p.Host.Address || in.Target.Fingerprint != p.Host.Fingerprint) {
		return reasonInvalid, errors.New("named request differs from its participation host")
	}
	if in.Human != nil && in.Human.AgentAuthor() {
		p, err = a.participation(in.Conv, in.Human.AuthorPID)
		if err != nil {
			return reasonProof, err
		}
	}
	if agent && (p.Role == protocol.RoleHuman || sender.Address != p.Host.Address || sender.Fingerprint() != p.Host.Fingerprint) {
		return reasonInvalid, errors.New("an agent's turn is not from its participation's exact host")
	}
	if envelope.ProactiveAgent(in) && in.AgentID != p.AgentID {
		return reasonInvalid, errors.New("proactive message differs from its exact admitted agent")
	}
	if agent && !historical {
		// Ended is final, failing closed: an output still in flight when
		// its participation was declined or dismissed is refused for good
		// on a device that learned of the end first, while a device that
		// admitted it earlier keeps it, and one linked later receives that
		// copy as history. Devices may disagree on such an output; that is
		// a known limit (ROOM_V1 §4.1).
		switch {
		case p.State == PartDeclined || p.State == PartDismissed:
			return reasonInvalid, errors.New("an agent's turn after its participation ended")
		case p.State != PartActive || p.Held != 0:
			return reasonProof, errors.New("an agent's turn waits for its participation to be active")
		}
	}
	if in.AgentID != "" && (!protocol.ValidID(in.AgentID) || in.AgentID != p.AgentID || sender.Address != p.Host.Address || sender.Fingerprint() != p.Host.Fingerprint || !envelope.ProactiveAgent(in) && (in.Kind != envelope.KindAnswer && in.Kind != envelope.KindResult && !isResponderProgress(in) || in.ReplyTo == "" || in.Sub != "")) {
		return reasonInvalid, errors.New("named answer differs from its participation host")
	}
	if p.External && in.AgentID != "" {
		m, err := a.dmMembers(in.Conv)
		if err != nil {
			return "", err
		}
		return externalOutputRequest(a.store.db, in, p, m, a.Address, a.Self().Fingerprint())
	}
	return "", nil
}

// verifyAgents marks the agent's turns among msgs that their participation's
// exact host key sent (ConvMessage.VerifiedAgent), by the rule admission
// checks: this device as the host, the key that verified a received copy,
// or for history the original key its own device vouched for. Without
// resolved participation evidence nothing is marked. The members are read
// once, for every participation the turns name.
func (a *Agent) verifyAgents(conv string, msgs []ConvMessage) {
	parts := map[string]*ParticipationInfo{}
	var members *dmMembers
	for i, m := range msgs {
		if m.ExcerptPID != "" || m.PID == "" || !agentTurn(m.Sub, m.Kind, m.status, m.ReplyTo, m.PID, m.Origin) {
			continue
		}
		author := m.PID
		if m.Human != nil && m.Human.AgentAuthor() {
			author = m.Human.AuthorPID
		}
		p, seen := parts[author]
		if !seen {
			if members == nil {
				dm, err := a.dmMembers(conv)
				if err != nil {
					return // no members resolved here: nothing is marked
				}
				members = &dm
			}
			if info, err := participationIn(a.store.db, conv, author, *members, a.Address); err == nil {
				p = &info
			}
			parts[author] = p
		}
		key := m.Key
		if m.History {
			key = m.Claimed
		}
		msgs[i].VerifiedAgent = p != nil && p.Invite != "" && p.State != PartConflict && p.Role != protocol.RoleHuman && m.From == p.Host.Address && key == p.Host.Fingerprint
	}
}

type NeedsUpdateError struct{ Address, Cap string }

func (e *NeedsUpdateError) Error() string {
	message := "cannot read named agents yet; update all its active AgentNet sessions"
	switch e.Cap {
	case protocol.CapHistoryArchive:
		message = "cannot read encrypted history archives yet; update all its active AgentNet sessions"
	case protocol.CapRequestSteer:
		message = "cannot safely receive current-run steering yet; update all its active AgentNet sessions"
	case protocol.CapRequestFollowup:
		message = "cannot safely queue bound request follow-ups yet; update all its active AgentNet sessions"
	case protocol.CapModelSync, protocol.CapModelSyncV2:
		message = "cannot read private model reports yet; update all its active AgentNet sessions"
	case protocol.CapSharedTopicTitle:
		message = "cannot read shared topic names yet; update all its active AgentNet sessions"
	case protocol.CapConversationResources:
		message = "cannot read shared conversation links yet; update all its active AgentNet sessions"
	case protocol.CapTopicOrganization:
		message = "cannot read selected topic moves and merges yet; update all its active AgentNet sessions"
	case protocol.CapTopicContext:
		message = "cannot verify reviewed topic history yet; update all its active AgentNet sessions"
	case protocol.CapGuestAgent:
		message = "cannot enforce guest-derived agent participation yet; update all its active AgentNet sessions"
	case protocol.CapTopicParticipation:
		message = "cannot enforce topic-only participation yet; update all its active AgentNet sessions"
	case protocol.CapOwnSyncV3:
		message = "cannot synchronize direct-agent history or resolve own requests yet; update all its active AgentNet sessions"
	case protocol.CapOwnSyncV2:
		message = "cannot synchronize newer own-device history and invitation views yet; update all its active AgentNet sessions"
	case protocol.CapReadSync, protocol.CapReadSyncV2:
		message = "cannot synchronize read state yet; update all its active AgentNet sessions"
	case protocol.CapDirectTopicSync:
		message = "cannot synchronize direct-agent topic organization yet; update all its active AgentNet sessions"
	case protocol.CapReminderSync:
		message = "cannot synchronize personal reminders yet; update all its active AgentNet sessions"
	case protocol.CapTopicStateSync, protocol.CapTopicStateSyncV2:
		message = "cannot synchronize topic Done, Reopen and Archive marks yet; update all its active AgentNet sessions"
	case protocol.CapGroupInvitationManage:
		message = "cannot receive linked-device invitation cancellation yet; update all its active AgentNet sessions"
	case protocol.CapGroupInvitationControl:
		message = "cannot handle refreshable group invitations yet; update all its active AgentNet sessions"
	case protocol.CapGroupHumanParticipation:
		message = "cannot handle group human guests yet; update all its active AgentNet sessions"
	case protocol.CapHumanParticipation:
		message = "cannot read human participation yet; update all its active AgentNet sessions"
	case protocol.CapReplyReceiver:
		message = "cannot read selected receiver delegation yet"
	case protocol.CapGroup:
		message = "cannot read group context yet"
	case protocol.CapAgentOrganization:
		message = "cannot verify agents’ human-request topic moves yet; update all its active AgentNet sessions"
	case protocol.CapMentionReaction:
		message = "cannot read agents’ ordinary mention reactions yet; update all its active AgentNet sessions"
	case protocol.CapAgentReaction:
		message = "cannot read assistant reactions yet; update all its active AgentNet sessions"
	case protocol.CapProgress:
		message = "cannot read nonterminal responder progress yet; update all its active AgentNet sessions"
	case protocol.CapConvClear:
		message = "cannot apply conversation deletions yet; it deletes it once updated"
	case protocol.CapRoom:
		message = "cannot read room participation yet; update all its active AgentNet sessions"
	}
	return fmt.Sprintf("%v: %s %s", errAgentIdentityUnsupported, e.Address, message)
}
func (e *NeedsUpdateError) Unwrap() error { return errAgentIdentityUnsupported }
