package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ConversationResources is the main row of the existing shared metadata register.
// Links are context only: they grant no provider access or execution authority.
type ConversationResources struct {
	Links  []protocol.ResourceLink `json:"links"`
	Rev    int64                   `json:"rev"`
	Writer string                  `json:"writer"`
	LID    string                  `json:"lid"`
}

var ErrConversationResourcesStale = errors.New("shared links changed; reopen the current links before saving")

// ConversationResources reads shared metadata only for a current member device.
// Guest projections and agent-job admission remain separate existing boundaries.
func (a *Agent) ConversationResources(conv string) (ConversationResources, error) {
	result := ConversationResources{Links: []protocol.ResourceLink{}}
	members, err := a.dmMembers(conv)
	if err != nil {
		return result, err
	}
	if members.group == nil || !members.device(a.Address, a.Self().Fingerprint()) {
		return result, errors.New("shared links require current group membership")
	}
	record, _, err := sharedTopicTitle(a.store.db, conv, "")
	if err != nil {
		return result, err
	}
	if record.resources != "" {
		if err = json.Unmarshal([]byte(record.resources), &result.Links); err != nil {
			return result, err
		}
	}
	result.Rev, result.Writer, result.LID = record.Rev, record.Writer, record.LID
	return result, nil
}

// SetConversationResources uses ordinary human-member metadata admission and the
// existing register's transactional stale-edit fence. It never starts a job.
func (a *Agent) SetConversationResources(ctx context.Context, conv string, links []protocol.ResourceLink, rev int64, writer string) (ConversationResources, error) {
	current, err := a.ConversationResources(conv)
	if err != nil {
		return current, err
	}
	if current.Rev != rev || current.Writer != writer || rev >= protocol.MaxTopicTitleRevision {
		return current, ErrConversationResourcesStale
	}
	if links == nil {
		links = []protocol.ResourceLink{}
	}
	if err = protocol.ValidateResourceLinks(links); err != nil {
		return current, err
	}
	pid, err := a.ownHumanPID(conv, "")
	if err != nil {
		return current, err
	}
	if pid != "" {
		return current, errors.New("only current human group members may edit shared links")
	}
	_, err = a.SendConv(ctx, conv, ConvOutgoing{Kind: envelope.KindMessage, TopicEvent: &envelope.TopicEvent{Action: "resources", Resources: &links, Rev: rev + 1}})
	if err != nil {
		if errors.Is(err, ErrSharedTopicTitleStale) {
			err = ErrConversationResourcesStale
		}
		return current, err
	}
	return a.ConversationResources(conv)
}

// An accepted local admission republishes the current register as a new,
// ordinary signed human turn. The publication ledger, membership installation,
// outbox and register CAS commit together; receivers never initiate this work.
func (a *Agent) groupResourcesPublicationCopies(ctx context.Context, packet GroupContext, invitation string, carriers []outCopy) ([]outCopy, func(*sql.Tx) error, error) {
	if invitation == "" {
		return nil, nil, nil
	}
	old, found, err := sharedTopicTitle(a.store.db, packet.State.Conv, "")
	if err != nil || !found || old.resources == "" {
		return nil, nil, err
	}
	r, err := groupInvitationIn(a.store.db, invitation, "out")
	if err != nil {
		return nil, nil, err
	}
	var consent protocol.GroupConsent
	member, present := packet.State.Member(r.Proposal.Target)
	if json.Unmarshal(r.consent, &consent) != nil || consent.Validate() != nil || consent.Invitation != invitation || consent.Admission == nil ||
		!present || member.Admission.Hash() != consent.Admission.Hash() || r.Proposal.Seq != packet.State.Seq ||
		r.Proposal.Prev != packet.State.Prev || r.Proposal.Root.ID() != packet.Root.ID() ||
		r.Inviter != a.Address || r.peerFP != a.Self().Fingerprint() {
		return nil, nil, ErrGroupInvitationStale
	}
	var links []protocol.ResourceLink
	if err = json.Unmarshal([]byte(old.resources), &links); err != nil {
		return nil, nil, err
	}
	m := ConvOutgoing{TopicEvent: &envelope.TopicEvent{Action: "resources", Resources: &links, Rev: old.Rev + 1}}
	if err = a.prepareSharedTopicMetadata(packet.State.Conv, &m); err != nil {
		return nil, nil, err
	}
	if err = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return nil, nil, err
	}
	admission, err := groupMemberAdmission(a.store.db, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return nil, nil, err
	}
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		return nil, nil, err
	}
	selfRosterHash := me.roster.Hash()
	admissionHash := admission.Hash()
	root, _ := json.Marshal(packet.Root)
	lid := protocol.NewID()
	seen := map[string]bool{}
	var copies []outCopy
	for _, carrier := range carriers {
		if carrier.in.PID != "" || seen[carrier.env.To] {
			continue
		}
		seen[carrier.env.To] = true
		key, e := a.sendKey(ctx, carrier.env.To)
		if e != nil {
			return nil, nil, e
		}
		if e = groupTurnCheck(a.store.db, packet, key.Address, key.Fingerprint()); e != nil {
			return nil, nil, e
		}
		person, ok, e := a.store.personByAddress(key.Address)
		if e != nil {
			return nil, nil, e
		}
		if !ok {
			return nil, nil, ErrGroupContextPending
		}
		fan := []envelope.Fan{{Person: me.roster.Person, Roster: selfRosterHash}}
		if person.roster.Person != me.roster.Person {
			fan = append(fan, envelope.Fan{Person: person.roster.Person, Roster: person.roster.Hash()})
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: lid, From: a.Address, To: key.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: packet.State.Conv, Root: root, TopicEvent: m.TopicEvent, Origin: envelope.OriginUI, Replica: person.roster.Person == me.roster.Person, Fan: fan}
		recipient, e := key.Recipient()
		if e != nil {
			return nil, nil, e
		}
		sealed, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return nil, nil, e
		}
		copies = append(copies, outCopy{in: in, env: sealed, state: stateQueued, required: protocol.CapGroup, recipientFP: key.Fingerprint(), groupAdmission: admissionHash})
	}
	check := func(tx *sql.Tx) error {
		intent, e := groupInvitationIn(tx, invitation, "out")
		if e != nil {
			return e
		}
		if !bytes.Equal(intent.consent, r.consent) || intent.Inviter != r.Inviter || intent.peerFP != r.peerFP ||
			(intent.State != "accepted" && intent.State != "history-unavailable" && intent.State != "published" && intent.State != "published-history-unavailable") {
			return ErrGroupInvitationStale
		}
		current, found, e := sharedTopicTitle(tx, packet.State.Conv, "")
		if e != nil {
			return e
		}
		if !found || current != old {
			return ErrSharedTopicTitleStale
		}
		for _, copy := range copies {
			if e := groupTurnCheck(tx, packet, copy.env.To, copy.recipientFP); e != nil {
				return e
			}
			for _, fan := range copy.in.Fan {
				person, ok, e := personByIDIn(tx, fan.Person)
				if e != nil {
					return e
				}
				if !ok || person.roster.Hash() != fan.Roster {
					return ErrGroupContextPending
				}
			}
		}
		return m.beforeStore(tx, copies, envelope.Inner{})
	}
	return copies, check, nil
}
