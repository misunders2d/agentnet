package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Kept only by the native claim path, after bounded context selection. Received
// text, historical execution stamps and a caller's claimed PID never populate it.
type admittedAgentContext struct {
	Attempt int                 `json:"attempt"`
	Hash    string              `json:"hash"`
	Refs    []protocol.GrantRef `json:"refs"`
}

func (a *Agent) captureAgentContext(j job, c ParticipationContext) error {
	var captured admittedAgentContext
	err := a.store.db.QueryRow(`SELECT attempts,content_hash FROM inbox WHERE id=? AND state=? AND replica=0 AND conv=? AND pid=?`, j.ID, stateRunning, j.Conv, j.PID).Scan(&captured.Attempt, &captured.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // a read-only prompt preview is not an admitted run
	}
	if err != nil {
		return err
	}
	for _, m := range c.Messages {
		key := m.Key
		if m.ExcerptPID != "" {
			key = m.Claimed
		}
		if m.Sub == "" && m.TopicEvent == nil && !m.Deleted && protocol.ValidID(m.LID) && protocol.ValidFingerprint(key) {
			captured.Refs = append(captured.Refs, protocol.GrantRef{LID: m.LID, Fingerprint: key})
		}
	}
	raw, err := json.Marshal(captured)
	if err != nil {
		return err
	}
	_, err = a.store.db.Exec(`UPDATE inbox SET agent_context=? WHERE id=? AND state=? AND attempts=? AND replica=0`, string(raw), j.ID, stateRunning, captured.Attempt)
	return err
}

type agentOrganizationRun struct {
	p        ParticipationInfo
	original HistoryItem
	context  admittedAgentContext
	local    bool
}

func (a *Agent) organizationRun(q dbq, cause string, attempt int) (agentOrganizationRun, error) {
	var r agentOrganizationRun
	var request agentReq
	var raw, target string
	var replica bool
	var current int
	err := q.QueryRow(`SELECT id,sender,verified_by,kind,conv,pid,state,coalesce(target,''),local,replica,attempts,coalesce(agent_context,'') FROM inbox WHERE id=?`, cause).Scan(&request.ID, &request.Sender, &request.Key, &request.Kind, &request.Conv, &request.PID, &request.State, &target, &request.Local, &replica, &current, &raw)
	if err != nil {
		return r, err
	}
	if request.State != stateRunning || replica || attempt < 1 || current != attempt || json.Unmarshal([]byte(raw), &r.context) != nil || r.context.Attempt != attempt || !protocol.ValidHash(r.context.Hash) {
		return r, errors.New("topic organization needs the exact current admitted native run")
	}
	if err = json.Unmarshal([]byte(target), &request.Target); err != nil {
		return r, err
	}
	verdict, why, err := agentVerdict(q, request, a.Address, a.Self().Fingerprint(), true, map[string]*partView{})
	if err != nil {
		return r, err
	}
	if verdict != verdictRun {
		return r, errors.New(why)
	}
	m, err := membersIn(q, request.Conv)
	if err != nil {
		return r, err
	}
	r.p, err = participationIn(q, request.Conv, request.PID, m, a.Address)
	if err != nil {
		return r, err
	}
	if err = a.mentionReactionCurrentAgent(q, r.p.AgentID); err != nil {
		return r, err
	}
	original, err := a.followupOriginal(q, request.Conv, envelope.Ref{ID: cause, Fingerprint: request.Key})
	if err != nil {
		return r, err
	}
	r.original, r.local = original.h, request.Local
	if contentHash(r.original.inner(request.Conv)) != r.context.Hash || r.original.Human != nil && r.original.Human.AgentAuthor() {
		return r, errors.New("the immutable admitted human original differs")
	}
	if err = topicOrganizationAuthor(q, request.Conv, r.original.From, r.original.FromKey); err != nil {
		return r, err
	}
	return r, nil
}

