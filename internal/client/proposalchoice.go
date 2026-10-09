package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Choice records are local, transactional deduplication, not received grants.
// A host records a choice only after matching its original question/proposal.
const proposalChoiceSchema = `
CREATE TABLE proposal_choices(conv TEXT NOT NULL,ref TEXT NOT NULL,proposal TEXT NOT NULL,task TEXT NOT NULL UNIQUE,PRIMARY KEY(conv,ref));
CREATE TABLE proposal_sends(conv TEXT NOT NULL,ref TEXT NOT NULL,author TEXT NOT NULL,task TEXT NOT NULL UNIQUE,body TEXT NOT NULL,needs_new INTEGER NOT NULL,PRIMARY KEY(conv,ref,author));
`

type proposalSendIDKey struct{}

func storedProposalChoice(q dbq, in envelope.Inner, key string) (string, error) {
	var proposal string
	err := q.QueryRow(`SELECT proposal FROM proposal_choices WHERE task=?`, in.ID).Scan(&proposal)
	if err == nil {
		return proposal, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return proposalFor(q, in, key)
}

// An imported direct row remains inert. Only its authenticated original
// endpoints may provide the context for a fresh, explicitly confirmed task.
func (a *Agent) sharedProposalQuestion(q dbq, p confirmableProposal) (target, pid, topic string, err error) {
	if p.conv == "" {
		answer, e := a.deviceHistorySource(q, "in", p.id)
		if e != nil {
			return "", "", "", ErrNotConfirmable
		}
		question, e := a.deviceHistorySource(q, "in", p.replyTo)
		if e != nil {
			return "", "", "", ErrNotConfirmable
		}
		h := question.item
		if h.Kind != envelope.KindQuestion || h.Sub != "" || h.From != answer.to || h.FromKey != answer.toKey || question.to != p.from || question.toKey != p.key {
			return "", "", "", ErrNotConfirmable
		}
		if same, e := proposalHuman(q, a.Address, a.Self().Fingerprint(), h.From, h.FromKey, true); e != nil || !same {
			return "", "", "", ErrNotConfirmable
		}
		return targetJSON(h.Target), "", "", nil
	}
	var from, key string
	err = q.QueryRow(`SELECT sender,coalesce(verified_by,claimed_fp,''),coalesce(target,''),coalesce(pid,''),coalesce(topic,'') FROM inbox WHERE conv=? AND (id=? OR lid=?) AND kind='question' AND ref_id IS NULL AND coalesce(sub,'')='' LIMIT 1`, p.conv, p.replyTo, p.replyTo).Scan(&from, &key, &target, &pid, &topic)
	if err != nil {
		return "", "", "", ErrNotConfirmable
	}
	if same, e := proposalHuman(q, a.Address, a.Self().Fingerprint(), from, key, true); e != nil || !same {
		return "", "", "", ErrNotConfirmable
	}
	return target, pid, topic, nil
}

// This identity marks an explicit confirmation, without granting authority.
// Its source, sender, executor and context must independently match below.
func proposalTaskID(key, conv, ref, host string) string {
	b, _ := json.Marshal([]string{"confirm-proposal-v2", key, conv, ref, host})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

func proposalTaskRef(in envelope.Inner) string {
	if in.Conv != "" {
		return in.LID
	}
	return in.ID
}

// proposalHuman checks a current pinned human and a historical human endpoint
// of that same verified person. Historical evidence never restores live rights.
func proposalHuman(q dbq, from, key, original, originalKey string, local ...bool) (bool, error) {
	p, found, err := scanPersonIn(q, `person IN (SELECT person FROM person_devices WHERE address=?)`, from)
	if err != nil || !found || p.info.State == personConflict || !p.has(from, key) || !p.roster.Human(key) {
		return false, err
	}
	var pending bool
	if err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM peers WHERE address=? AND pending IS NOT NULL)`, from).Scan(&pending); err != nil || pending {
		return false, err
	}
	pub, found, err := pinnedKey(q, from)
	if err != nil || (len(local) == 0 || !local[0]) && (!found || pub.Fingerprint() != key) {
		return false, err
	}
	if p.has(original, originalKey) && p.roster.Human(originalKey) {
		return true, nil
	}
	return deviceHistoryHuman(q, p.info.Person, original, originalKey)
}

// proposalChoiceFor recognizes only the legacy exact confirmation or a new
// explicit confirmation ID. A normal task replying to a proposal stays normal.
// Revised text has provenance and deduplication, never the exact-text exception.
func proposalChoiceFor(q dbq, in envelope.Inner, key string, local ...bool) (string, error) {
	legacy, err := proposalFor(q, in, key)
	if err != nil {
		return legacy, err
	}
	if legacy != "" || in.Kind == envelope.KindTask && proposalTaskRef(in) == proposalTaskID(key, in.Conv, in.ReplyTo, in.To) {
		var known bool
		if err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM person_chain c,json_each(c.record,'$.devices') d WHERE json_extract(d.value,'$.address')=? UNION ALL SELECT 1 FROM person_devices WHERE address=?)`, in.From, in.From).Scan(&known); err != nil {
			return "", err
		}
		if known {
			if current, e := proposalHuman(q, in.From, key, in.From, key, local...); e != nil || !current {
				return "", e
			}
		}
	}
	if legacy != "" {
		return legacy, nil
	}
	if in.Kind != envelope.KindTask || in.Replica || in.ReplyTo == "" || key == "" || in.Sub != "" || in.Ref != nil || len(in.Attachments) != 0 || proposalTaskRef(in) != proposalTaskID(key, in.Conv, in.ReplyTo, in.To) {
		return "", nil
	}
	var id, asker, askerKey, recipient string
	err = q.QueryRow(`SELECT o.id,ask.sender,coalesce(ask.verified_by,''),o.recipient FROM outbox o JOIN inbox ask ON (ask.id=o.reply_to OR ask.lid=o.reply_to) AND coalesce(ask.conv,'')=coalesce(o.conv,'')
 WHERE coalesce(o.conv,'')=? AND CASE WHEN o.conv IS NULL THEN o.id ELSE o.lid END=?
 AND o.ref_id IS NULL AND coalesce(o.kind,json_extract(o.envelope,'$.kind'))='answer' AND o.status='proposal'
 AND ask.kind='question' AND ask.replica=0 AND coalesce(ask.conv,'')=?
 AND coalesce(o.pid,'')=? AND coalesce(ask.pid,'')=? AND coalesce(ask.topic,'')=?
 AND coalesce(ask.target,'')=? AND (o.recipient=ask.sender OR ask.local=1)
 ORDER BY o.rowid LIMIT 1`, in.Conv, in.ReplyTo, in.Conv, in.PID, in.PID, in.Topic, targetJSON(in.Target)).Scan(&id, &asker, &askerKey, &recipient)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if asker == in.From && askerKey == key {
		return id, nil
	}
	if same, err := proposalHuman(q, in.From, key, asker, askerKey, local...); err != nil || !same {
		return "", err
	}
	return id, nil
}

