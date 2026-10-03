package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Headless machines (R05, R06, R09; MEL-497, MEL-426, MEL-435): a bot on a
// server nobody sits at, and the person responsible for it deciding from
// their own messenger.
//
// Three records, all version 3 controls (envelope.go) sent only to
// devices whose signed capabilities include protocol.CapHeadless:
//
//   - status: the device that HOLDS a request says where it stands
//     (queued, awaiting a decision, running, needs a person, stopped...).
//     Its Ref is the request (id, or logical id in a conversation) under
//     the REQUESTER's key; the receiver keeps it only from the device that
//     holds that very request (the recipient of a device message; the
//     target device of a conversation request). Highest counter wins.
//     Custody and delivery stay what they are: a status never says
//     "delivered", a receipt never says "running".
//   - report: this machine's review notice (reviewnotice.go) carries, for
//     granted operators, the waiting requests themselves (id, requester
//     and key, kind, state, blocker category, attempt, first line); anyone
//     else still gets a count and nothing more. A report is a snapshot
//     with a time, never a live queue.
//   - decision: a granted operator (operators.go) accepts, declines,
//     resolves, replies to or stops one request the host holds, naming the
//     state and attempt they saw in a report the host itself sent them (a
//     report is believed only from its host). The host applies it in one
//     transaction with the decision's own storage, so a replay, a double
//     click or a crash never runs anything twice, and answers with a
//     status naming the decision: the resulting state, or why it was
//     refused.
//
// Nothing here creates a job, an alert or a chat turn; nothing received
// grants anything (operators are named locally, once).

// ExecView is a request's execution state as its executing device last
// reported it: never inferred from delivery or presence.
type ExecView struct {
	State   string `json:"state"`
	At      int64  `json:"at"` // the host's clock, unix seconds
	Detail  string `json:"detail,omitempty"`
	Host    string `json:"host"`
	Attempt int64  `json:"attempt,omitempty"`
	// Stale: the host is not connected to the relay now (as the relay's
	// current member list says), or a running state is older than
	// ExecRunningMaxAge, so this may be old news. At stays the host's own
	// timestamp either way.
	Stale bool `json:"stale,omitempty"`
}

// ExecRunningMaxAge bounds how long a "running" status is shown as
// current: the host reports it once, at the claim, so past this age even a
// connected host's word is marked stale rather than shown as live.
const ExecRunningMaxAge = time.Hour

// settle finalizes an execution view for display: a terminal answer or
// result the host sent for the request (its own word too, in the reply
// itself) dominates an earlier running state, and a running state past
// ExecRunningMaxAge is stale even from a connected host.
func (e *ExecView) settle(connected bool, answerStatus string, answerAt int64, now int64) {
	if answerStatus != "" && !execTerminal(e.State) {
		e.State, e.At, e.Detail = answerStatus, answerAt, ""
	}
	e.Stale = !connected || (e.State == stateRunning && now-e.At > int64(ExecRunningMaxAge/time.Second))
}

// legacyAnswer finds, among a device thread's messages, the received answer
// or result to request id and returns its outcome and the sender's time.
func legacyAnswer(msgs []ConversationMessage, id string) (status string, at int64) {
	for i := range msgs {
		m := &msgs[i]
		if m.Dir == "in" && m.ReplyTo == id && execTerminal(m.Status) {
			at = m.SentAt.Unix()
			if m.SentAt.IsZero() {
				at = m.At.Unix()
			}
			return m.Status, at
		}
	}
	return "", 0
}

// execTerminal reports whether state is one a host reports last.
func execTerminal(state string) bool {
	switch state {
	case envelope.StatusDone, envelope.StatusFailed, envelope.StatusTimeout, envelope.StatusCancelled, envelope.StatusDeclined, envelope.StatusInterrupted:
		return true
	}
	return false
}

// Report is a review notice with its requests named (version 2 body).
type Report struct {
	V     int          `json:"v"`
	At    int64        `json:"at"`
	Host  string       `json:"host"`
	Items []ReportItem `json:"items"`
}

