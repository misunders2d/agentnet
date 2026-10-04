package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
)

// Errors for replying to questions and tasks.
var (
	ErrBeingAnswered   = errors.New("the worker is answering this now; run `agentnet cancel` first")
	ErrAlreadyAnswered = errors.New("this has already been answered")
	ErrNotPending      = errors.New("nothing to accept: not a held question or a task awaiting acceptance")
	// ErrNothingRuns refuses an accept that nothing here would act on: the
	// item stays as it was, and the error says why and what would.
	ErrNothingRuns = errors.New("nothing here would run it, so it was not accepted")
)

// replyKind is what answers a received message of kind k.
func replyKind(k string) string {
	switch k {
	case envelope.KindQuestion:
		return envelope.KindAnswer
	case envelope.KindTask:
		return envelope.KindResult
	}
	return envelope.KindMessage
}

// takeOver claims a received question or task for a reply written by hand
// (or a decline), in the same transaction that stores the reply. Plain
// messages need no claim.
func takeOver(id, kind, newState string) func(*sql.Tx, string) error {
	if kind != envelope.KindQuestion && kind != envelope.KindTask {
		return nil
	}
	return func(tx *sql.Tx, replyID string) error {
		res, err := tx.Exec(`UPDATE inbox SET state = ?, responder = 'manual', result_id = ? WHERE id = ? AND state IN (?, ?, ?, ?, ?, ?, ?, ?)`,
			newState, replyID, id, statePending, stateAccepted, stateHeld, stateAwaiting, stateJobFailed, stateInterrupt, stateCancelled, stateNeedHuman)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		var state string
		if err := tx.QueryRow(`SELECT state FROM inbox WHERE id = ?`, id).Scan(&state); err != nil {
			return err
		}
		if state == stateRunning || state == stateCancelReq {
			return ErrBeingAnswered
		}
		return ErrAlreadyAnswered
	}
}

// Reply sends body (and files) to the sender of inbox message id. Replying
// to a question or task takes it over: the worker will not also answer it,
// and a reply is refused while the worker is running it.
func (a *Agent) Reply(ctx context.Context, id, body string, files ...string) (SendResult, error) {
	return a.ReplyWait(ctx, id, body, 0, files...)
}

// ReplyWait is Reply that waits up to wait for the recipient's receipt, as
// Outgoing.Wait does.
func (a *Agent) ReplyWait(ctx context.Context, id, body string, wait time.Duration, files ...string) (SendResult, error) {
	sender, kind, err := a.store.inboxKind(id)
	if errors.Is(err, ErrConversationItem) {
		return SendResult{}, err
	}
	if err != nil {
		return SendResult{}, fmt.Errorf("no inbox message %s", id)
	}
	m := Outgoing{To: sender, Body: body, ReplyTo: id, Files: files, Kind: replyKind(kind), claim: takeOver(id, kind, stateManual), Wait: wait}
	if kind == envelope.KindTask {
		m.Status = envelope.StatusDone
	}
	return a.SendMessage(ctx, m)
}

// Decline refuses a task (or held question) and tells the sender.
func (a *Agent) Decline(ctx context.Context, id, reason string) (SendResult, error) {
	if in, fp, e := a.storedReceiverSetup(id); e == nil && in.ReceiverRoute.Op == "delegate" {
		return a.declineReceiverDelegation(ctx, in, fp, reason)
	}
	sender, kind, err := a.store.inboxKind(id)
	if errors.Is(err, ErrConversationItem) {
		return SendResult{}, err
	}
	if err != nil {
		return SendResult{}, fmt.Errorf("no inbox message %s", id)
	}
	if kind != envelope.KindTask && kind != envelope.KindQuestion {
		return SendResult{}, errors.New("only questions and tasks can be declined")
	}
	return a.SendMessage(ctx, Outgoing{To: sender, Body: reason, ReplyTo: id, Kind: replyKind(kind),
		Status: envelope.StatusDeclined, claim: takeOver(id, kind, stateDeclined)})
}