func (a *Agent) organizationRequestProof(q dbq, r agentOrganizationRun) (*envelope.TopicRequest, error) {
	ref := &envelope.TopicRequest{LID: r.original.LID, Author: r.original.FromKey, Hash: r.context.Hash}
	if !r.local {
		return ref, nil // another author's actual original is the only proof
	}
	if r.original.From != a.Address || r.original.FromKey != a.Self().Fingerprint() || r.original.Origin != envelope.OriginUI || r.original.Target == nil {
		return nil, errors.New("only a genuine local UI human original can create its compact proof")
	}
	me, found, err := scanPersonIn(q, "state=?", personSelf)
	if err != nil || !found || !me.has(a.Address, a.Self().Fingerprint()) || !me.roster.Human(a.Self().Fingerprint()) {
		return nil, errors.New("the original author is no longer a current verified human")
	}
	proof := envelope.TopicRequestProof{V: 1, Conv: r.p.Conv, LID: r.original.LID, PID: r.p.PID, Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.roster.Hash(), Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Target: *r.original.Target, Hash: r.context.Hash, Kind: r.original.Kind, Topic: r.original.Topic}
	proof.Sign(a.id.Sign)
	if err := proof.Verify(a.Self()); err != nil {
		return nil, err
	}
	ref.Proof = &proof
	return ref, nil
}

// OrganizeAgentTopic is the admitted harness's action under its human request.
// No second approval is needed; exact native attempt and supplied references
// bound this operation. Moving presentation does not change invitation scope.
func (a *Agent) OrganizeAgentTopic(ctx context.Context, cause string, attempt int, request TopicOrganizationRequest) (ConvSent, error) {
	r, err := a.organizationRun(a.store.db, cause, attempt)
	if err != nil {
		return ConvSent{}, err
	}
	if request.Conv == "" {
		request.Conv = r.p.Conv
	}
	if request.Conv != r.p.Conv || request.Merge != "" {
		return ConvSent{}, errors.New("choose selected messages in this admitted chat")
	}
	// A stable request-local operation makes retries harmless after the CLI's
	// reply is lost. Selection order does not change the requested operation.
	request.IDs = slices.Clone(request.IDs)
	slices.Sort(request.IDs)
	identity, _ := json.Marshal(struct {
		Cause   string
		Attempt int
		Request TopicOrganizationRequest
	}{cause, attempt, request})
	hash := sha256.Sum256(identity)
	operation := hex.EncodeToString(hash[:16])
	if request.New && request.Topic == "" {
		topicHash := sha256.Sum256(append([]byte("agentnet-organized-topic-v1\n"), identity...))
		request.Topic = hex.EncodeToString(topicHash[:16])
	}
	var kept string
	if e := a.store.db.QueryRow(`SELECT coalesce(topic_event,'') FROM outbox WHERE conv=? AND lid=? LIMIT 1`, request.Conv, operation).Scan(&kept); e == nil {
		var event envelope.TopicEvent
		if json.Unmarshal([]byte(kept), &event) != nil || event.Request == nil || event.Request.LID != r.original.LID || event.Request.Hash != r.context.Hash {
			return ConvSent{}, ErrTopicOrganizationChanged
		}
		var parent string
		if e = a.store.db.QueryRow(`SELECT coalesce(reply_to,'') FROM outbox WHERE conv=? AND lid=? LIMIT 1`, request.Conv, operation).Scan(&parent); e != nil {
			return ConvSent{}, e
		}
		sent, _, e := a.keptTopicOrganization(TopicOrganizationReview{TopicOrganizationRequest: request, Operation: operation, Parent: parent}, &event)
		return sent, e
	} else if !errors.Is(e, sql.ErrNoRows) {
		return ConvSent{}, e
	}
	review, err := a.previewTopicOrganization(request, func(q dbq) error { _, e := a.organizationRun(q, cause, attempt); return e }, r.p.PID)
	if err != nil {
		return ConvSent{}, err
	}
	for _, move := range review.Moves {
		ref := protocol.GrantRef{LID: move.LID, Fingerprint: move.Author}
		if !slices.Contains(r.context.Refs, ref) && ref != (protocol.GrantRef{LID: r.original.LID, Fingerprint: r.original.FromKey}) {
			return ConvSent{}, errors.New("selected message was not supplied to this native run")
		}
	}
	proof, err := a.organizationRequestProof(a.store.db, r)
	if err != nil {
		return ConvSent{}, err
	}
	event := &envelope.TopicEvent{Action: "move", Moves: review.Moves, Request: proof}
	claim := func(tx *sql.Tx, _ string) error {
		if _, err := a.organizationRun(tx, cause, attempt); err != nil {
			return err
		}
		stamp, err := a.topicOrganizationStamp(tx, request)
		if err != nil || stamp != review.Token {
			return ErrTopicOrganizationChanged
		}
		if request.New && request.Title != "" {
			return a.recordTopicTitle(tx, request.Conv, request.Topic, request.Title)
		}
		return nil
	}
	notice, err := a.TopicOrganizationNotice(request.Conv, request.Topic, request.Title)
	if err != nil {
		return ConvSent{}, err
	}
	sent, err := a.SendConv(WithQueuedSend(ctx, operation), request.Conv, ConvOutgoing{Body: notice, Topic: request.Topic, ReplyTo: review.Parent, TopicEvent: event, PID: r.p.PID, AgentID: r.p.AgentID, Origin: envelope.OriginAgentOrganization, Emotion: "neutral", claim: claim, agentOrganizationCause: cause, agentOrganizationAttempt: attempt})
	if err == nil && request.New && request.Title != "" {
		a.topicTitlesChanged()
	}
	return sent, err
}