// ReportItem is one waiting request as the host reported it.
type ReportItem struct {
	ID         string          `json:"id"` // the request's id on the host
	From       string          `json:"from"`
	Key        string          `json:"key"` // the requester's key as the host verified it
	Kind       string          `json:"kind"`
	State      string          `json:"state"`
	Blocker    string          `json:"blocker"` // BlockerAcceptance, BlockerApproval, BlockerNeedsHuman
	Since      int64           `json:"since"`
	Attempt    int64           `json:"attempt"`
	Excerpt    string          `json:"excerpt,omitempty"`    // the request's first line: operators only
	Actionable bool            `json:"actionable,omitempty"` // the recipient's key is a granted operator on the host
	Result     *DecisionResult `json:"result,omitempty"`     // filled by the reader from the host's answers to its decisions
}

// DecisionResult is what the host answered to one of this device's
// decisions on a reported request.
type DecisionResult struct {
	Decision string `json:"decision"`
	State    string `json:"state"`
	Refused  string `json:"refused,omitempty"`
	At       int64  `json:"at"`
}

// Blocker categories a report names: what stands between a request and
// running. Never a harness's own words.
const (
	BlockerAcceptance = "awaiting_acceptance"    // a task nobody accepted
	BlockerApproval   = "question_not_approved"  // a question from a sender not approved for automatic answers
	BlockerNeedsHuman = "needs_human"            // the responder said a person must decide
	BlockerOther      = "waiting_for_the_person" // anything else in review
)

func blockerOf(kind, state string) string {
	switch {
	case state == stateAwaiting:
		return BlockerAcceptance
	case state == stateHeld:
		return BlockerApproval
	case state == stateNeedHuman:
		return BlockerNeedsHuman
	}
	return BlockerOther
}

// ParseReport reads a version 2 report body; ok is false for the older
// count-only text.
func ParseReport(body string) (Report, bool) {
	if !strings.HasPrefix(body, "{") {
		return Report{}, false
	}
	var r Report
	if err := decodeStrict([]byte(body), &r); err != nil || r.V != 2 {
		return Report{}, false
	}
	return r, true
}

// ---- status: the host's word on a request ----

// statusDetail maps an inbox state to what may be told about it: a state
// name and a bounded category; never the responder's own text.
func statusOf(state string) (public, detail string, ok bool) {
	switch state {
	case statePending, stateAccepted:
		return "queued", "queued for the agent here", true
	case stateAwaiting:
		return "awaiting", BlockerAcceptance, true
	case stateHeld:
		return "awaiting", BlockerApproval, true
	case stateRunning:
		return "running", "", true
	case stateNeedHuman:
		return "needs_human", BlockerNeedsHuman, true
	case stateResolved:
		return "resolved", "a person here closed it without a reply", true
	case stateJobFailed:
		return "failed", "the agent's run failed here", true
	case stateInterrupt:
		return "interrupted", "the daemon stopped while it ran", true
	case stateCancelled, stateCancelReq:
		return "cancelled", "stopped here", true
	case stateNotRun, stateNotDelivered:
		return "not_run", "not run here", true
	case stateDeclined:
		return "declined", "", true
	}
	return "", "", false
}

