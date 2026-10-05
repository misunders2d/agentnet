package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Proposals (MEL-521): a question's run that needs an action it may not
// take answers with the exact task it proposes (status proposal,
// worker.go). Only the person who asked confirms it ("Do it": agentnet do
// ID, ConfirmProposal), which sends exactly the stored text as a task
// replying to the proposal; the task then meets the host's normal task
// approval. On the host, a task matches the proposal it carries out only
// when it comes from the proposal's addressee with the text byte for byte:
// it then carries that provenance (the question, the proposal, who
// confirmed it) to the run and to the person approving it, and a second
// confirmation is not run twice. A match grants nothing; a task that does
// not match is an ordinary task.
//
// TODO(integrate:P3): conversation (v2) proposals and the page's do_it
// action build on P3's message views; this covers device threads.

// ProposalView is the provenance of a task that carries out a proposal.
type ProposalView struct {
	QuestionID  string `json:"question_id"`
	Question    string `json:"question"` // its first line
	Asker       string `json:"asker"`
	ProposalID  string `json:"proposal_id"`
	Proposal    string `json:"proposal"` // its first line; the task's own text is the whole proposal
	ConfirmedBy string `json:"confirmed_by"`
}

// proposalFor is the proposal (an answer this host sent with status
// proposal) that device task in carries out: one replying to it, from the
// proposal's addressee, with its text exactly; "" when it is an ordinary
// task.
func proposalFor(q dbq, in envelope.Inner) (string, error) {
	if in.Kind != envelope.KindTask || in.Conv != "" || in.ReplyTo == "" {
		return "", nil
	}
	var id string
	err := q.QueryRow(`SELECT id FROM outbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL AND status = ? AND coalesce(kind, json_extract(envelope, '$.kind')) = ? AND recipient = ? AND body = ?`,
		in.ReplyTo, envelope.StatusProposal, envelope.KindAnswer, in.From, in.Body).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// proposalConfirmedBy is the task, other than id, that carried out
// proposal already ("" none): nothing is run twice.
func proposalConfirmedBy(q dbq, proposal, from, id string) (string, error) {
	var first string
	err := q.QueryRow(`SELECT id FROM inbox WHERE conv IS NULL AND kind = ? AND reply_to = ? AND sender = ? AND id != ? AND state != ? ORDER BY received_ms, id LIMIT 1`,
		envelope.KindTask, proposal, from, id, stateNotRun).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return first, err
}

// ProposalOf is the provenance of received task id when it carries out a
// proposal this device made (nil: an ordinary task).
func (a *Agent) ProposalOf(id string) (*ProposalView, error) {
	var in envelope.Inner
	var conv sql.NullString
	err := a.store.db.QueryRow(`SELECT id, sender, kind, body, coalesce(reply_to, ''), conv FROM inbox WHERE id = ? AND local = 0 AND replica = 0`, id).
		Scan(&in.ID, &in.From, &in.Kind, &in.Body, &in.ReplyTo, &conv)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in.Conv = conv.String
	proposal, err := proposalFor(a.store.db, in)
	if err != nil || proposal == "" {
		return nil, err
	}
	v := &ProposalView{ProposalID: proposal, Proposal: firstLine(in.Body), ConfirmedBy: in.From, Asker: in.From}
	// The question it answered: received here from the asker.
	err = a.store.db.QueryRow(`SELECT q.id, q.body, q.sender FROM outbox o JOIN inbox q ON q.id = o.reply_to WHERE o.id = ?`, proposal).Scan(&v.QuestionID, &v.Question, &v.Asker)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	v.Question = firstLine(v.Question)
	return v, nil
}

// ErrNotConfirmable refuses a Do it the proposal does not allow.
var ErrNotConfirmable = errors.New("that is not a proposal you can confirm")

// ConfirmProposal is "Do it" on proposal id, an answer this device
// received to a question it asked: it sends exactly the proposal's stored
// text as a task replying to it (to the same named agent, if any), which
// the host then runs under its normal task approval. Confirming again
// returns the task already sent. It refuses a proposal whose sender's key
// change is pending, one edited or deleted after it was made, a blank or
// erased one, a copy (replica or history), and one answering anything but
// a question this device asked.
func (a *Agent) ConfirmProposal(ctx context.Context, id string) (SendResult, error) {
	var from, kind, status, body, replyTo, key string
	var conv sql.NullString
	var local, replica bool
	err := a.store.db.QueryRow(`SELECT sender, kind, coalesce(status, ''), body, coalesce(reply_to, ''), coalesce(verified_by, ''), conv, local, replica
		FROM inbox i WHERE id = ? AND ref_id IS NULL AND coalesce(sub, '') = '' AND NOT `+erasedIn, id).
		Scan(&from, &kind, &status, &body, &replyTo, &key, &conv, &local, &replica)
	if errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, ErrNoMessage
	}
	if err != nil {
		return SendResult{}, err
	}
	switch {
	case kind != envelope.KindAnswer || status != envelope.StatusProposal:
		return SendResult{}, ErrNotConfirmable
	case conv.Valid:
		return SendResult{}, errors.New("a proposal in a conversation is confirmed there (not yet from the command line)") // TODO(integrate:P3)
	case local || replica || key == "":
		return SendResult{}, errors.New("this copy of the proposal cannot be confirmed here")
	case strings.TrimSpace(body) == "":
		return SendResult{}, errors.New("the proposal has no text left to run")
	}
	var pending sql.NullString
	if err := a.store.db.QueryRow(`SELECT pending FROM peers WHERE address = ?`, from).Scan(&pending); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, err
	} else if pending.Valid {
		return SendResult{}, ErrKeyPending
	}
	// Changed after it was made: what would run is no longer what it says.
	var controls int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv IS NULL AND sender = ? AND ref_id = ? AND ref_fp = ? AND sub IN (?, ?)`,
		from, id, key, envelope.SubRevision, envelope.SubRetraction).Scan(&controls); err != nil {
		return SendResult{}, err
	}
	if controls > 0 {
		return SendResult{}, errors.New("the proposal was edited or deleted after it was made, so it cannot be confirmed")
	}
	// Asked by this device: it answers a question this device sent there.
	var target string
	err = a.store.db.QueryRow(`SELECT coalesce(target, '') FROM outbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL AND coalesce(kind, json_extract(envelope, '$.kind')) = ? AND (recipient = ? OR substr(recipient, 1, length(?) + 1) = ? || '#')`,
		replyTo, envelope.KindQuestion, from, from, from).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, errors.New("only the device that asked the question confirms its proposal")
	}
	if err != nil {
		return SendResult{}, err
	}
	// Confirmed already: that task stands.
	var sent string
	err = a.store.db.QueryRow(`SELECT id FROM outbox WHERE conv IS NULL AND ref_id IS NULL AND coalesce(kind, json_extract(envelope, '$.kind')) = ? AND reply_to = ? AND recipient = ? ORDER BY created_ms, id LIMIT 1`,
		envelope.KindTask, id, from).Scan(&sent)
	if err == nil {
		r, err := a.Status(ctx, sent, 0)
		return SendResult{ID: sent, State: r.State, Path: r.Path}, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, err
	}
	out := Outgoing{To: from, Kind: envelope.KindTask, Body: body, ReplyTo: id}
	if target != "" {
		var t envelope.Target
		if json.Unmarshal([]byte(target), &t) == nil && t.AgentID != "" {
			out.Target = &t // the same named agent that proposed it
		}
	}
	return a.SendMessage(ctx, out)
}