// Accept lets the worker run a task awaiting acceptance, answer a held
// question, or explicitly rerun one that was interrupted, failed, cancelled
// or marked needs_human. A rerun starts afresh; it does not resume the
// earlier run. Only the local user can do this; nothing received can.
//
// In a DM, only a request to this device's agent can be accepted (its
// states arise only there; a guest's question awaits like a task); it
// still runs only if its participation allows
// it when the worker claims it (agentjob.go). A question or task for the
// person (stateConvHeld) is answered in the conversation instead.
func (a *Agent) Accept(id string) error {
	if in, _, e := a.storedReceiverSetup(id); e == nil && in.ReceiverRoute.Op == "delegate" {
		res, e := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=? AND state=?`, stateAccepted, id, stateAwaiting)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotPending
		}
		a.NoteChange()
		notifyDaemon(a.home)
		return nil
	}
	acceptable := []any{id, envelope.KindTask, stateAwaiting, envelope.KindQuestion, stateHeld,
		envelope.KindTask, envelope.KindQuestion, stateInterrupt, stateJobFailed, stateCancelled, stateNeedHuman,
		envelope.KindTask, envelope.KindQuestion, stateAwaiting, envelope.KindTask, envelope.KindQuestion, stateInterrupt, stateJobFailed, stateCancelled, stateNeedHuman}
	const acceptableWhere = ` WHERE id = ? AND (conv IS NULL AND
		((kind = ? AND state = ?) OR (kind = ? AND state = ?) OR (kind IN (?, ?) AND state IN (?, ?, ?, ?)))
		OR pid IS NOT NULL AND replica = 0 AND ((kind IN (?, ?) AND state = ?) OR (kind IN (?, ?) AND state IN (?, ?, ?, ?))))`
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM inbox`+acceptableWhere, acceptable...).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrNotPending
	}
	if why, err := a.acceptBlocked(id); err != nil {
		return err
	} else if why != "" {
		return fmt.Errorf("%w: %s", ErrNothingRuns, why)
	}
	res, err := a.store.db.Exec(`UPDATE inbox SET state = ?`+acceptableWhere, append([]any{stateAccepted}, acceptable...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotPending
	}
	a.NoteChange() // a worker in this process looks again
	notifyDaemon(a.home)
	a.noteStatus(id)
	return nil
}

// acceptBlocked says why nothing here would run request id if it were
// accepted ("" when something would, or when id is no such request, which
// Accept refuses on its own): a request to this device's agent whose
// participation ended (or whose asking guest left), or one no responder
// or local agent here would take (NothingRuns).
func (a *Agent) acceptBlocked(id string) (string, error) {
	var j job
	var target string
	var pid sql.NullString
	err := a.store.db.QueryRow(`SELECT sender, coalesce(verified_by, ''), kind, coalesce(conv, ''), pid, coalesce(target, ''), local FROM inbox WHERE id = ?`, id).
		Scan(&j.From, &j.Key, &j.Kind, &j.Conv, &pid, &target, &j.Local)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if target != "" {
		j.Target = &envelope.Target{}
		if json.Unmarshal([]byte(target), j.Target) != nil {
			j.Target = nil
		}
	}
	agentID := ""
	if j.Target != nil {
		agentID = j.Target.AgentID
	}
	if pid.Valid {
		j.ID, j.PID = id, pid.String
		v, why, err := agentVerdict(a.store.db, j.agentReq(stateAccepted), a.Address, a.Self().Fingerprint(), false, map[string]*partView{})
		if err != nil {
			return "", err
		}
		if v == verdictStop {
			return why, nil
		}
	}
	return a.NothingRuns(agentID), nil
}