// noteStatus tells the requester (and, in a conversation, its members)
// where request id stands now, if the request is one whose state the
// requester cannot see otherwise. Best effort, in the background: a
// requester that cannot read statuses, or is out of reach, is told
// nothing and nothing waits for it.
func (a *Agent) noteStatus(id string) {
	var selected int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE inbox_id=?`, id).Scan(&selected); err != nil || selected != 0 {
		return // selected input is local continuation data, not a remote job
	}
	var sender, key, kind, state string
	var conv, lid sql.NullString
	var local, replica bool
	err := a.store.db.QueryRow(`SELECT sender, coalesce(verified_by, ''), kind, state, conv, lid, local, replica FROM inbox WHERE id = ?`, id).
		Scan(&sender, &key, &kind, &state, &conv, &lid, &local, &replica)
	if err != nil || local || replica || key == "" || (kind != envelope.KindQuestion && kind != envelope.KindTask) {
		return
	}
	public, detail, ok := statusOf(state)
	if !ok {
		return
	}
	ref := ControlRef{ID: id, Fingerprint: key}
	if conv.Valid {
		ref = ControlRef{Conv: conv.String, ID: lid.String, Fingerprint: key}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		n, err := a.store.nextCounter(ref, envelope.SubStatus, "")
		if err != nil {
			return
		}
		body, _ := json.Marshal(envelope.Status{State: public, N: n, At: time.Now().Unix(), Detail: detail})
		if _, err := a.sendControlAs(ctx, ref, envelope.SubStatus, string(body), protocol.CapHeadless); err != nil && !errors.Is(err, ErrNoControls) {
			a.Logf("status of %s not told to %s: %v", id, sender, err)
		}
	}()
}

// endJob is finishJob with the requester told where the request stands.
func (a *Agent) endJob(id, state, detail string) error {
	err := a.store.finishJob(id, state, detail)
	var selected int
	if e := a.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE inbox_id=?`, id).Scan(&selected); e != nil || selected != 0 {
		return err
	}
	a.noteStatus(id)
	return err
}

// statusAllowed decides whether a status from sender may be kept here: the
// sender holds the request the status names. Legacy: this device sent
// that request to the sender. Conversation: the request's target is the
// sender's device. A status answering a decision (Decision set) is kept
// when this device sent that decision to the sender.
func (a *Agent) statusAllowed(in envelope.Inner, from, senderFP string) (bool, string) {
	var st envelope.Status
	if json.Unmarshal([]byte(in.Body), &st) != nil {
		return false, "malformed status"
	}
	var n int
	if st.Decision != "" {
		// An answer to a decision: only the very decision this device sent
		// there, about that very request, naming the same report and attempt.
		var body string
		err := a.store.db.QueryRow(`SELECT body FROM outbox WHERE id = ? AND recipient = ? AND sub = ? AND conv IS NULL AND ref_id = ? AND ref_fp = ?`,
			st.Decision, from, envelope.SubDecision, in.Ref.ID, in.Ref.Fingerprint).Scan(&body)
		if err != nil {
			return false, "it answers a decision this device did not send there about that request"
		}
		var d envelope.Decision
		if json.Unmarshal([]byte(body), &d) != nil || d.Report != st.Report || d.Attempt != st.Attempt {
			return false, "it answers a decision this device did not make (report or attempt differ)"
		}
		return true, ""
	}
	if in.Conv == "" {
		a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id = ? AND recipient = ? AND conv IS NULL AND ref_id IS NULL AND ? = ?
			AND coalesce(kind, json_extract(envelope, '$.kind')) IN (?, ?)`,
			in.Ref.ID, from, in.Ref.Fingerprint, a.Self().Fingerprint(), envelope.KindQuestion, envelope.KindTask).Scan(&n)
		if n == 0 {
			return false, "the sender does not hold that request of this device"
		}
		return true, ""
	}
	// A conversation request: that logical id, from that requester key, of
	// a request kind, whose one target is the sender's device.
	a.store.db.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE conv = ? AND lid = ? AND json_extract(target, '$.address') = ? AND json_extract(target, '$.fingerprint') = ? AND coalesce(verified_by, claimed_fp, '') = ? AND kind IN (?, ?))
		+ (SELECT count(*) FROM outbox WHERE conv = ? AND lid = ? AND json_extract(target, '$.address') = ? AND json_extract(target, '$.fingerprint') = ? AND ? = ? AND kind IN (?, ?))`,
		in.Conv, in.Ref.ID, from, senderFP, in.Ref.Fingerprint, envelope.KindQuestion, envelope.KindTask,
		in.Conv, in.Ref.ID, from, senderFP, in.Ref.Fingerprint, a.Self().Fingerprint(), envelope.KindQuestion, envelope.KindTask).Scan(&n)
	if n == 0 {
		return false, "the sender is not the device that request (from that key) is for"
	}
	return true, ""
}