func (a *Agent) topicOrganizationAdmission(q dbq, in envelope.Inner, address, fp string, historical bool) (string, error) {
	if !envelope.AgentTopicOrganization(in) {
		return reasonInvalid, topicOrganizationAuthor(q, in.Conv, address, fp)
	}
	if err := envelope.CheckTopic(in); err != nil {
		return reasonInvalid, err
	}
	m, err := membersIn(q, in.Conv)
	if err != nil {
		return reasonProof, err
	}
	p, err := participationIn(q, in.Conv, in.PID, m, a.Address)
	if err != nil {
		return reasonProof, err
	}
	if p.Role != "" || p.Host.Address != address || p.Host.Fingerprint != fp || p.AgentID != in.AgentID || p.Invite == "" || p.Decision == "" || p.State == PartConflict || !historical && !p.Claimable() {
		return reasonInvalid, errors.New("organization differs from its current admitted agent")
	}
	ref := in.TopicEvent.Request
	if err := ref.Validate(in); err != nil {
		return reasonInvalid, err
	}
	original, originalErr := a.followupOriginal(q, in.Conv, envelope.Ref{ID: ref.LID, Fingerprint: ref.Author})
	var requestTopic, requester string
	if ref.Proof != nil {
		proof := ref.Proof
		if proof.Author.Address != address || proof.Author.Fingerprint != fp || proof.Target.Address != p.Host.Address || proof.Target.Fingerprint != p.Host.Fingerprint || proof.Target.AgentID != p.AgentID {
			return reasonInvalid, errors.New("a host cannot attest a foreign human request")
		}
		var authorKey identity.Public
		found := false
		for _, person := range m.persons {
			if person.info.Person == proof.Author.Person && m.chains[person.info.Person][proof.Author.Roster] {
				authorKey, found = person.device(address)
			}
		}
		if !found || authorKey.Fingerprint() != fp {
			return reasonInvalid, errors.New("compact proof author lacks its exact current member key")
		}
		if err := proof.Verify(authorKey); err != nil {
			return reasonInvalid, err
		}
		requester, requestTopic = address, proof.Topic
		if !historical && !m.requestEpoch(address, ref.Author, &proof.Target) {
			return reasonInvalid, errors.New("the original human group admission changed")
		}
		if originalErr == nil && (contentHash(original.h.inner(in.Conv)) != ref.Hash || original.h.Kind != proof.Kind || original.h.Topic != proof.Topic) {
			return reasonInvalid, errors.New("compact proof differs from the actual original")
		}
	} else {
		if originalErr != nil {
			return reasonProof, errors.New("organization waits for its exact signed human original")
		}
		h := original.h
		if contentHash(h.inner(in.Conv)) != ref.Hash || h.PID != p.PID || h.Target == nil || h.Target.Address != p.Host.Address || h.Target.Fingerprint != p.Host.Fingerprint || h.Target.AgentID != p.AgentID || h.Human != nil && h.Human.AgentAuthor() {
			return reasonInvalid, errors.New("organization request differs from the immutable human original")
		}
		requester, requestTopic = h.From, h.Topic
		if !historical && !m.requestEpoch(h.From, h.FromKey, h.Target) {
			return reasonInvalid, errors.New("the original human group admission changed")
		}
	}
	if err := topicOrganizationAuthor(q, in.Conv, requester, ref.Author); err != nil {
		return reasonInvalid, err
	}
	if p.Topic != nil && requestTopic != *p.Topic {
		return reasonInvalid, errParticipationTopic
	}
	for _, move := range in.TopicEvent.Moves {
		if err := agentOrganizationRefScope(q, in.Conv, protocol.GrantRef{LID: move.LID, Fingerprint: move.Author}, p, a.Self().Fingerprint(), ref); err != nil {
			if errors.Is(err, errOrganizationGrantProof) {
				return reasonProof, err
			}
			if errors.Is(err, sql.ErrNoRows) {
				return reasonProof, errors.New("organization waits for the exact selected original")
			}
			return reasonInvalid, err
		}
	}
	return "", nil
}

