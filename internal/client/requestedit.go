package client

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// This subkind is written only by the local scheduler. It is neither signed
// history nor an independently authored request.
const requestEditJob = "local-request-edit"

const requestEditIndex = `CREATE INDEX inbox_edit_jobs ON inbox(conv,sender,reply_to)
	WHERE sub='` + requestEditJob + `';`

const editJobParent = `CASE WHEN sub='` + requestEditJob + `' THEN coalesce(reply_to,'') ELSE '' END`

func editReplyTo(j job) string {
	if j.editOf != "" {
		return j.editOf
	}
	return j.ID
}

func (a *Agent) editExecutorCurrent(j job, harness string) (bool, error) {
	if j.editOf == "" {
		return true, nil
	}
	var agent, priorHarness string
	err := a.store.db.QueryRow(`SELECT coalesce(nullif(agent_id,''),json_extract(target,'$.agent_id'),''),coalesce(responder,'') FROM inbox WHERE id=?`, j.editOf).
		Scan(&agent, &priorHarness)
	return err == nil && agent == j.AgentID && priorHarness == harness, err
}

// Local storage of a derived job is distinct from a request authored locally.
// Claiming must still evaluate the original sender's grants.
const executionLocal = `CASE WHEN sub='` + requestEditJob + `' THEN
	coalesce((SELECT p.local FROM inbox p WHERE p.id=inbox.reply_to),0) ELSE local END`

// requestEditAtClaim reads exact, directly verified revisions from the same
// transaction that owns the first attempt. The admitted message stays intact.
// An edit of imported history or work already run cannot change this snapshot.
func requestEditAtClaim(tx *sql.Tx, j job) (string, error) {
	if j.Receiver != nil || j.Kind != envelope.KindQuestion && j.Kind != envelope.KindTask {
		return j.Body, nil
	}
	_, text, err := currentRequestEdit(tx, j.ID, true)
	if err == sql.ErrNoRows {
		return j.Body, nil
	}
	return text, err
}

func currentRequestEdit(q dbq, request string, firstClaim bool) (id, text string, err error) {
	err = q.QueryRow(`
		WITH original AS (
			SELECT i.id,i.conv,coalesce(i.lid,i.id) AS logical,i.sender,i.verified_by,i.local
			FROM inbox i WHERE i.id=? AND (NOT ? OR i.attempts=1) AND i.replica=0 AND i.ref_id IS NULL
			AND i.receiver_route IS NULL AND coalesce(i.origin,'') NOT LIKE 'agent:%'
			AND NOT EXISTS (SELECT 1 FROM json_each(i.human,'$.proof') s
				WHERE json_extract(s.value,'$.pid')=json_extract(i.human,'$.author_pid')
				AND json_extract(s.value,'$.type')='scope' AND coalesce(json_extract(s.value,'$.role'),'')='')
			AND NOT `+erasedInFor("i")+`
			AND NOT `+retractedClause+`
		), revisions AS (
			SELECT coalesce(e.lid,e.id) AS id, e.body FROM original p JOIN inbox e
			ON e.conv IS p.conv AND e.ref_id=p.logical AND e.ref_fp=p.verified_by
			WHERE e.sub='revision' AND e.sender=p.sender AND e.verified_by=p.verified_by AND e.replica=0
			UNION ALL
			SELECT coalesce(e.lid,e.id), e.body FROM original p JOIN outbox e
			ON e.conv IS p.conv AND e.ref_id=p.logical AND e.ref_fp=p.verified_by
			WHERE p.local=1 AND e.sub='revision' AND json_extract(e.envelope,'$.from')=p.sender
		)
		SELECT id,json_extract(body,'$.text') FROM revisions WHERE json_valid(body)
		AND json_type(body,'$.rev')='integer' AND json_extract(body,'$.rev')>0
		AND json_type(body,'$.text')='text' AND length(trim(json_extract(body,'$.text')))>0
		ORDER BY json_extract(body,'$.rev') DESC,id DESC LIMIT 1`, request, firstClaim).Scan(&id, &text)
	return id, text, err
}