// ---- decisions: an operator deciding on a host ----

// Decide sends this device's decision on request id (under the requester's
// key) held by host, as reported in report: action on the request in state
// expect at attempt attempt. The host answers with a status naming the
// decision (see ReportItem.Result).
func (a *Agent) Decide(ctx context.Context, host, id, key, action, expect string, attempt int64, text, report string) (ControlSent, error) {
	if !protocol.ValidID(id) || !protocol.ValidFingerprint(key) {
		return ControlSent{}, errors.New("a decision names the request's id and its sender's key, as the host reported them")
	}
	if host == a.Address {
		return ControlSent{}, errors.New("decide here directly: this machine holds the request")
	}
	feats, err := a.relayFeatures(ctx)
	if err != nil {
		return ControlSent{}, err
	}
	hostKey, err := a.sendKey(ctx, host)
	if err != nil {
		return ControlSent{}, err
	}
	if ok, why := a.capSupport(ctx, host, hostKey, feats, protocol.CapHeadless); !ok {
		return ControlSent{}, fmt.Errorf("%w: %s", ErrNoControls, why)
	}
	recipient, err := hostKey.Recipient()
	if err != nil {
		return ControlSent{}, err
	}
	body, _ := json.Marshal(envelope.Decision{Action: action, Expect: expect, Attempt: attempt, Text: text, Report: report})
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: a.Address, To: host, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubDecision, Body: string(body), Ref: &envelope.Ref{ID: id, Fingerprint: key}}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return ControlSent{}, err
	}
	if err := a.store.addControlOutbox(env, in); err != nil {
		return ControlSent{}, err
	}
	defer notifyDaemon(a.home)
	res, err := a.deliver(ctx, env, nil)
	return ControlSent{ID: env.ID, State: res.State, Detail: res.Detail}, err
}