func agentOrganizationRefScope(q dbq, conv string, ref protocol.GrantRef, p ParticipationInfo, selfFP string, request *envelope.TopicRequest) error {
	var topic, human string
	err := q.QueryRow(`SELECT coalesce(topic,''),coalesce(human,'') FROM inbox WHERE conv=? AND lid=? AND coalesce(verified_by,claimed_fp,'')=? AND coalesce(sub,'')='' AND topic_event IS NULL AND ref_id IS NULL
 UNION ALL SELECT coalesce(topic,''),coalesce(human,'') FROM outbox WHERE conv=? AND lid=? AND ?=? AND coalesce(sub,'')='' AND topic_event IS NULL AND ref_id IS NULL LIMIT 1`, conv, ref.LID, ref.Fingerprint, conv, ref.LID, ref.Fingerprint, selfFP).Scan(&topic, &human)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h, _, snapshotErr := grantedAgentSnapshot(q, p, ref)
			if snapshotErr != nil {
				return err
			}
			topic, human = h.Topic, humanJSON(h.Human)
		} else {
			return err
		}
	}
	if p.Topic != nil && topic != *p.Topic {
		return errParticipationTopic
	}
	if ref.LID == request.LID && ref.Fingerprint == request.Author || slices.Contains(p.Grant, ref) {
		return nil
	}
	var captured envelope.HumanTurn
	if p.Member && json.Unmarshal([]byte(human), &captured) == nil && slices.ContainsFunc(captured.Audience, func(s envelope.HumanScope) bool {
		return s.PID == p.PID && s.Invite == p.Invite && s.Decision == p.Decision
	}) {
		return nil
	}
	if p.Member && p.Scope != "" {
		var held bool
		if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM participation_events WHERE conv=? AND pid=? AND hash=? AND type=?)`, conv, p.PID, p.Invite, protocol.EventInvite).Scan(&held); err != nil {
			return err
		}
		if !held {
			return errOrganizationGrantProof
		}
	}
	return errors.New("selected original was not admitted to this agent's scope")
}

func (a *Agent) agentOrganizationDeliveryGate(env envelope.Envelope) (bool, error) {
	var in envelope.Inner
	var event string
	err := a.store.db.QueryRow(`SELECT conv,lid,pid,coalesce(agent_id,''),topic,topic_event FROM outbox WHERE id=? AND origin=? AND coalesce(sub,'')=''`, env.ID, envelope.OriginAgentOrganization).Scan(&in.Conv, &in.LID, &in.PID, &in.AgentID, &in.Topic, &event)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	in.V, in.Kind, in.Origin = envelope.Version2, envelope.KindMessage, envelope.OriginAgentOrganization
	if err = json.Unmarshal([]byte(event), &in.TopicEvent); err != nil {
		return false, err
	}
	if _, err = a.topicOrganizationAdmission(a.store.db, in, a.Address, a.Self().Fingerprint(), false); err != nil {
		return false, a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: "+err.Error(), "")
	}
	return true, nil
}

// A public scope intentionally omits private grants. Missing signer evidence
// waits for proof; the organizer host cannot supply the inviter's signature.
var errOrganizationGrantProof = errors.New("organization waits for its inviter's signed selected grant")

// Existing valid retained moves tell the original inviter which subset needs
// disclosure, including moves stored before upgrade. Only that exact current
// human signer can publish an ordinary signed share; no private invite is sent.
// This is a negative selector, never authority. Counted share rows suppress
// already disclosed refs; only a missing exact ref reaches full verification.
// Conversation indexes bound the move scan once per authored conversation,
// rather than once per participation, and no body is decoded in Go on no-op wakes.
const organizationGrantCandidatesSQL = `WITH invitations AS MATERIALIZED (
 SELECT conv,pid,hash,event FROM participation_events WHERE type='invite' AND author=?
 AND json_extract(event,'$.author.address')=? AND json_type(event,'$.group')='object'
 AND json_array_length(json_extract(event,'$.grant'))>0
), moves AS MATERIALIZED (
 SELECT m.conv,m.pid,m.topic_event FROM (SELECT DISTINCT conv FROM invitations) c
 JOIN inbox m ON m.conv=c.conv WHERE m.origin='agent:organization' AND m.topic_event IS NOT NULL
 UNION
 SELECT m.conv,m.pid,m.topic_event FROM (SELECT DISTINCT conv FROM invitations) c
 JOIN outbox m ON m.conv=c.conv WHERE m.origin='agent:organization' AND m.topic_event IS NOT NULL
)
SELECT DISTINCT i.conv,i.pid FROM moves m JOIN invitations i ON i.conv=m.conv AND i.pid=m.pid
JOIN json_each(m.topic_event,'$.moves') move JOIN json_each(i.event,'$.grant') grant
 ON json_extract(move.value,'$.lid')=json_extract(grant.value,'$.lid')
 AND json_extract(move.value,'$.author')=json_extract(grant.value,'$.fingerprint')