func claimProposalChoice(tx *sql.Tx, in envelope.Inner, key string, local ...bool) (string, string, error) {
	proposal, err := proposalChoiceFor(tx, in, key, local...)
	if err != nil || proposal == "" {
		return proposal, "", err
	}
	// Preserve already accepted legacy confirmations when upgrading this host.
	var original, originalKey string
	if err = tx.QueryRow(`SELECT ask.sender,coalesce(ask.verified_by,'') FROM outbox o JOIN inbox ask ON (ask.id=o.reply_to OR ask.lid=o.reply_to) AND coalesce(ask.conv,'')=coalesce(o.conv,'') WHERE o.id=?`, proposal).Scan(&original, &originalKey); err != nil {
		return "", "", err
	}
	first, err := proposalConfirmedBy(tx, proposal, original, originalKey, in.ID)
	if err != nil {
		return "", "", err
	}
	if first == "" {
		first = in.ID
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO proposal_choices(conv,ref,proposal,task) VALUES(?,?,?,?)`, in.Conv, in.ReplyTo, proposal, first); err != nil {
		return "", "", err
	}
	if err = tx.QueryRow(`SELECT task FROM proposal_choices WHERE conv=? AND ref=?`, in.Conv, in.ReplyTo).Scan(&first); err != nil {
		return "", "", err
	}
	if first == in.ID {
		return proposal, "", nil
	}
	return proposal, first, nil
}

func exactChosenProposal(q dbq, in envelope.Inner, key string) (bool, error) {
	proposal, err := storedProposalChoice(q, in, key)
	if err != nil || proposal == "" {
		return false, err
	}
	var exact bool
	err = q.QueryRow(`SELECT body=? FROM outbox WHERE id=?`, in.Body, proposal).Scan(&exact)
	return exact, err
}

func proposalCopyNeedsCapability(q querier, id string) (bool, error) {
	var needed bool
	err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox o JOIN proposal_sends s ON o.id=s.task OR o.lid=s.task WHERE o.id=? AND s.needs_new=1 AND (o.conv IS NULL OR json_extract(o.target,'$.address')=o.recipient))`, id).Scan(&needed)
	return needed, err
}