// admitDecision applies a decision from an operator, or answers why not.
// Everything that decides is read and written in ONE transaction: the
// operator's grant (its exact pinned key), the request's state and attempt
// as the operator named them, the decision's own row (its id: the same
// decision again changes nothing and gets its recorded outcome back) and
// the request's transition. A reply or decline goes through the ordinary
// reply path, whose outbox transaction carries that same claim, so a crash
// can neither lose the action nor let a replay send twice. Nothing about a
// request is disclosed to a sender that is not an operator here beyond the
// refusal itself.
func (a *Agent) admitDecision(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public) error {
	var d envelope.Decision
	if err := json.Unmarshal([]byte(in.Body), &d); err != nil {
		return a.store.holdAs(env, reasonInvalid)
	}
	answer := func(state, refused string) {
		a.answerDecision(ctx, env.From, in, d, state, refused)
	}
	senderFP := sender.Fingerprint()
	// The decision's row, written with whatever it decides (or the refusal
	// it met), so a replay meets the recorded outcome.
	record := func(tx *sql.Tx, outcome string) (fresh bool, err error) {
		now := time.Now()
		res, err := tx.Exec(`INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, received_at, state, verified_by, sub, received_ms, ref_id, ref_fp, read_at, detail)
			VALUES(?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, nullif(?, ''))`,
			in.ID, in.From, in.TS, in.Kind, in.Body, now.Unix(), senderFP, in.Sub, now.UnixMilli(), in.Ref.ID, in.Ref.Fingerprint, now.Unix(), outcome)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n == 1, nil
	}
	// A replay: the recorded outcome, never a fresh look.
	var recorded sql.NullString
	if err := a.store.db.QueryRow(`SELECT detail FROM inbox WHERE id = ?`, in.ID).Scan(&recorded); err == nil {
		state, _ := a.store.jobState(in.Ref.ID)
		answer(state, recorded.String)
		return nil
	}
	// The decision in one transaction, or its refusal recorded in one.
	refuse := func(why string) error {
		tx, err := a.store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := record(tx, why); err != nil {
			return err
		}
		if err := a.store.done(tx.Commit()); err != nil {
			return err
		}
		a.Logf("decision %s from %s refused: %s", in.ID, env.From, why)
		state, _ := a.store.jobState(in.Ref.ID)
		if ok, _ := operatorHolds(a.store.db, env.From, senderFP); !ok {
			state = "" // nothing about the request for a non-operator
		}
		answer(state, why)
		return nil
	}
	// decide checks, within tx, the grant and the exact request as named,
	// and applies change (a request transition) if any.
	var kind, from string
	decide := func(tx *sql.Tx, change string, changeArgs []any) (string, error) {
		ok, err := operatorHolds(tx, env.From, senderFP)
		if err != nil {
			return "", err
		}
		if !ok {
			return ErrNotOperator.Error(), nil
		}
		// The report the operator acted on is one this machine sent that
		// operator, listing that request as the decision names it: a notice
		// from anyone else naming this machine is never grounds to act here.
		var report string
		err = tx.QueryRow(`SELECT body FROM outbox WHERE id = ? AND recipient = ? AND status = ? AND conv IS NULL`,
			d.Report, env.From, envelope.StatusReviewNotice).Scan(&report)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if !reportLists(report, in.Ref.ID, in.Ref.Fingerprint, d.Expect, d.Attempt) {
			return "this machine sent you no report listing that request as you saw it", nil
		}
		var state string
		var attempts int64
		err = tx.QueryRow(`SELECT state, kind, sender, attempts FROM inbox WHERE id = ? AND verified_by = ? AND conv IS NULL AND local = 0 AND ref_id IS NULL`,
			in.Ref.ID, in.Ref.Fingerprint).Scan(&state, &kind, &from, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return "no such request here", nil
		}
		if err != nil {
			return "", err
		}
		if state != d.Expect || attempts != d.Attempt {
			return fmt.Sprintf("the request is no longer as you saw it (now %s, attempt %d)", state, attempts), nil
		}
		if change != "" {
			res, err := tx.Exec(change, append([]any{in.Ref.ID, d.Expect, d.Attempt}, changeArgs...)...)
			if err != nil {
				return "", err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return fmt.Sprintf("%s is not possible for a %s in state %s", d.Action, kind, state), nil
			}
		}
		return "", nil
	}
	const where = ` WHERE id = ? AND state = ? AND attempts = ?`
	switch d.Action {
	case "accept", "resolve", "cancel":
		var change string
		var args []any
		switch d.Action {
		case "accept":
			change = `UPDATE inbox SET state = ?` + where + ` AND ((kind = ? AND state = ?) OR (kind = ? AND state = ?) OR state IN (?, ?, ?, ?))`
			args = []any{envelope.KindTask, stateAwaiting, envelope.KindQuestion, stateHeld, stateInterrupt, stateJobFailed, stateCancelled, stateNeedHuman}
		case "resolve":
			change = `UPDATE inbox SET state = ?` + where + ` AND state = ?`
			args = []any{stateNeedHuman}
		case "cancel":
			change = `UPDATE inbox SET state = ?` + where + ` AND state = ?`
			args = []any{stateRunning}
		}
		target := map[string]string{"accept": stateAccepted, "resolve": stateResolved, "cancel": stateCancelReq}[d.Action]
		tx, err := a.store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// UPDATE inbox SET state = ?target WHERE id = ?, state = ?, attempts = ? ...
		refused, err := decide(tx, strings.Replace(change, "SET state = ?", "SET state = '"+target+"'", 1), args)
		if err != nil {
			return err
		}
		if _, err := record(tx, refused); err != nil {
			return err
		}
		if err := a.store.done(tx.Commit()); err != nil {
			return err
		}
		if refused != "" {
			a.Logf("decision %s from %s on %s refused: %s", in.ID, env.From, in.Ref.ID, refused)
			state, _ := a.store.jobState(in.Ref.ID)
			if refused == ErrNotOperator.Error() || refused == "no such request here" {
				state = ""
			}
			answer(state, refused)
			return nil
		}
	case "reply", "decline":
		// Checked and recorded in the reply's own outbox transaction: the
		// claim runs the same checks, then the ordinary takeover.
		tx, err := a.store.db.Begin()
		if err != nil {
			return err
		}
		refused, err := decide(tx, "", nil)
		tx.Rollback() // a look only; the write happens in the reply's transaction
		if err != nil {
			return err
		}
		if refused != "" {
			return refuse(refused)
		}
		newState, status := stateManual, ""
		if d.Action == "decline" {
			newState, status = stateDeclined, envelope.StatusDeclined
		}
		over := takeOver(in.Ref.ID, kind, newState)
		claim := func(tx *sql.Tx, replyID string) error {
			if refused, err := decide(tx, "", nil); err != nil {
				return err
			} else if refused != "" {
				return &decisionRefused{refused}
			}
			if _, err := record(tx, ""); err != nil {
				return err
			}
			return over(tx, replyID)
		}
		_, err = a.SendMessage(ctx, Outgoing{To: from, Body: d.Text, ReplyTo: in.Ref.ID, Kind: replyKind(kind), Status: status, claim: claim})
		var dr *decisionRefused
		switch {
		case errors.As(err, &dr):
			return refuse(dr.why)
		case err != nil:
			return refuse("could not " + d.Action + ": " + err.Error())
		}
	default:
		return refuse("unknown action")
	}
	a.Logf("decision %s from %s applied to %s: %s", in.ID, env.From, in.Ref.ID, d.Action)
	a.wakeWorker()
	a.NoteChange()
	now, _ := a.store.jobState(in.Ref.ID)
	answer(now, "")
	a.noteStatus(in.Ref.ID) // the requester learns too
	return nil
}

