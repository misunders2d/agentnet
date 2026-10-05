package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Conversation history across one person's devices (owner decision
// 2026-09-29: the same chats, with their history, on every device).
//
// A device of the person forwards what it holds to another device of the
// same person as history: an envelope (sub "history", a replica) from that
// device, carrying one HistoryItem: a message as it was sent or received,
// with the key it was sent under (claimed: the forwarding device vouches for
// it; the original signature covered ciphertext for another device, so it
// cannot travel). Roots travel with every envelope and keep their creator's
// signature; participation events keep their author's.
//
// Two paths, both through the outbox (durable, receipted, resumed after a
// restart):
//
//   - the snapshot: after this device links a new device of its person,
//     it walks its conversation rows in order, a page per sync (saved
//     position, same transaction as the page queued), until it reaches the
//     end;
//   - forwarding: a device that admits a message sent to an older roster of
//     its person (fan) forwards it to the devices that roster lacks, in the
//     same transaction as it admits it. Every such device forwards; copies
//     beyond the first are duplicates, stored once.
//
// A history copy never runs, answers, alerts or reminds; it is shown as
// synced from the device that forwarded it, and gives way to a copy of the
// same message received directly (convstore.go: addConvInbox).

// HistoryItem is one message (or participation event, as the message that
// carried it) forwarded as history.
type HistoryItem struct {
	V              int                     `json:"v"`
	From           string                  `json:"from"`
	FromKey        string                  `json:"from_key"` // the key it was sent under (claimed)
	ID             string                  `json:"id"`
	LID            string                  `json:"lid"`
	TS             int64                   `json:"ts"`
	Kind           string                  `json:"kind"`
	Body           string                  `json:"body"`
	ReplyTo        string                  `json:"reply_to,omitempty"`
	Status         string                  `json:"status,omitempty"`
	Sub            string                  `json:"sub,omitempty"`
	Origin         string                  `json:"origin,omitempty"`
	Emotion        string                  `json:"emotion,omitempty"`
	Target         *envelope.Target        `json:"target,omitempty"`
	PID            string                  `json:"pid,omitempty"`
	Attachments    []envelope.Attachment   `json:"attachments,omitempty"` // manifests (name, size, sha256); no blob for the new device
	At             int64                   `json:"at"`                    // when the forwarding device got or sent it (unix ms): its place in the conversation
	Ref            *envelope.Ref           `json:"ref,omitempty"`         // a control's target (controls.go)
	AgentID        string                  `json:"agent_id,omitempty"`
	GroupAdmission string                  `json:"group_admission,omitempty"` // only direct live admission, vouched for by an own linked device
	ReceiverRoute  *envelope.ReceiverRoute `json:"receiver_route,omitempty"`  // provenance only; history never installs a delegation
	Human          *envelope.HumanTurn     `json:"human,omitempty"`           // unchanged captured audience; never recomputed
	Quote          string                  `json:"quote,omitempty"`
	TopicDone      bool                    `json:"topic_done,omitempty"`
	Topic          string                  `json:"topic,omitempty"`
	TopicEvent     *envelope.TopicEvent    `json:"topic_event,omitempty"`
}

// inner is the item as the message it records.
func (h HistoryItem) inner(conv string) envelope.Inner {
	in := envelope.Inner{V: envelope.Version2, ID: h.ID, From: h.From, TS: h.TS, Kind: h.Kind, Body: h.Body, ReplyTo: h.ReplyTo, Quote: h.Quote, Topic: h.Topic, TopicEvent: h.TopicEvent, TopicDone: h.TopicDone,
		Status: h.Status, Sub: h.Sub, Origin: h.Origin, Emotion: h.Emotion, Target: h.Target, PID: h.PID, Conv: conv, LID: h.LID, Replica: true, AgentID: h.AgentID, ReceiverRoute: h.ReceiverRoute, Human: h.Human}
	if envelope.IsControl(h.Sub) { // a control travels as history with its target reference
		in.V, in.Ref = envelope.Version3, h.Ref
	}
	for _, a := range h.Attachments {
		in.Attachments = append(in.Attachments, envelope.Attachment{Name: a.Name, Size: a.Size, SHA256: a.SHA256})
	}
	return in
}

func itemOf(in envelope.Inner, key string, at int64) HistoryItem {
	h := HistoryItem{V: 1, From: in.From, FromKey: key, ID: in.ID, LID: in.LID, TS: in.TS, Kind: in.Kind, Body: in.Body, ReplyTo: in.ReplyTo, Quote: in.Quote, Topic: in.Topic, TopicEvent: in.TopicEvent, TopicDone: in.TopicDone,
		Status: in.Status, Sub: in.Sub, Origin: in.Origin, Emotion: in.Emotion, Target: in.Target, PID: in.PID, At: at, Ref: in.Ref, AgentID: in.AgentID, ReceiverRoute: in.ReceiverRoute, Human: in.Human}
	for _, a := range in.Attachments {
		h.Attachments = append(h.Attachments, envelope.Attachment{Name: a.Name, Size: a.Size, SHA256: a.SHA256})
	}
	return h
}