// Local outgoing provenance is presentation of our persisted explicit choice.
// It never supplies worker permission or reconstructs an executable inbox row.
func (a *Agent) sentProposalView(id string) (*ProposalView, error) {
	var conv, ref, author, body string
	err := a.store.db.QueryRow(`SELECT s.conv,s.ref,s.author,s.body FROM proposal_sends s JOIN outbox o ON o.id=s.task OR o.lid=s.task WHERE o.id=? LIMIT 1`, id).Scan(&conv, &ref, &author, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return a.syncedProposalView(id)
	}
	if err != nil {
		return nil, err
	}
	v := &ProposalView{Task: body, ConfirmedBy: a.Address}
	err = a.store.db.QueryRow(`SELECT id,body,coalesce(reply_to,'') FROM inbox WHERE coalesce(conv,'')=? AND CASE WHEN conv IS NULL THEN id ELSE lid END=? AND coalesce(verified_by,claimed_fp,'')=?
 UNION ALL SELECT id,body,coalesce(reply_to,'') FROM outbox WHERE coalesce(conv,'')=? AND CASE WHEN conv IS NULL THEN id ELSE lid END=? AND ?=? LIMIT 1`, conv, ref, author, conv, ref, author, a.Self().Fingerprint()).Scan(&v.ProposalID, &v.Proposal, &v.QuestionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = a.store.db.QueryRow(`SELECT body,sender FROM inbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND kind='question' UNION ALL SELECT body,? FROM outbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND coalesce(kind,json_extract(envelope,'$.kind'))='question' LIMIT 1`, conv, v.QuestionID, v.QuestionID, a.Address, conv, v.QuestionID, v.QuestionID).Scan(&v.Question, &v.Asker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.Edited = v.Task != v.Proposal
	return v, nil
}

// syncedProposalChoice observes a fresh explicit choice copied by this person's
// history. The original recipient and deterministic marker must match; ordinary
// tasks replying to the proposal are not choices. Imported rows remain inert.
func (a *Agent) syncedProposalChoice(q dbq, p confirmableProposal, onlyID ...string) (string, error) {
	ref := p.id
	if p.conv != "" {
		ref = p.lid
	}
	rows, err := q.Query(`SELECT id,sender,coalesce(verified_by,claimed_fp,''),coalesce(lid,'') FROM inbox WHERE coalesce(conv,'')=? AND kind='task' AND reply_to=? AND coalesce(pid,'')=? AND coalesce(topic,'')=? AND coalesce(target,'')=? AND ref_id IS NULL AND coalesce(sub,'')='' ORDER BY received_at,id`, p.conv, ref, p.pid, p.topic, targetJSON(p.target))
	if err != nil {
		return "", err
	}
	type candidate struct{ id, from, key, lid string }
	var candidates []candidate
	for rows.Next() {
		var r candidate
		if err = rows.Scan(&r.id, &r.from, &r.key, &r.lid); err != nil {
			rows.Close()
			return "", err
		}
		candidates = append(candidates, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	own, found, err := scanPersonIn(q, `state=?`, personSelf)
	if err != nil || !found {
		return "", err
	}
	for _, r := range candidates {
		if len(onlyID) > 0 && r.id != onlyID[0] {
			continue
		}
		logical := r.id
		if p.conv != "" {
			logical = r.lid
		}
		if logical != proposalTaskID(r.key, p.conv, ref, p.from) {
			continue
		}
		human, e := deviceHistoryHuman(q, own.info.Person, r.from, r.key)
		if e != nil {
			return "", e
		}
		if !human {
			continue
		}
		if p.conv == "" {
			source, e := a.deviceHistorySource(q, "in", r.id)
			if e != nil {
				return "", e
			}
			if source.to != p.from || source.toKey != p.key {
				continue
			}
		}
		return r.id, nil
	}
	return "", nil
}

func (a *Agent) syncedProposalView(id string) (*ProposalView, error) {
	var p confirmableProposal
	var body, from, target, ref string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,''),coalesce(reply_to,''),body,sender FROM inbox WHERE id=? AND kind='task' AND ref_id IS NULL AND coalesce(sub,'')=''`, id).Scan(&p.conv, &ref, &body, &from)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = a.store.db.QueryRow(`SELECT id,sender,coalesce(verified_by,claimed_fp,''),body,coalesce(reply_to,''),coalesce(lid,''),coalesce(pid,''),coalesce(topic,'') FROM inbox WHERE coalesce(conv,'')=? AND CASE WHEN conv IS NULL THEN id ELSE lid END=? AND kind='answer' AND status='proposal' AND ref_id IS NULL AND coalesce(sub,'')='' LIMIT 1`, p.conv, ref).Scan(&p.id, &p.from, &p.key, &p.body, &p.replyTo, &p.lid, &p.pid, &p.topic)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := &ProposalView{ProposalID: p.id, Proposal: p.body, Task: body, ConfirmedBy: from, Edited: body != p.body}
	err = a.store.db.QueryRow(`SELECT id,body,sender,coalesce(target,'') FROM inbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND kind='question' UNION ALL SELECT id,body,?,coalesce(target,'') FROM outbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND coalesce(kind,json_extract(envelope,'$.kind'))='question' LIMIT 1`, p.conv, p.replyTo, p.replyTo, a.Address, p.conv, p.replyTo, p.replyTo).Scan(&v.QuestionID, &v.Question, &v.Asker, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if target != "" {
		p.target = &envelope.Target{}
		if err = json.Unmarshal([]byte(target), p.target); err != nil {
			return nil, err
		}
	}
	chosen, err := a.syncedProposalChoice(a.store.db, p, id)
	if err != nil || chosen == "" {
		return nil, err
	}
	return v, nil
}

// Old askers locate their question by its physical copy ID. Use the shared
// logical reference only when the original asker can read the own3 contract.
// Capability controls representation only; the request's authority is unchanged.
func (a *Agent) proposalReplyRef(ctx context.Context, conv, ref string) (string, error) {
	var from, key string
	var local bool
	err := a.store.db.QueryRow(`SELECT sender,coalesce(verified_by,''),local FROM inbox WHERE id=? AND conv=? AND kind='question' AND replica=0`, ref, conv).Scan(&from, &key, &local)
	if err != nil {
		return "", err
	}
	if !local {
		pub, found, e := pinnedKey(a.store.db, from)
		if e != nil {
			return "", e
		}
		var pending bool
		if e = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM peers WHERE address=? AND pending IS NOT NULL)`, from).Scan(&pending); e != nil {
			return "", e
		}
		if !found || pending || pub.Fingerprint() != key || a.requireParticipationCaps(ctx, pub, protocol.CapOwnSyncV3) != nil {
			return ref, nil
		}
	}
	return groupReplyLID(a.store.db, conv, ref)
}