// reportLists reports whether body, a report this machine sent, lists
// request id under key in state at attempt.
func reportLists(body, id, key, state string, attempt int64) bool {
	r, ok := ParseReport(body)
	if !ok {
		return false
	}
	for _, it := range r.Items {
		if it.ID == id && it.Key == key && it.State == state && it.Attempt == attempt {
			return true
		}
	}
	return false
}

// decisionRefused ends a reply's outbox transaction: the decision does not
// hold any more.
type decisionRefused struct{ why string }

func (e *decisionRefused) Error() string { return e.why }

// answerDecision tells the operator what became of its decision: a status
// naming the decision and echoing exactly what it named (report, attempt),
// with the request's resulting state; never the request's execution state
// for others.
func (a *Agent) answerDecision(ctx context.Context, to string, in envelope.Inner, d envelope.Decision, state, refused string) {
	if state == "" {
		state = "not_run" // nothing known to the operator; the refusal says why
	} else if public, _, ok := statusOf(state); ok {
		state = public
	} else {
		state = "answered"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ref := ControlRef{ID: in.Ref.ID, Fingerprint: in.Ref.Fingerprint}
		n, _ := a.store.nextCounter(ref, envelope.SubStatus, "")
		body, _ := json.Marshal(envelope.Status{State: state, N: n, At: time.Now().Unix(), Refused: refused, Decision: in.ID, Report: d.Report, Attempt: d.Attempt})
		if err := a.sendControlTo(ctx, to, ref, envelope.SubStatus, string(body)); err != nil {
			a.Logf("answer to decision %s not sent to %s: %v", in.ID, to, err)
		}
	}()
}

// sendControlTo sends one device-scoped control straight to address (a
// host answering an operator, whose request is not in a thread with it).
func (a *Agent) sendControlTo(ctx context.Context, address string, ref ControlRef, sub, body string) error {
	feats, err := a.relayFeatures(ctx)
	if err != nil {
		return err
	}
	key, err := a.sendKey(ctx, address)
	if err != nil {
		return err
	}
	if ok, why := a.capSupport(ctx, address, key, feats, protocol.CapHeadless); !ok {
		return fmt.Errorf("%w: %s", ErrNoControls, why)
	}
	recipient, err := key.Recipient()
	if err != nil {
		return err
	}
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: a.Address, To: address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: sub, Body: body, Ref: &envelope.Ref{ID: ref.ID, Fingerprint: ref.Fingerprint}}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return err
	}
	if err := a.store.addControlOutbox(env, in); err != nil {
		return err
	}
	_, err = a.deliver(ctx, env, nil)
	return err
}

