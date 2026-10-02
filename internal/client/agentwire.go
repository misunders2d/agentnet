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

func agentRequirement(in envelope.Inner) string {
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
	if in.Sub == envelope.SubExcerpt && in.PID != "" {
		return protocol.CapExternalParticipation
	}
	if namedAgentFields(in) {
		return protocol.CapAgentIdentity
	}
	if in.Sub == envelope.SubEvent {
		var e protocol.ParticipationEvent
		if json.Unmarshal([]byte(in.Body), &e) == nil && e.Host != nil && e.Host.AgentID != "" {
			return protocol.CapAgentIdentity
		}
	}
	return ""
}

func namedAgentFields(in envelope.Inner) bool {
	return in.AgentID != "" || in.Target != nil && in.Target.AgentID != ""
}

func (a *Agent) requireAgentIdentity(ctx context.Context, key identity.Public) error {
	return a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
}

func copyRequirement(c outCopy) string {
	if c.required != "" {
		return c.required
	}
	return agentRequirement(c.in)
}

func (a *Agent) requireParticipationCaps(ctx context.Context, key identity.Public, required string) error {
	if required != protocol.CapAgentIdentity && required != protocol.CapExternalParticipation && required != protocol.CapGroup && required != protocol.CapHeadless && required != protocol.CapReplyReceiver {
		return errors.New("unknown queued capability requirement")
	}
	label, device, err := protocol.SplitAddress(key.Address)
	if err != nil {
		return err
	}
	var profile protocol.Profile
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil {
		return err
	}
	if !profile.Supports(key.Address, key.SignKey, required) || required == protocol.CapExternalParticipation && !profile.Supports(key.Address, key.SignKey, protocol.CapAgentIdentity) {
		if required == protocol.CapReplyReceiver {
			return fmt.Errorf("%w: %s cannot read selected receiver delegation yet", errAgentIdentityUnsupported, key.Address)
		}
		if required == protocol.CapGroup {
			return fmt.Errorf("%w: %s cannot read group context yet", errAgentIdentityUnsupported, key.Address)
		}
		return fmt.Errorf("%w: %s cannot read named agents yet; update all its active AgentNet sessions", errAgentIdentityUnsupported, key.Address)
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

// Named conversation identities are bound by the signed invitation, not by
// an origin label. Historical copies use their sibling-vouched original key.
// Missing participation evidence stays on the existing proof retry path.
func (a *Agent) checkConversationAgent(in envelope.Inner, sender identity.Public) (string, error) {
	if !namedAgentFields(in) {
		return "", nil
	}
	if in.PID == "" || !protocol.ValidID(in.PID) {
		return reasonInvalid, errors.New("named conversation turn has no participation")
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
	if in.Target != nil && (in.Target.AgentID != p.AgentID || in.Target.Address != p.Host.Address || in.Target.Fingerprint != p.Host.Fingerprint) {
		return reasonInvalid, errors.New("named request differs from its participation host")
	}
	if in.AgentID != "" && (!protocol.ValidID(in.AgentID) || in.AgentID != p.AgentID || sender.Address != p.Host.Address || sender.Fingerprint() != p.Host.Fingerprint || in.Kind != envelope.KindAnswer && in.Kind != envelope.KindResult || in.ReplyTo == "" || in.Sub != "") {
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