// historyCopy seals item as history for the device to, in conversation
// conv (root raw).
func (a *Agent) historyCopy(to identity.Public, conv string, raw []byte, item HistoryItem, proofs ...*groupControlIngressProof) (outCopy, error) {
	root, err := protocol.ParseConvRoot(raw)
	if err != nil {
		return outCopy{}, err
	}
	if externalDM(root) {
		me, ok, err := a.store.selfPerson(a.Address)
		if err != nil {
			return outCopy{}, err
		}
		if _, member := root.Member(me.info.Person); !ok || !member || !me.has(to.Address, to.Fingerprint()) {
			return outCopy{}, errors.New("DM history goes only to a current linked device of a human room member")
		}
	}
	if root.Kind == protocol.ConvKindGroup {
		packet, e := groupTurnPacketIn(a.store.db, conv)
		if e != nil {
			return outCopy{}, e
		}
		if e = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); e != nil {
			return outCopy{}, e
		}
		if e = groupTurnCheck(a.store.db, packet, to.Address, to.Fingerprint()); e != nil {
			return outCopy{}, e
		}
		me, ok, e := a.store.selfPerson(a.Address)
		if e != nil || !ok || !me.has(to.Address, to.Fingerprint()) {
			return outCopy{}, errors.New("group history goes only to a current own linked device")
		}
		admission, e := groupMemberAdmission(a.store.db, packet, a.Address, a.Self().Fingerprint())
		if e != nil {
			return outCopy{}, e
		}
		if groupParticipationHistoryItem(item) {
			stamp, e := a.groupParticipationSourceAdmission(a.store.db, packet, item)
			if e != nil {
				return outCopy{}, e
			}
			item.GroupAdmission = stamp
		}
		if groupControlSub(item.Sub) {
			var proof *groupControlIngressProof
			if len(proofs) > 0 {
				proof = proofs[0]
			}
			stamp, e := a.groupControlSourceAdmission(a.store.db, packet, item, proof)
			if e != nil {
				return outCopy{}, e
			}
			item.GroupAdmission = stamp
		}
		if item.GroupAdmission == "" {
			e = a.store.db.QueryRow(`SELECT coalesce(group_admission,'') FROM inbox WHERE conv=? AND lid=? AND coalesce(verified_by,claimed_fp)=? UNION ALL SELECT coalesce(group_admission,'') FROM outbox WHERE conv=? AND lid=? AND sub IS NULL LIMIT 1`, conv, item.LID, item.FromKey, conv, item.LID).Scan(&item.GroupAdmission)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return outCopy{}, e
			}
		}
		if item.GroupAdmission != admission.Hash() {
			if e = groupHistorySelected(a.store.db, packet, to.Address, to.Fingerprint(), historyRef(conv, item)); e != nil {
				return outCopy{}, errors.New("group: own history lacks current live admission or selected grant")
			}
			item.GroupAdmission = ""
		}
	}
	recipient, err := to.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	body, _ := json.Marshal(item)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: to.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: string(body), Conv: conv, LID: protocol.NewID(), Root: raw, Replica: true, Sub: envelope.SubHistory}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return outCopy{}, err
	}
	copy := outCopy{env: env, in: in, state: stateQueued}
	if root.Kind == protocol.ConvKindGroup {
		copy.required = protocol.CapGroup
		copy.recipientFP = to.Fingerprint()
		packet, e := groupTurnPacketIn(a.store.db, conv)
		if e != nil {
			return outCopy{}, e
		}
		admission, e := groupMemberAdmission(a.store.db, packet, to.Address, to.Fingerprint())
		if e != nil {
			return outCopy{}, e
		}
		copy.groupAdmission = admission.Hash()
	}
	if item.PID != "" {
		p, err := a.participation(conv, item.PID)
		if err != nil && !errors.Is(err, ErrNoParticipation) {
			return outCopy{}, err
		}
		if err == nil && p.External && root.Kind != protocol.ConvKindGroup {
			copy.required = protocol.CapExternalParticipation
		}
	}
	return copy, nil
}