WHERE NOT EXISTS (
 SELECT 1 FROM participation_events s JOIN room_membership_events counted ON counted.hash=s.hash
 JOIN json_each(s.event,'$.grant') disclosed
 WHERE s.conv=i.conv AND s.pid=i.pid AND s.type='share' AND s.prev=i.hash
 AND json_extract(disclosed.value,'$.lid')=json_extract(grant.value,'$.lid')
 AND json_extract(disclosed.value,'$.fingerprint')=json_extract(grant.value,'$.fingerprint')
)`

func (a *Agent) discloseOrganizationGrants(ctx context.Context) int {
	rows, err := a.store.db.Query(organizationGrantCandidatesSQL, a.Self().Fingerprint(), a.Address)
	if err != nil {
		return 0
	}
	type subject struct{ conv, pid string }
	var subjects []subject
	for rows.Next() {
		var s subject
		if rows.Scan(&s.conv, &s.pid) == nil {
			subjects = append(subjects, s)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0
	}
	for _, s := range subjects {
		if err := a.discloseOrganizationGrant(ctx, s.conv, s.pid); err != nil {
			a.Logf("organization grant disclosure held: %v", err)
		}
	}
	return len(subjects) // exactly the number of full per-participation checks
}
func (a *Agent) discloseOrganizationGrant(ctx context.Context, conv, pid string) error {
	p, err := a.Participation(pid)
	if err != nil {
		return err
	}
	if p.Conv != conv || !p.Member || !p.Claimable() {
		return nil
	}
	events, err := a.store.participationEvents(conv, pid)
	if err != nil {
		return err
	}
	var invite protocol.ParticipationEvent
	for _, ev := range events {
		if ev.Hash() == p.Invite && ev.Type == protocol.EventInvite && ev.Author.Address == a.Address && ev.Author.Fingerprint == a.Self().Fingerprint() {
			invite = ev
		}
	}
	if invite.Host == nil || len(invite.Grant) == 0 {
		return nil
	}
	m, err := membersIn(a.store.db, conv)
	if err != nil {
		return err
	}
	if m.group == nil || !m.authorEpoch(invite.Author) {
		return nil
	}
	if err = topicOrganizationAuthor(a.store.db, conv, a.Address, a.Self().Fingerprint()); err != nil {
		return err
	}
	rows, err := a.store.db.Query(`SELECT topic_event FROM inbox WHERE conv=? AND pid=? AND origin=? AND topic_event IS NOT NULL UNION SELECT topic_event FROM outbox WHERE conv=? AND pid=? AND origin=? AND topic_event IS NOT NULL`, conv, pid, envelope.OriginAgentOrganization, conv, pid, envelope.OriginAgentOrganization)
	if err != nil {
		return err
	}
	var refs []protocol.GrantRef
	for rows.Next() {
		var raw string
		var event envelope.TopicEvent
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &event) != nil {
			continue
		}
		for _, move := range event.Moves {
			ref := protocol.GrantRef{LID: move.LID, Fingerprint: move.Author}
			if !slices.Contains(invite.Grant, ref) || slices.Contains(refs, ref) {
				continue
			}
			disclosed := slices.ContainsFunc(events, func(ev protocol.ParticipationEvent) bool {
				return ev.Type == protocol.EventShare && slices.Contains(p.Shares, ev.Hash()) && slices.Contains(ev.Grant, ref)
			})
			if !disclosed {
				refs = append(refs, ref)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(refs) == 0 {
		return err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	share := protocol.ParticipationEvent{V: 1, Conv: conv, PID: pid, Type: protocol.EventShare, Prev: p.Invite, TS: time.Now().Unix(), Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Host: invite.Host, Grant: refs, Audience: invite.Audience, Topic: invite.Topic}
	for _, ref := range refs {
		if snapshot, ok := grantSnapshot(ref, invite.Snapshots); ok {
			share.Snapshots = append(share.Snapshots, snapshot)
		}
	}
	if err = m.bindGroupInvite(&share); err != nil {
		return err
	}
	before, err := a.topicOrganizationStamp(a.store.db, snapshotRequest(conv, invite.Topic, refs))
	if err != nil {
		return err
	}
	for _, snapshot := range share.Snapshots {
		ref := protocol.GrantRef{LID: snapshot.LID, Fingerprint: snapshot.Fingerprint}
		item, err := a.snapshotSourceItem(a.store.db, conv, ref)
		if err != nil {
			return err
		}
		controls, err := a.controlsOf(ControlRef{Conv: conv, ID: ref.LID, Fingerprint: ref.Fingerprint})
		if err != nil {
			return err
		}
		item.Body = controls.Shown(item.Body)
		if controls.Deleted || contentHash(item.inner(conv)) != snapshot.Hash {
			return errors.New("organization grant snapshot changed")
		}
	}
	return a.recordAndSendWith(ctx, share, func(tx *sql.Tx) error {
		if err := topicOrganizationAuthor(tx, conv, a.Address, a.Self().Fingerprint()); err != nil {
			return err
		}
		now, err := membersIn(tx, conv)
		if err != nil {
			return err
		}
		valid, err := now.verifyInviteEpoch(tx, share)
		if err != nil {
			return err
		}
		if !valid {
			return errors.New("organization grant current admission changed")
		}
		current, err := participationIn(tx, conv, pid, now, a.Address)
		if err != nil {
			return err
		}
		if !current.Claimable() || current.Invite != p.Invite || current.Decision != p.Decision {
			return errors.New("organization grant participation changed")
		}
		if !now.authorEpoch(invite.Author) || !now.authorEpoch(share.Author) {
			return errors.New("organization grant inviter admission changed")
		}
		stamp, err := a.topicOrganizationStamp(tx, snapshotRequest(conv, invite.Topic, refs))
		if err != nil {
			return err
		}
		if stamp != before {
			return ErrTopicOrganizationChanged
		}

		return nil
	})
}

// One upgrade scan moves only verified group organization messages whose sole
// missing proof is the selected inviter grant into the existing proof queue.
// No message is admitted here, no execution is restarted, no live replay occurs.
func (a *Agent) recoverOrganizationGrantProof() error {
	const marker = "organization-selected-grant-proof-v1"
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM config WHERE k=?)`, marker).Scan(&done); err != nil || done {
		return err
	}
	if err = historyRecoveryCurrent(tx, a.Self()); err != nil {
		if errors.Is(err, errHistoryRecoveryAuthority) {
			return nil
		}
		return err
	}
	deferred := false
	for after := ""; ; {
		rows, err := tx.Query(`SELECT id,envelope FROM quarantine WHERE reason=? AND id>? ORDER BY id LIMIT ?`, reasonInvalid, after, proofPage)
		if err != nil {
			return err
		}
		type held struct{ id, raw string }
		var page []held
		for rows.Next() {
			var h held
			if err = rows.Scan(&h.id, &h.raw); err != nil {
				rows.Close()
				return err
			}
			page = append(page, h)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].id
		for _, h := range page {
			var env envelope.Envelope
			if json.Unmarshal([]byte(h.raw), &env) != nil || env.ID != h.id {
				continue
			}
			sender, found, err := pinnedKey(tx, env.From)
			if err != nil {
				return err
			}
			if !found {
				deferred = true
				continue
			}
			in, err := envelope.Open(env, a.id, a.Address, sender)
			if err != nil {
				continue
			}
			original, author, fp, historical := in, sender.Address, sender.Fingerprint(), false
			if in.Sub == envelope.SubHistory && in.Replica {
				if historyRecoveryCurrent(tx, sender, a.Self()) != nil {
					deferred = true
					continue
				}
				var item HistoryItem
				if decodeGroupCarrierJSON([]byte(in.Body), &item) != nil {
					continue
				}
				original, author, fp, historical = item.inner(in.Conv), item.From, item.FromKey, true
			}
			if !envelope.AgentTopicOrganization(original) {
				continue
			}
			packet, err := groupTurnPacketIn(tx, in.Conv)
			if err != nil || groupTurnCheck(tx, packet, a.Address, a.Self().Fingerprint()) != nil || groupTurnCheck(tx, packet, sender.Address, sender.Fingerprint()) != nil {
				deferred = true
				continue
			}
			reason, admissionErr := a.topicOrganizationAdmission(tx, original, author, fp, historical)
			if admissionErr != nil {
				deferred = deferred || reason == reasonProof
				continue // retained invalid copies wait until the exact grant actually verifies
			}
			if _, err = tx.Exec(`UPDATE quarantine SET reason=? WHERE id=? AND reason=?`, reasonProof, h.id, reasonInvalid); err != nil {
				return err
			}
		}
	}
	if deferred {
		return a.store.done(tx.Commit())
	}
	if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?,'1')`, marker); err != nil {
		return err
	}
	return a.store.done(tx.Commit())
}