// NothingRuns says why nothing on this device would run a request to its
// agent agentID ("" its default responder), or "" when something would.
func (a *Agent) NothingRuns(agentID string) string {
	r, err := a.Responder()
	if err != nil {
		return err.Error()
	}
	if agentID == "" {
		if r != nil {
			return ""
		}
		if chosen, _ := a.ResponderChosen(); chosen {
			return "you chose to answer by hand here, so no responder runs anything: answer it yourself (agentnet reply ID TEXT), or choose a responder (agentnet responder set)"
		}
		return "no responder is chosen here: choose one (agentnet responder set), or answer it yourself (agentnet reply ID TEXT)"
	}
	if _, err := a.ResolveExecutorIn(a.store.db, agentID, r); err != nil {
		return "the agent it names (" + agentID + ") is not available here: " + err.Error()
	}
	return ""
}

// Resolve records that the local human dealt with an item the responder
// marked as needing their decision, with a question or task whose run was
// interrupted (it is not run again), or with one held for them in a
// conversation (closed without answering there), or dismisses the notice
// of an agent participation accepted without a click (id: its PID;
// selfconsent.go). It sends nothing; to answer the sender, use Reply or
// Decline instead.
func (a *Agent) Resolve(id string) error {
	res, err := a.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ? AND (state = ? OR state = ? AND kind IN (?, ?))`,
		stateResolved, id, stateNeedHuman, stateInterrupt, envelope.KindQuestion, envelope.KindTask)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// A question or task held for the person in a conversation, closed
		// without answering it there: nothing is sent (no status either: a
		// person's turn has no executing device to speak for it).
		if res, err := a.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ? AND state = ?`, stateResolved, id, stateConvHeld); err != nil {
			return err
		} else if n, _ := res.RowsAffected(); n == 1 {
			a.NoteChange()
			return nil
		}
		if dismissed, err := a.dismissSelfConsentNotice(id); err != nil || dismissed {
			return err
		}
		return errors.New("nothing to resolve: not marked needs_human or interrupted, nor held for you in a conversation")
	}
	notifyDaemon(a.home)
	a.noteStatus(id)
	return nil
}

// ErrNoDaemon refuses a cancel no daemon would act on.
var ErrNoDaemon = errors.New("no daemon runs here now, so nothing is running this: a daemon stopped while it ran, and it shows as interrupted when the daemon starts again (agentnet daemon), any harness of it still running stopped first (Linux)")

// Cancel asks the worker to stop the question or task it is running. Only
// a daemon runs anything here: with none running (it stopped or crashed
// while this ran), the cancel is refused rather than recorded as if
// something would act on it.
func (a *Agent) Cancel(id string) error {
	if !a.daemonRuns() {
		if s, _ := a.store.jobState(id); s == stateRunning || s == stateCancelReq {
			return ErrNoDaemon
		}
	}
	res, err := a.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ? AND state = ?`, stateCancelReq, id, stateRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("not running")
	}
	notifyDaemon(a.home)
	return nil
}

// daemonRuns reports whether a daemon holds this home now (this process's
// own included); when that cannot be told, it says it does.
func (a *Agent) daemonRuns() bool {
	release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock"))
	if err != nil {
		return true
	}
	release()
	return false
}

// Approve lets questions from address be answered automatically.
func (a *Agent) Approve(address string) error {
	_, err := a.store.db.Exec(`INSERT OR IGNORE INTO approvals(address, added_at) VALUES(?, ?)`, address, time.Now().Unix())
	if err == nil {
		notifyDaemon(a.home)
	}
	return err
}

// Approvals counts the agents whose questions are answered automatically.
func (a *Agent) Approvals() (int, error) {
	var n int
	err := a.store.db.QueryRow(`SELECT count(*) FROM approvals`).Scan(&n)
	return n, err
}

// NoApprovals is what a responder does with no agent approved.
const NoApprovals = "no agent approved yet, so every question waits for you (agentnet inbox --review); approve one with agentnet approve ADDRESS only if the person asks"

// Unapprove stops automatic answers for address. Its questions still
// waiting for the worker go back to held (the worker also re-checks approval
// when it claims); ones you accepted explicitly stay accepted, and one
// already running may finish unless cancelled.
func (a *Agent) Unapprove(address string) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM approvals WHERE address = ?`, address); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE inbox SET state = ? WHERE sender = ? AND kind = ? AND state = ?`,
		stateHeld, address, envelope.KindQuestion, statePending); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}