// ---- reading: what the requester and the operator see ----

// execViews resolves the statuses among rows: the newest per request from
// its executor, statuses answering decisions left out.
func execViews(rows []controlRow) map[ControlRef]ExecView {
	type best struct {
		n  int64
		id string
		v  ExecView
	}
	out := map[ControlRef]best{}
	for _, c := range rows {
		if c.sub != envelope.SubStatus {
			continue
		}
		var st envelope.Status
		if json.Unmarshal([]byte(c.body), &st) != nil || st.Decision != "" {
			continue
		}
		ref := ControlRef{ID: c.refID, Fingerprint: c.refFP}
		cur, ok := out[ref]
		if !ok || st.N > cur.n || (st.N == cur.n && c.id > cur.id) {
			out[ref] = best{st.N, c.id, ExecView{State: st.State, At: st.At, Detail: st.Detail, Host: c.author, Attempt: st.Attempt}}
		}
	}
	views := make(map[ControlRef]ExecView, len(out))
	for ref, b := range out {
		views[ref] = b.v
	}
	return views
}

// hostConnected reports whether the relay's current member list says
// address is connected; unknown counts as not connected (stale).
func (a *Agent) hostConnected(address string) bool {
	v := a.MemberView()
	if !v.Current {
		return false
	}
	for _, m := range v.Members.Members {
		if m.Address == address {
			return m.Presence == protocol.PresenceConnected
		}
	}
	return false
}

// decisionResults attaches, to each item of the report with id reportID
// from host, the host's answer to the last decision this device sent
// about that item FROM THAT REPORT (its id, key, report and attempt bound
// in the decision itself); answers to decisions made from another report
// or attempt stay with theirs.
func (a *Agent) decisionResults(host, reportID string, r *Report) {
	for i := range r.Items {
		it := &r.Items[i]
		rows, err := a.store.db.Query(`SELECT s.body, d.body FROM inbox s JOIN outbox d ON d.id = json_extract(s.body, '$.decision')
			WHERE s.sender = ? AND s.sub = ? AND s.ref_id = ? AND s.ref_fp = ?
			  AND d.recipient = ? AND d.sub = ? AND d.ref_id = s.ref_id AND d.ref_fp = s.ref_fp ORDER BY s.received_ms`,
			host, envelope.SubStatus, it.ID, it.Key, host, envelope.SubDecision)
		if err != nil {
			return
		}
		for rows.Next() {
			var body, decision string
			var st envelope.Status
			var d envelope.Decision
			if rows.Scan(&body, &decision) != nil || json.Unmarshal([]byte(body), &st) != nil || json.Unmarshal([]byte(decision), &d) != nil {
				continue
			}
			if st.Decision == "" || d.Report != reportID || d.Attempt != it.Attempt || st.Report != d.Report || st.Attempt != d.Attempt {
				continue
			}
			if it.Result == nil || st.At >= it.Result.At {
				it.Result = &DecisionResult{Decision: st.Decision, State: st.State, Refused: st.Refused, At: st.At}
			}
		}
		rows.Close()
	}
}

// NoticeReport returns the report a received review notice carries, with
// this device's decision results, or false for a count-only notice. A
// machine reports only its own requests: a body naming another host is
// not a report (the authenticated sender is the host, never the body).
func (a *Agent) NoticeReport(m Message) (Report, bool) {
	r, ok := ParseReport(m.Body)
	if !ok || (r.Host != "" && r.Host != m.From) {
		return Report{}, false
	}
	r.Host = m.From
	a.decisionResults(m.From, m.ID, &r)
	return r, true
}