// queueRunningRequestEdit runs inside the transaction that commits the
// authenticated edit. That transaction and the first claim are serialized:
// a revision is either read by the first claim or bound to the existing run.
func queueRunningRequestEdit(tx *sql.Tx, in envelope.Inner, key string) error {
	if in.Sub != envelope.SubRevision || in.Ref == nil || in.Replica || key == "" || key != in.Ref.Fingerprint {
		return nil
	}
	var parent string
	var attempt int64
	where, args := inScope(ControlRef{Conv: in.Conv, ID: in.Ref.ID, Fingerprint: in.Ref.Fingerprint})
	args = append(args, in.From)
	err := tx.QueryRow(`SELECT i.id,i.attempts FROM inbox i WHERE `+where+`
		AND i.sender=? AND i.replica=0 AND i.ref_id IS NULL AND i.receiver_route IS NULL
		AND i.kind IN ('question','task') AND i.attempts>0
		AND i.state IN ('running','helpers_waiting','answered','needs_human','resolved')
		AND i.steering_parent IS NULL AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs r WHERE r.inbox_id=i.id)
		AND NOT `+retractedClause, args...).Scan(&parent, &attempt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	editID := in.LID
	if editID == "" {
		editID = in.ID
	}
	newest, _, err := currentRequestEdit(tx, parent, false)
	if err == sql.ErrNoRows || err == nil && newest != editID {
		return nil
	}
	if err != nil {
		return err
	}
	var duplicate bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM inbox WHERE sub=? AND ref_id=? AND ref_fp=? AND reply_to=?)`,
		requestEditJob, editID, key, parent).Scan(&duplicate); err != nil || duplicate {
		return err
	}
	// A newer edit replaces a correction still waiting for input. A running
	// handover has an independent native receipt and is never replayed.
	if _, err = tx.Exec(`UPDATE inbox SET state=?,detail='Superseded by a newer edit',status_due=0
		WHERE sub=? AND reply_to=? AND state IN ('pending','part_waiting','accepted','awaiting','held')`,
		stateNotRun, requestEditJob, parent); err != nil {
		return err
	}
	child := protocol.NewID()
	queued := stateAgentWaiting
	if in.Conv == "" {
		queued = statePending
	}
	link := &envelope.FollowupRef{Ref: *in.Ref, Attempt: attempt}
	raw, err := json.Marshal(link)
	if err != nil {
		return err
	}
	now := time.Now()
	_, err = tx.Exec(`INSERT INTO inbox(
		id,sender,ts,kind,body,reply_to,received_at,state,verified_by,conv,lid,sub,replica,target,
		received_ms,pid,ref_id,ref_fp,local,acked,read_at,status_due,topic,human,request_followup,
		queued_followup_parent,queued_followup_attempt)
		SELECT ?,p.sender,?,p.kind,'',p.id,?,?,p.verified_by,p.conv,
		CASE WHEN p.conv IS NOT NULL THEN ? END,?,0,p.target,?,p.pid,?,p.verified_by,
		1,1,?,1,p.topic,p.human,?,?,? FROM inbox p WHERE p.id=?`,
		child, in.TS, now.Unix(), queued, child, requestEditJob, now.UnixMilli(), editID,
		now.Unix(), string(raw), parent, attempt, parent)
	return err
}

// Take text from the authorized edit at claim time. The private queue
// records only the reference so clearing history cannot leave another copy.
func editJobText(q dbq, j job, live bool) (string, bool, error) {
	if j.editOf == "" {
		return j.Body, true, nil
	}
	var editID, oldState string
	var sameAttempt bool
	err := q.QueryRow(`SELECT c.ref_id,p.state,p.attempts=c.queued_followup_attempt
		FROM inbox c JOIN inbox p ON p.id=c.reply_to
		WHERE c.id=? AND p.id=? AND c.sub=? AND p.replica=0`, j.ID, j.editOf, requestEditJob).
		Scan(&editID, &oldState, &sameAttempt)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil || !sameAttempt {
		return "", false, err
	}
	switch oldState {
	case stateRunning:
		if !live {
			return "", false, nil
		}
	case stateAnswered, stateNeedHuman, stateResolved:
		if live {
			return "", false, nil
		}
	default:
		return "", false, nil
	}
	current, text, err := currentRequestEdit(q, j.editOf, false)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return text, current == editID && err == nil, err
}

// Only a correction whose native input has not been sent can move from its
// claimed handover back to the queue when the original finishes.
func (a *Agent) queueEditAfterCompletion(c nativeFollowupClaim) bool {
	if c.editOf == "" {
		return false
	}
	res, err := a.store.db.Exec(`UPDATE inbox SET state=?,status_due=status_due+1,
		detail='Queued; original finished before the edit was sent'
		WHERE id=? AND state=? AND attempts=? AND EXISTS (SELECT 1 FROM inbox p
		WHERE p.id=inbox.reply_to AND p.attempts=inbox.queued_followup_attempt
		AND p.state IN ('answered','needs_human','resolved'))`, c.queuedState, c.ID, stateRunning, c.nativeAttempt)
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false
	}
	a.store.changed()
	a.wakeStatus()
	return true
}

func editStateWord(state string) string {
	switch state {
	case stateAgentWaiting, statePending, stateAccepted:
		return "queued"
	case stateRunning, stateHelpersWaiting, stateCancelReq:
		return "running"
	case stateSteered:
		return "steered"
	case stateAnswered:
		return "answered"
	default:
		return "failed"
	}
}

func editStatusDetail(id, phase string) string { return "request-edit " + id + " " + phase }

func parseEditStatus(detail string) (id, phase string) {
	const prefix = "request-edit "
	if !strings.HasPrefix(detail, prefix) {
		return "", ""
	}
	id, phase, ok := strings.Cut(detail[len(prefix):], " ")
	if !ok || !protocol.ValidID(id) {
		return "", ""
	}
	switch phase {
	case "queued", "running", "steered", "answered", "failed":
		return id, phase
	}
	return "", ""
}

// projectLocalEditStatuses supplies the same view at the executing host,
// including an own-agent run whose status never needs a relay round trip.
func (s *store) projectLocalEditStatuses(rows []controlRow, conv, peer string) ([]controlRow, error) {
	jobs, err := s.db.Query(`SELECT c.id,coalesce(p.lid,p.id),p.verified_by,c.ref_id,c.state
		FROM inbox c JOIN inbox p ON p.id=c.reply_to
		WHERE c.conv IS nullif(?,'') AND (?='' OR c.sender=?) AND c.sub='`+requestEditJob+`'`, conv, peer, peer)
	if err != nil {
		return nil, err
	}
	defer jobs.Close()
	for jobs.Next() {
		var row controlRow
		var edit, state string
		if err := jobs.Scan(&row.id, &row.refID, &row.refFP, &edit, &state); err != nil {
			return nil, err
		}
		body, _ := json.Marshal(envelope.Status{N: 1 << 62, Detail: editStatusDetail(edit, editStateWord(state))})
		row.sub, row.body = envelope.SubStatus, string(body)
		rows = append(rows, row)
	}
	return rows, jobs.Err()
}

func closeUnrunEdit(tx *sql.Tx, id, state, why string) error {
	_, err := tx.Exec(`UPDATE inbox SET state=?,detail=?,status_due=status_due+1 WHERE id=? AND state=?`, stateNotRun, why, id, state)
	return err
}