// insertCopies stores history copies in the outbox, within tx.
func insertCopies(tx *sql.Tx, copies []outCopy) error {
	now := time.Now()
	for _, c := range copies {
		data, _ := json.Marshal(c.env)
		body := ""
		if c.required == protocol.CapGroup {
			body = c.in.Body
		} else if c.in.Sub == envelope.SubHistory {
			var item HistoryItem
			if err := json.Unmarshal([]byte(c.in.Body), &item); err != nil {
				return err
			}
			room, err := roomCopy(tx, c.in.Conv, c.in.Sub, c.in.Body, "")
			if err != nil {
				return err
			}
			if _, assistant := historyAssistant(c.in.Body, c.in.Conv); item.ReceiverRoute != nil || assistant || room { // delivery reads the item's own requirement
				body = c.in.Body
			}
		}
		if _, err := tx.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at, conv, lid, kind, created_ms, sub, required_cap,recipient_fp,group_admission)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, nullif(?, ''),nullif(?,''),nullif(?,''))`,
			c.env.ID, c.env.To, body, string(data), c.state, now.Unix(), c.in.Conv, c.in.LID, c.in.Kind, now.UnixMilli(), c.in.Sub, copyRequirement(c), c.recipientFP, c.groupAdmission); err != nil {
			return err
		}
	}
	return nil
}

// forwardStale returns history copies of in (admitted from a device of
// person p, verified under key) for the current devices of this
// installation's person that the roster the sender sent to (fan) lacks:
// copies the sender could not know to send.
func (a *Agent) forwardStale(me personRow, in envelope.Inner, key string, raw []byte, stamps ...string) []outCopy {
	return a.forwardStaleWithControlProof(me, in, key, raw, nil, stamps...)
}

func (a *Agent) forwardStaleWithControlProof(me personRow, in envelope.Inner, key string, raw []byte, proof *groupControlIngressProof, stamps ...string) []outCopy {
	if root, err := protocol.ParseConvRoot(raw); err != nil {
		return nil
	} else if _, member := root.Member(me.info.Person); externalDM(root) && !member {
		return nil
	}
	var sentTo string
	for _, f := range in.Fan {
		if f.Person == me.info.Person {
			sentTo = f.Roster
		}
	}
	if sentTo == "" || sentTo == me.info.Roster {
		return nil
	}
	old, ok, err := a.store.chainStep(me.info.Person, sentTo)
	if err != nil || !ok {
		return nil // a roster not in this person's chain: nothing to go by
	}
	item := itemOf(in, key, time.Now().UnixMilli())
	if len(stamps) > 0 {
		item.GroupAdmission = stamps[0]
	}
	var copies []outCopy
	for _, d := range me.roster.Devices {
		if d.Address == a.Address || old.Has(d.Address, d.Fingerprint()) || d.Address == in.From {
			continue
		}
		c, err := a.historyCopy(d, in.Conv, raw, item, proof)
		if err != nil {
			a.Logf("forwarding %s to %s: %v", in.ID, d.Address, err)
			continue
		}
		copies = append(copies, c)
	}
	return copies
}

// admitHistory admits a history envelope from sender, a current device of
// this installation's own person (checked by the caller with the root):
// its item is stored as history if its claimed sender device is (or was)
// a device of a member of the conversation, as that member's pinned chain
// lists it.
func (a *Agent) admitHistory(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, hold func(string, string) error, fromQuarantine bool) error {
	var item HistoryItem
	if err := decodeStrict([]byte(in.Body), &item); err != nil || item.V != 1 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) {
		return hold(reasonInvalid, "a malformed history item")
	}
	owner := ""
	external := false
	var key identity.Public
	for pass := 0; pass < 2 && owner == ""; pass++ {
		for _, m := range root.Members {
			if pass == 1 {
				if _, err := a.refreshPerson(ctx, m.Person, false); err != nil && !errors.Is(err, errPersonConflict) {
					return err
				}
			}
			if k, ok := a.store.deviceKey(m.Person, item.From, item.FromKey); ok {
				owner, key = m.Person, k
				break
			}
		}
	}
	if item.Human != nil {
		if err := a.verifyHumanProof(ctx, root, item.Human); err != nil {
			return hold(reasonProof, err.Error())
		}
		if owner == "" {
			for _, e := range item.Human.Proof {
				if e.Type == protocol.EventScope && e.PID == item.Human.AuthorPID && e.Host.Address == item.From && e.Host.Fingerprint == item.FromKey {
					if k, ok := a.store.deviceKey(e.Host.Person, item.From, item.FromKey); ok {
						owner, key = e.Host.Person, k
					}
				}
			}
		}
	}
	// A linked human member may retain its invited agent's outputs and signed
	// acceptance/decline. The original host's pinned roster proof supplies
	// authorship only, never
	// room membership; an outside host's linked devices still fail the
	// caller's recipient membership check.
	output := item.AgentID != "" && item.Sub == "" && (item.Kind == envelope.KindAnswer || item.Kind == envelope.KindResult || item.Kind == envelope.KindMessage && item.Status == envelope.StatusProgress)
	decision := false
	if item.Sub == envelope.SubEvent {
		if ev, err := protocol.ParseParticipationEvent([]byte(item.Body)); err == nil {
			decision = ev.Type == protocol.EventAccept || ev.Type == protocol.EventDecline
		}
	}
	assistantReaction := envelope.AssistantReaction(item.inner(in.Conv))
	if owner == "" && item.PID != "" && (output || decision || assistantReaction) {
		p, err := a.participation(in.Conv, item.PID)
		if errors.Is(err, ErrNoParticipation) {
			return hold(reasonProof, err.Error())
		}
		if err != nil {
			return err
		}
		if p.Invite == "" || p.State == PartConflict {
			return hold(reasonProof, "external output history has no unambiguous invitation")
		}
		if p.External && (!output && !assistantReaction || item.AgentID == p.AgentID) && item.From == p.Host.Address && item.FromKey == p.Host.Fingerprint {
			if k, ok := a.store.deviceKey(p.Host.Person, item.From, item.FromKey); ok {
				owner, key, external = p.Host.Person, k, true
			}
		}
	}
	if owner == "" {
		return hold(reasonInvalid, "the history item's sender is no device of a member of this conversation")
	}
	if p, ok, err := a.store.personByID(owner); err != nil {
		return err
	} else if ok && p.info.State == personConflict {
		return hold(reasonConflict, errPersonConflict.Error())
	}
	orig := item.inner(in.Conv)
	if err := envelope.CheckTopic(orig); err != nil {
		return err
	}
	if err := envelope.CheckQuote(orig); err != nil {
		return hold(reasonInvalid, err.Error())
	}
	if err := receiverHistoryRoute(a.store.db, orig, item.FromKey); err != nil {
		return hold(reasonInvalid, err.Error())
	}
	if reason, err := a.checkConversationAgent(orig, key, true); err != nil {
		if reason != "" {
			return hold(reason, err.Error())
		}
		return err
	}
	if orig.Sub == envelope.SubDriveSpace { // applied under the original author's person, as a direct one is
		if err := a.admitDriveHistory(owner, orig); err != nil {
			return hold(reasonProof, err.Error())
		}
	}
	if envelope.IsControl(orig.Sub) {
		// A control carried as history is applied under the same rules as
		// one received directly (controls.go): its claimed author is the
		// forwarding device's word, as the turn's is, but who may edit or
		// delete what is checked here, before anything is stored or removed.
		if orig.Ref == nil {
			return hold(reasonInvalid, "a control in history names no message")
		}
		m, err := a.dmMembers(in.Conv)
		if err != nil {
			return err
		}
		if reason, why := a.controlAuthorized(m, orig, owner); reason != "" {
			return hold(reason, why)
		}
		if orig.Sub == envelope.SubStatus { // only the device the request is for speaks for it, as directly
			if ok, why := a.statusAllowed(orig, item.From, item.FromKey); !ok {
				here, err := a.convRowHere(in.Conv, orig.Ref)
				if err != nil {
					return err
				}
				if !here { // the request may still come
					return hold(reasonProof, why)
				}
				return hold(reasonInvalid, why)
			}
		}
		if envelope.AssistantReaction(orig) { // the assistant's, bound as its direct copy is
			if reason, err := assistantHistoryCheck(a.store.db, orig, key.Address, key.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
				if reason == "" {
					return err
				}
				return hold(reason, err.Error())
			}
		}
	}
	var also func(*sql.Tx) error
	if orig.Sub == envelope.SubEvent { // the signed event itself, verified under its author's key
		ev, err := checkParticipationEvent(orig, key.Fingerprint(), key.SignKey)
		if err != nil {
			return hold(reasonInvalid, err.Error())
		}
		if ev.Type == protocol.EventInvite && ev.Host != nil && externalDM(root) {
			if _, member := root.Member(ev.Host.Person); !member {
				if reason, err := a.externalHostProof(ctx, ev.Host); err != nil {
					if reason != "" {
						return hold(reason, err.Error())
					}
					if errors.Is(err, errPersonConflict) {
						return hold(reasonConflict, err.Error())
					}
					if errors.Is(err, ErrNoPerson) || errors.Is(err, errPersonRecord) || errors.Is(err, errRootInvalid) {
						return hold(reasonProof, err.Error())
					}
					return err
				}
			}
		}
		if decision {
			m, err := a.dmMembers(in.Conv)
			if err != nil {
				return err
			}
			p, err := participationIn(a.store.db, in.Conv, item.PID, m, a.Address)
			if err != nil {
				return err
			}
			if p.External {
				if err := externalTurn(orig, p, m, key.Address, key.Fingerprint()); err != nil {
					return hold(reasonInvalid, err.Error())
				}
			}
		}
		raw := []byte(orig.Body)
		also = func(tx *sql.Tx) error { return insertParticipationEvent(tx, ev, raw) }
	}
	if external {
		event := also
		also = func(tx *sql.Tx) error {
			m, err := membersIn(tx, in.Conv)
			if err != nil {
				return err
			}
			p, err := participationIn(tx, in.Conv, item.PID, m, a.Address)
			if err != nil {
				return err
			}
			if !p.External || p.Invite == "" || p.State == PartConflict || p.Host.Address != key.Address || p.Host.Fingerprint != key.Fingerprint() {
				return errors.New("external history host proof changed before admission")
			}
			if assistantReaction {
				if _, err := assistantHistoryCheck(tx, orig, key.Address, key.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
					return err
				}
			} else if output {
				if orig.AgentID != p.AgentID {
					return errors.New("external history agent changed before admission")
				}
				if _, err := externalOutputRequest(tx, orig, p, m, a.Address, a.Self().Fingerprint()); err != nil {
					return err
				}
			} else if err := externalTurn(orig, p, m, key.Address, key.Fingerprint()); err != nil {
				return err
			}
			if event != nil {
				return event(tx)
			}
			return nil
		}
	}
	if item.Human != nil {
		prior := also
		also = func(tx *sql.Tx) error {
			if err := insertHumanProof(tx, item.Human); err != nil {
				return err
			}
			if err := humanTurnAuthorization(tx, orig, item.From, item.FromKey, a.Address, a.Self().Fingerprint(), true); err != nil {
				return err
			}
			if prior != nil {
				return prior(tx)
			}
			return nil
		}
	}
	res, err := a.store.addHistoryInbox(orig, item.At, item.FromKey, env.From, env.ID, fromQuarantine, also)
	if errors.Is(err, errTooManyEvents) {
		return hold(reasonInvalid, err.Error())
	}
	if err == nil && res == admitted && orig.Sub == envelope.SubEvent {
		a.convWork.due(convRetry)
		a.kickNow()
		a.wakeWorker() // a participation may resolve differently; nothing here runs from history
	}
	if err == nil && res == admitted {
		a.applyRetraction(orig) // a deletion reaching this device as history removes the text and cache here too
	}
	return err
}

// History snapshot jobs (history_jobs): one per new device of this person
// that this device linked.

// historyPos is where a snapshot continues: after the row (conv, ms, id).
type historyPos struct {
	Conv string `json:"conv"`
	Ms   int64  `json:"ms"`
	ID   string `json:"id"`
}

const historyPage = 50

// startHistory queues a snapshot of this device's conversations for dev.
func (a *Agent) startHistory(dev identity.Public) error {
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM conversations`).Scan(&n); err != nil {
		return err
	}
	now := time.Now().Unix()
	pos, _ := json.Marshal(historyPos{})
	if _, err := a.store.db.Exec(`INSERT OR IGNORE INTO history_jobs(device, fingerprint, pos, convs_total, state, created_at, updated_at) VALUES(?, ?, ?, ?, 'running', ?, ?)`,
		dev.Address, dev.Fingerprint(), string(pos), n, now, now); err != nil {
		return err
	}
	a.replayErased(dev) // conversations deleted here stay deleted there (convclear.go)
	a.convWork.due(convHistory)
	a.kickNow()          // this process's daemon, if it is one (the page approved)
	notifyDaemon(a.home) // or the daemon running beside this command (agentnet person approve)
	return nil
}

