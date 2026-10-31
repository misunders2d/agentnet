package client

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

var errParticipationTopicPending = errors.New("participation: exact topic evidence is pending")
var errParticipationTopic = errors.New("participation: turn is outside the invited topic")

func sameParticipationTopic(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// InviteAgentInScope preserves the legacy whole-chat API while the UI supplies
// an explicit stable topic. A different scope always creates a distinct PID.
func (a *Agent) InviteAgentInScope(ctx context.Context, conv, host, agent string, grant, tasks []string, note string, topic *string) (ParticipationInfo, error) {
	ctx = withRoundTrips(ctx) // the catalog's key check is the invitation's
	if agent != "" {
		records, err := a.AgentCatalog(ctx, host)
		if err != nil {
			return ParticipationInfo{}, err
		}
		found := false
		for _, r := range records {
			found = found || r.ID == agent
		}
		if !found {
			return ParticipationInfo{}, ErrUnknownAgent
		}
	}
	return a.inviteParticipation(ctx, conv, host, agent, grant, tasks, note, "", topic)
}
func (a *Agent) InviteHumanInScope(ctx context.Context, conv, host string, grant []string, note string, topic *string) (ParticipationInfo, error) {
	return a.inviteParticipation(ctx, conv, host, "", grant, nil, note, protocol.RoleHuman, topic)
}

// requireTopicHost: the invited host must enforce topic-only participation
// (tps1), suspended or not; it is the device that runs or speaks for the
// participant. The members' devices are not asked here: one older or
// suspended device must not stop a scoped invitation for everyone (CG-4).
// Every copy carrying the scope (the invite and its records, the
// participant's turns, proofs naming the topic) waits per device for tps1
// at handover and release (copyNeeds.topicScoped), so such a device holds
// no record of the participation and admits nothing from it until it
// updates.
func (a *Agent) requireTopicHost(ctx context.Context, m dmMembers, host protocol.ParticipationHost) error {
	if host.Address == a.Address {
		return nil
	}
	for _, p := range m.persons {
		if d, ok := p.device(host.Address); ok {
			if d.Fingerprint() != host.Fingerprint {
				return errors.New("participation host key changed")
			}
			return a.requireParticipationCaps(ctx, d, protocol.CapTopicParticipation)
		}
	}
	key, err := a.sendKey(ctx, host.Address)
	if err != nil {
		return err
	}
	if key.Fingerprint() != host.Fingerprint {
		return errors.New("participation host key changed")
	}
	return a.requireParticipationCaps(ctx, key, protocol.CapTopicParticipation)
}

// topicReference resolves accepted originals by exact ID first, then one exact
// logical author. Unlike the display projection, an absent parent is not Main.
// Bounds and cycles fail closed; an ID collision cannot select another author.
func topicReference(q dbq, conv, ref, author string) (string, error) {
	seen := map[string]bool{}
	for len(seen) < 256 {
		if ref == "" {
			return "", nil
		}
		if seen[ref] {
			return "", errParticipationTopicPending
		}
		seen[ref] = true
		rows, err := q.Query(`SELECT id,coalesce(lid,id),coalesce(verified_by,claimed_fp,''),coalesce(topic,''),coalesce(reply_to,'') FROM inbox WHERE conv=? AND (id=? OR lid=?) AND coalesce(sub,'')='' AND (verified_by IS NOT NULL OR claimed_fp IS NOT NULL)
UNION ALL SELECT o.id,coalesce(o.lid,o.id),coalesce(d.fingerprint,''),coalesce(o.topic,''),coalesce(o.reply_to,'') FROM outbox o LEFT JOIN person_devices d ON d.address=json_extract(o.envelope,'$.from') WHERE o.conv=? AND (o.id=? OR o.lid=?) AND coalesce(o.sub,'')=''`, conv, ref, ref, conv, ref, ref)
		if err != nil {
			return "", err
		}
		type row struct{ id, lid, key, topic, parent string }
		var candidates []row
		exact := false
		for rows.Next() {
			var r row
			if err = rows.Scan(&r.id, &r.lid, &r.key, &r.topic, &r.parent); err != nil {
				break
			}
			if author != "" && r.key != author {
				continue
			}
			exact = exact || r.id == ref
			candidates = append(candidates, r)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return "", err
		}
		var chosen row
		found := false
		for _, r := range candidates {
			if exact && r.id != ref {
				continue
			}
			if r.key == "" || found && (chosen.lid != r.lid || chosen.key != r.key || chosen.topic != r.topic || chosen.parent != r.parent) {
				return "", errParticipationTopicPending
			}
			chosen = r
			found = true
		}
		if !found {
			return "", errParticipationTopicPending
		}
		if chosen.topic != "" {
			if !protocol.ValidID(chosen.topic) {
				return "", errParticipationTopicPending
			}
			return chosen.topic, nil
		}
		var promoted int
		err = q.QueryRow(`SELECT count(*) FROM (SELECT id FROM inbox WHERE conv=? AND topic=? AND json_valid(topic_event) AND json_extract(topic_event,'$.action')='create' UNION ALL SELECT id FROM outbox WHERE conv=? AND topic=? AND json_valid(topic_event) AND json_extract(topic_event,'$.action')='create')`, conv, chosen.lid, conv, chosen.lid).Scan(&promoted)
		if err != nil {
			return "", err
		}
		if promoted > 0 {
			return chosen.lid, nil
		}
		ref, author = chosen.parent, ""
	}
	return "", errParticipationTopicPending
}
func participationTurnTopic(q dbq, in envelope.Inner) (string, error) {
	if in.Ref != nil {
		return topicReference(q, in.Conv, in.Ref.ID, in.Ref.Fingerprint)
	}
	if in.Topic != "" {
		if !protocol.ValidID(in.Topic) {
			return "", errParticipationTopic
		}
		return in.Topic, nil
	}
	return topicReference(q, in.Conv, in.ReplyTo, "")
}
func checkParticipationTopic(q dbq, topic *string, in envelope.Inner) error {
	if topic == nil || in.Sub == envelope.SubEvent || in.Sub == envelope.SubExcerpt {
		return nil
	}
	got, err := participationTurnTopic(q, in)
	if err != nil {
		return err
	}
	if got != *topic {
		return errParticipationTopic
	}
	return nil
}
func (a *Agent) checkTopicGrants(q dbq, conv string, topic *string, refs []protocol.GrantRef, snapshots ...[]protocol.GrantSnapshot) error {
	if topic == nil {
		return nil
	}
	if len(snapshots) > 0 && len(snapshots[0]) > 0 {
		return topicOrganizationAuthor(q, conv, a.Address, a.Self().Fingerprint())
	}
	for _, ref := range refs {
		got, err := topicReference(q, conv, ref.LID, ref.Fingerprint)
		if err != nil {
			return err
		}
		if got != *topic {
			return errParticipationTopic
		}
	}
	return nil
}

// Captured reader and author scopes are checked independently; the signed
// audience cannot lend one participant another participant's broader access.
func topicAudienceAuthorization(q dbq, in envelope.Inner, m dmMembers, self string) error {
	if in.Human == nil {
		return nil
	}
	for _, s := range in.Human.Audience {
		p, err := participationIn(q, in.Conv, s.PID, m, self)
		if err != nil {
			return err
		}
		if err = checkParticipationTopic(q, p.Topic, in); err != nil {
			return err
		}
	}
	return nil
}

// topicCopy is a second requirement beside the existing room/group/agent
// capability. It also recognizes retained historical proofs after a restart.
func topicCopy(q dbq, conv, pid, sub, body, human string, derived ...*bool) (bool, error) {
	if sub == envelope.SubGroupContext {
		// Body is the encrypted carrier descriptor, not the attached context.
		// Count exact retained scoped records before exposing its memberships.
		var count int
		if err := q.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND json_valid(event) AND json_type(event,'$.topic')='text'`, conv).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}

	if sub == envelope.SubHistory {
		// Ordinary legacy history intentionally keeps no local plaintext body.
		// Scoped participation copies retain their typed proof for this check.
		if body == "" {
			return false, nil
		}
		var item HistoryItem
		if err := json.Unmarshal([]byte(body), &item); err != nil {
			return false, err
		}
		pid, sub, body, human = item.PID, item.Sub, item.Body, humanJSON(item.Human)
		if item.GroupHistory != nil {
			for _, e := range item.GroupHistory.Memberships {
				if e.Topic != nil {
					return true, nil
				}
			}
		}
	}
	if human != "" {
		var h envelope.HumanTurn
		if err := json.Unmarshal([]byte(human), &h); err != nil {
			return false, err
		}
		for _, e := range h.Proof {
			if e.Topic != nil {
				return true, nil
			}
		}
	}
	if sub == envelope.SubEvent {
		var e protocol.ParticipationEvent
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return false, err
		}
		if e.Topic != nil {
			return true, nil
		}
		pid = e.PID
	}
	if conv == "" || pid == "" {
		return false, nil
	}
	var n int
	if len(derived) > 0 {
		var guest int
		err := q.QueryRow(`SELECT count(*),coalesce(sum(CASE WHEN coalesce(json_extract(event,'$.parent'),'')<>'' THEN 1 ELSE 0 END),0) FROM participation_events WHERE conv=? AND pid=? AND json_type(event,'$.topic')='text'`, conv, pid).Scan(&n, &guest)
		*derived[0] = *derived[0] || guest > 0
		return n > 0, err
	}
	err := q.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND pid=? AND json_type(event,'$.topic')='text'`, conv, pid).Scan(&n)
	return n > 0, err
}

// Existing selected native sessions may contain another topic's context. Until
// their receiver contract can bind this scope, refuse before committing copies.
func checkTopicReplyReceiver(q dbq, b *replyBinding, in envelope.Inner) error {
	if b == nil || b.receiver.Kind == "human" {
		return nil
	}
	scoped, err := topicCopy(q, in.Conv, in.PID, in.Sub, in.Body, humanJSON(in.Human))
	if err != nil {
		return err
	}
	if scoped {
		return errors.New("Topic-only sharing cannot use a selected agent or session to receive replies. Choose Yourself for replies; you can still ask the topic's agent.")
	}
	return nil
}