// kickNow asks the current stream's worker to sync now; safe from any
// goroutine (a page's request, the stream reader).
func (a *Agent) kickNow() {
	a.kickMu.Lock()
	k := a.kick
	a.kickMu.Unlock()
	if k != nil {
		k()
	}
}

// historyStep queues one page of every running snapshot and reports
// whether any has more.
func (a *Agent) historyStep(ctx context.Context) (more bool) {
	rows, err := a.store.db.Query(`SELECT device, fingerprint, pos, state FROM history_jobs WHERE state IN ('running', 'done')`)
	if err != nil {
		return false
	}
	type job struct {
		device, fp, state string
		pos               historyPos
	}
	var jobs []job
	for rows.Next() {
		var j job
		var pos string
		if rows.Scan(&j.device, &j.fp, &pos, &j.state) == nil && json.Unmarshal([]byte(pos), &j.pos) == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return false
	}
	for _, j := range jobs {
		dev, ok := me.device(j.device)
		if !ok || dev.Fingerprint() != j.fp {
			a.store.db.Exec(`UPDATE history_jobs SET state = 'ended', updated_at = ? WHERE device = ?`, time.Now().Unix(), j.device)
			continue // no longer a device of this person
		}
		m, err := a.historyPage(ctx, dev, j.pos, j.state == "done")
		if err != nil {
			a.Logf("history for %s: %v", j.device, err)
			a.convWork.due(convHistory) // retry on the next existing wake/push, without a timer or immediate error loop
			continue
		}
		more = more || m
	}
	return more
}

// Existing outbox rows are the durable batch marker: every deterministic
// proof page and current context must have an exact signed usable carrier.
// Terminal failure/expiration does not suppress the existing job's recovery.
type groupHistoryBatch struct {
	packet   GroupContext
	payloads []groupDeliveryPayload
	copies   []outCopy
}

func (a *Agent) groupHistoryBatchPresent(q dbq, dev identity.Public, packet GroupContext, payloads []groupDeliveryPayload) (bool, error) {
	rows, err := q.Query(`SELECT sub,body,envelope FROM outbox WHERE conv=? AND recipient=? AND recipient_fp=? AND required_cap=? AND coalesce(pid,'')='' AND sub IN ('group-proof','group-context') AND state IN ('queued','waiting','custody','delivered')`, packet.State.Conv, dev.Address, dev.Fingerprint(), protocol.CapGroup)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	seen := make([]bool, len(payloads))
	for rows.Next() {
		var sub, body, raw string
		if err := rows.Scan(&sub, &body, &raw); err != nil {
			return false, err
		}
		var descriptor protocol.GroupCarrier
		var env envelope.Envelope
		if decodeStrict([]byte(body), &descriptor) != nil || descriptor.Validate() != nil || descriptor.ToKey != dev.Fingerprint() || json.Unmarshal([]byte(raw), &env) != nil || env.V != envelope.Version2 || env.From != a.Address || env.To != dev.Address || env.Kind != envelope.KindMessage || len(env.Blobs) != 1 || env.VerifySig(a.Self().SignKey) != nil {
			continue
		}
		for i, p := range payloads {
			if sub == p.sub && descriptor.Seq == p.descriptor.Seq && descriptor.Hash == p.descriptor.Hash {
				seen[i] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, ok := range seen {
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func (a *Agent) checkGroupHistoryBatch(q dbq, dev identity.Public, batch groupHistoryBatch) error {
	own, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok || !own.has(a.Address, a.Self().Fingerprint()) || !own.has(dev.Address, dev.Fingerprint()) {
		return ErrGroupContextPending
	}
	current, err := groupTurnPacketIn(q, batch.packet.State.Conv)
	if err != nil {
		return err
	}
	old, _ := json.Marshal(batch.packet)
	raw, _ := json.Marshal(current)
	if !bytes.Equal(old, raw) {
		return ErrGroupContextPending
	}
	for _, address := range []identity.Public{a.Self(), dev} {
		if err := groupTurnCheck(q, current, address.Address, address.Fingerprint()); err != nil {
			return err
		}
	}
	payloads, err := groupDeliveryPayloads(q, current)
	if err != nil {
		return err
	}
	if len(payloads) != len(batch.payloads) {
		return ErrGroupContextPending
	}
	for i, p := range payloads {
		if p.sub != batch.payloads[i].sub || p.descriptor != batch.payloads[i].descriptor || !bytes.Equal(p.raw, batch.payloads[i].raw) {
			return ErrGroupContextPending
		}
	}
	return nil
}

func (a *Agent) prepareGroupHistoryCarriers(ctx context.Context, dev identity.Public) (batches []groupHistoryBatch, err error) {
	defer func() {
		if err != nil {
			for _, b := range batches {
				a.releaseGroupCopies(b.copies)
			}
		}
	}()
	own, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return nil, err
	}
	if !ok || !own.has(a.Address, a.Self().Fingerprint()) || !own.has(dev.Address, dev.Fingerprint()) {
		return nil, ErrGroupContextPending
	}
	if dev.Address == a.Address {
		return nil, nil
	}
	rows, err := a.store.db.Query(`SELECT conv FROM group_context ORDER BY conv`)
	if err != nil {
		return nil, err
	}
	var convs []string
	for rows.Next() {
		var conv string
		if err = rows.Scan(&conv); err != nil {
			break
		}
		convs = append(convs, conv)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	for _, conv := range convs {
		packet, e := groupTurnPacketIn(a.store.db, conv)
		if e != nil {
			return batches, e
		}
		member, present := packet.State.Member(own.info.Person)
		if !present || packet.State.Withdrawn(member, packet.Withdrawals) {
			continue
		} // visitor/removed person has no own-history authority
		var withdrawn int
		if e = a.store.db.QueryRow(`SELECT count(*) FROM (SELECT admission FROM group_withdrawals WHERE conv=? AND person=? AND admission=? UNION ALL SELECT admission FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?)`, conv, member.Person, member.Admission.Hash(), conv, member.Person, member.Admission.Hash()).Scan(&withdrawn); e != nil {
			return batches, e
		}
		if withdrawn != 0 {
			continue
		}
		batch := groupHistoryBatch{packet: packet}
		batch.payloads, e = groupDeliveryPayloads(a.store.db, packet)
		if e != nil {
			return batches, e
		}
		if e = a.checkGroupHistoryBatch(a.store.db, dev, batch); e != nil {
			return batches, e
		}
		complete, e := a.groupHistoryBatchPresent(a.store.db, dev, packet, batch.payloads)
		if e != nil {
			return batches, e
		}
		if complete {
			batches = append(batches, batch)
			continue
		}
		batch.copies, e = a.groupDeliveryTo(ctx, packet, batch.payloads, dev)
		if e != nil {
			return batches, e
		}
		batches = append(batches, batch)
	}
	return batches, nil
}

// historyPageFor queues the next page of history for dev after pos, and
// saves where it ended in the same transaction.
func (a *Agent) historyPageFor(dev identity.Public, pos historyPos) (more bool, err error) {
	return a.historyPage(context.Background(), dev, pos, false)
}

func (a *Agent) historyPage(ctx context.Context, dev identity.Public, pos historyPos, contextOnly bool) (more bool, err error) {
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return false, err
	}
	defer release()
	batches, err := a.prepareGroupHistoryCarriers(ctx, dev)
	if err != nil {
		return false, err
	}
	var prepared []outCopy
	for _, batch := range batches {
		prepared = append(prepared, batch.copies...)
	}
	defer func() { a.releaseGroupCopies(prepared) }()
	if contextOnly && len(prepared) == 0 {
		return false, nil
	} // completed jobs with complete batches do not write/wake again

	self := a.Self().Fingerprint()
	// Local request execution stamps are not signed author identities and
	// must not be exported as the original question/task's AgentID.
	var items []struct {
		conv string
		dir  string
		in   envelope.Inner
		key  string
		pos  historyPos
	}
	if !contextOnly {
		rows, err := a.store.db.Query(`
		SELECT conv, ms, id, dir, sender, coalesce(verified_by, claimed_fp, ''), ts, kind, body, coalesce(reply_to, ''), coalesce(status, ''), coalesce(sub, ''),
		       coalesce(origin, ''), coalesce(emotion, ''), coalesce(target, ''), coalesce(pid, ''), lid, coalesce(ref_id, ''), coalesce(ref_fp, ''), coalesce(agent_id, ''),coalesce(quote,''),coalesce(topic,''),coalesce(topic_event,''),topic_done FROM (
		  SELECT conv, received_ms AS ms, id, 'in' AS dir, sender, verified_by, claimed_fp, ts, kind, body, reply_to, status, sub, origin, emotion, target, pid, lid, ref_id, ref_fp,
		         CASE WHEN kind IN ('question', 'task') THEN '' ELSE agent_id END AS agent_id,quote,topic,topic_event,topic_done
		    FROM inbox WHERE conv IS NOT NULL AND local = 0 AND coalesce(sub, '') NOT IN ('history', 'clear', 'group-proof', 'group-context', 'group-invite', 'group-consent', 'group-withdrawal')
		     AND NOT `+erasedInFor("inbox")+`
		  UNION ALL
		  SELECT o.conv, o.created_ms, o.id, 'out', ?, ?, NULL, CASE WHEN coalesce(o.topic,'') <> '' OR (coalesce(o.pid,'') <> '' OR o.sub='status') AND o.conv IN (SELECT conv FROM group_context) THEN json_extract(o.envelope,'$.ts') ELSE o.created_at END, o.kind, o.body, o.reply_to, o.status, o.sub, o.origin, o.emotion, o.target, o.pid, o.lid, o.ref_id, o.ref_fp, o.agent_id,o.quote,o.topic,o.topic_event,o.topic_done
		    FROM outbox o WHERE o.conv IS NOT NULL AND coalesce(o.sub, '') NOT IN ('history', 'file', 'clear', 'group-proof', 'group-context', 'group-invite', 'group-consent', 'group-withdrawal')
		     AND o.rowid = (SELECT min(rowid) FROM outbox f WHERE f.conv = o.conv AND f.lid = o.lid) AND NOT `+erasedOut+`)
		WHERE (conv, ms, id) > (?, ?, ?) AND conv IN (SELECT id FROM conversations UNION SELECT conv FROM group_context)
		ORDER BY conv, ms, id LIMIT ?`, a.Address, self, self, pos.Conv, pos.Ms, pos.ID, historyPage)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var conv, dir, key, target, refID, refFP string
			var ms int64
			var in envelope.Inner
			var topicEvent string
			if err := rows.Scan(&conv, &ms, &in.ID, &dir, &in.From, &key, &in.TS, &in.Kind, &in.Body, &in.ReplyTo, &in.Status, &in.Sub,
				&in.Origin, &in.Emotion, &target, &in.PID, &in.LID, &refID, &refFP, &in.AgentID, &in.Quote, &in.Topic, &topicEvent, &in.TopicDone); err != nil {
				rows.Close()
				return false, err
			}
			if topicEvent != "" {
				json.Unmarshal([]byte(topicEvent), &in.TopicEvent)
			}
			if target != "" {
				in.Target = &envelope.Target{}
				json.Unmarshal([]byte(target), in.Target)
			}
			if refID != "" {
				in.Ref = &envelope.Ref{ID: refID, Fingerprint: refFP}
			}
			in.Conv = conv
			items = append(items, struct {
				conv string
				dir  string
				in   envelope.Inner
				key  string
				pos  historyPos
			}{conv, dir, in, key, historyPos{conv, ms, in.ID}})
		}
		rows.Close()
	}
	var copies []outCopy
	roots := map[string][]byte{}
	for _, it := range items {
		files, err := a.store.attachmentManifest(it.in.ID, it.dir)
		if err != nil {
			return false, err
		}
		it.in.Attachments = files
		raw, ok := roots[it.conv]
		if !ok {
			_, raw, _, err = a.store.conversation(it.conv)
			if err != nil {
				return false, err
			}
			roots[it.conv] = raw
		}
		if root, err := protocol.ParseConvRoot(raw); err != nil {
			return false, err
		} else if externalDM(root) {
			me, ok, err := a.store.selfPerson(a.Address)
			if err != nil {
				return false, err
			}
			if _, member := root.Member(me.info.Person); !ok || !member {
				continue // advance the snapshot cursor without copying visitor context
			}
		}
		it.in.ReceiverRoute, err = receiverStoredRoute(a.store.db, it.dir, it.in.ID)
		if err != nil {
			return false, err
		}
		it.in.Human, err = storedHuman(a.store.db, it.dir, it.in.ID)
		if err != nil {
			return false, err
		}
		prepared := itemOf(it.in, it.key, it.pos.Ms)
		if root, e := protocol.ParseConvRoot(raw); e == nil && root.Kind == protocol.ConvKindGroup {
			packet, e := groupTurnPacketIn(a.store.db, it.conv)
			if e != nil {
				return false, e
			}
			me, ok, e := a.store.selfPerson(a.Address)
			if e != nil {
				return false, e
			}
			member, present := packet.State.Member(me.info.Person)
			var withdrawn int
			if present {
				if e = a.store.db.QueryRow(`SELECT count(*) FROM group_withdrawals WHERE conv=? AND person=? AND admission=?`, it.conv, member.Person, member.Admission.Hash()).Scan(&withdrawn); e != nil {
					return false, e
				}
			}
			if !ok || !present || withdrawn != 0 || packet.State.Withdrawn(member, packet.Withdrawals) {
				continue
			}
			if e = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); e != nil {
				return false, e
			}
			if e = groupTurnCheck(a.store.db, packet, dev.Address, dev.Fingerprint()); e != nil {
				return false, e
			}
			// Visitors and previous admissions cannot become an own-history source.
			if !me.has(dev.Address, dev.Fingerprint()) {
				continue
			}
			if ordinaryGroupTurn(prepared.inner(it.conv)) {
				if retractedRef(a.store.db, it.conv, prepared.LID, prepared.FromKey) {
					continue
				}
				sources, e := a.groupHistorySources(a.store.db, it.conv, prepared.LID, prepared.FromKey, 0, 64)
				if e != nil {
					return false, e
				}
				found := false
				for _, source := range sources {
					if source.item.ID == prepared.ID {
						// Existing resolver checks every logical duplicate against the
						// visible content hash; never choose a conflicting first row.
						verified, e := a.groupHistorySourceIn(a.store.db, it.conv, historyRef(it.conv, source.item))
						if e != nil {
							return false, e
						}
						prepared = source.item
						prepared.GroupAdmission = verified.stamp
						found = true
						break
					}
				}
				if !found {
					return false, ErrGroupHistoryUnavailable
				}
				if prepared.GroupAdmission != member.Admission.Hash() && !member.Admission.AllowsHistory(historyRef(it.conv, prepared)) {
					continue // a known earlier admission is not a new own-live grant
				}
			} else if !groupControlSub(prepared.Sub) && !groupParticipationHistoryItem(prepared) {
				continue // separately admitted PID traffic has its own history policy
			}
		}
		c, err := a.historyCopy(dev, it.conv, raw, prepared)
		if err != nil {
			if errors.Is(err, errGroupControlHistoryEpoch) || errors.Is(err, errGroupParticipationHistoryEpoch) {
				continue
			}
			return false, err
		}
		copies = append(copies, c)
	}
	next, state := pos, "running"
	if len(items) > 0 {
		next = items[len(items)-1].pos
	}
	if contextOnly || len(items) < historyPage {
		state = "done"
	}
	data, _ := json.Marshal(next)
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var carriers []outCopy
	for _, batch := range batches {
		if e := a.checkGroupHistoryBatch(tx, dev, batch); e != nil {
			return false, e
		}
		complete, e := a.groupHistoryBatchPresent(tx, dev, batch.packet, batch.payloads)
		if e != nil {
			return false, e
		}
		if !complete {
			if len(batch.copies) == 0 {
				return false, ErrGroupContextPending
			} // usable marker changed after preparation; retry fresh
			carriers = append(carriers, batch.copies...)
		}
	}
	for _, copy := range copies {
		root, e := protocol.ParseConvRoot(copy.in.Root)
		if e != nil {
			return false, e
		}
		if root.Kind != protocol.ConvKindGroup {
			continue
		}
		packet, e := groupTurnPacketIn(tx, copy.in.Conv)
		if e != nil {
			return false, e
		}
		var item HistoryItem
		if e = decodeStrict([]byte(copy.in.Body), &item); e != nil {
			return false, e
		}
		own, ok, e := scanPersonIn(tx, "state = ?", personSelf)
		if e != nil {
			return false, e
		}
		if !ok || !own.has(copy.env.To, copy.recipientFP) {
			return false, ErrGroupContextPending
		}
		admission, e := groupMemberAdmission(tx, packet, copy.env.To, copy.recipientFP)
		if e != nil {
			return false, e
		}
		if admission.Hash() != copy.groupAdmission {
			return false, ErrGroupContextPending
		}
		if groupControlSub(item.Sub) {
			stamp, e := a.groupControlSourceAdmission(tx, packet, item, nil)
			if e != nil {
				return false, e
			}
			if stamp != item.GroupAdmission {
				return false, ErrGroupContextPending
			}
			if e = a.groupControlHistoryCheck(tx, root, a.Self(), item); e != nil {
				return false, e
			}
		} else if groupParticipationHistoryItem(item) {
			if e = a.groupParticipationHistoryOutboundCheck(tx, packet, copy.env.To, copy.recipientFP, item); e != nil {
				return false, e
			}
		} else if e = a.groupHistoryOutboundCheck(tx, packet, copy.env.To, copy.recipientFP, item); e != nil {
			return false, e
		}
	}
	if err := insertCopies(tx, append(carriers, copies...)); err != nil {
		return false, err
	}
	// Carriers reuse the ordinary durable upload ledger; legacy history has no blobs.
	for _, c := range carriers {
		for _, att := range c.in.Attachments {
			if _, err := tx.Exec(`INSERT INTO sent_attachments(message_id,blob_id,name,size,sha256) VALUES(?,?,?,?,?)`, c.env.ID, att.Blob.ID, att.Name, att.Size, att.SHA256); err != nil {
				return false, err
			}
		}
		for _, blob := range c.env.Blobs {
			if _, err := tx.Exec(`INSERT INTO uploads(blob_id,message_id,state) VALUES(?,?,?)`, blob.ID, c.env.ID, protocol.BlobUploading); err != nil {
				return false, err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE history_jobs SET pos = ?, state = ?, updated_at = ? WHERE device = ? AND fingerprint = ?`, string(data), state, time.Now().Unix(), dev.Address, dev.Fingerprint()); err != nil {
		return false, err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return false, err
	}
	// Only newly committed spools survive; duplicate preparations release below.
	committed := map[string]bool{}
	for _, c := range carriers {
		committed[c.in.ID] = true
	}
	kept := prepared[:0]
	for _, c := range prepared {
		if !committed[c.in.ID] {
			kept = append(kept, c)
		}
	}
	prepared = kept
	if len(copies)+len(carriers) > 0 {
		notifyDaemon(a.home)
	}
	return state == "running", nil
}

// attachmentManifest preserves the original attachment indices in one direction.
func (s *store) attachmentManifest(id, dir string) ([]envelope.Attachment, error) {
	table := "attachments"
	if dir == "out" {
		table = "sent_attachments"
	}
	rows, err := s.db.Query("SELECT name, size, sha256 FROM "+table+" WHERE message_id = ? ORDER BY rowid", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []envelope.Attachment
	for rows.Next() {
		var a envelope.Attachment
		if err := rows.Scan(&a.Name, &a.Size, &a.SHA256); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// HistoryJob is the progress of sending this device's conversations to a
// new device of its person.
type HistoryJob struct {
	Device     string `json:"device"`
	Name       string `json:"name"`
	ConvsDone  int    `json:"convs_done"`
	ConvsTotal int    `json:"convs_total"`
	State      string `json:"state"` // running (queued as this device's connection allows), done, ended (the device left)
}

// HistoryProgress lists the history snapshots this device sends or sent.
// Queued pages still go out through the outbox: "done" means all is
// queued; delivery follows as the Hub takes it (this device must stay
// connected until then).
func (a *Agent) HistoryProgress() ([]HistoryJob, error) {
	rows, err := a.store.db.Query(`SELECT device, pos, convs_total, state FROM history_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryJob
	var at []string // each job's position, counted once the rows are closed
	for rows.Next() {
		var j HistoryJob
		var pos string
		if err := rows.Scan(&j.Device, &pos, &j.ConvsTotal, &j.State); err != nil {
			return nil, err
		}
		_, j.Name, _ = protocol.SplitAddress(j.Device)
		var p historyPos
		json.Unmarshal([]byte(pos), &p)
		out, at = append(out, j), append(at, p.Conv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // the store has one connection: a query while these rows are open waits for it forever
	for i := range out {
		if out[i].State == "done" {
			out[i].ConvsDone = out[i].ConvsTotal
		} else if at[i] != "" {
			a.store.db.QueryRow(`SELECT count(*) FROM conversations WHERE id < ?`, at[i]).Scan(&out[i].ConvsDone)
		}
	}
	return out, nil
}
