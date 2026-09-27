package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Errors for replying to questions and tasks.
var (
	ErrBeingAnswered   = errors.New("the worker is answering this now; run `agentnet cancel` first")
	ErrAlreadyAnswered = errors.New("this has already been answered")
	ErrNotPending      = errors.New("nothing to accept: not a held question or a task awaiting acceptance")
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
		res, err := tx.Exec(`UPDATE inbox SET state = ?, responder = 'manual', result_id = ? WHERE id = ? AND state IN (?, ?, ?, ?, ?, ?, ?)`,
			newState, replyID, id, statePending, stateAccepted, stateHeld, stateAwaiting, stateJobFailed, stateInterrupt, stateCancelled)
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
	sender, kind, err := a.store.inboxKind(id)
	if err != nil {
		return SendResult{}, fmt.Errorf("no inbox message %s", id)
	}
	m := Outgoing{To: sender, Body: body, ReplyTo: id, Files: files, Kind: replyKind(kind), claim: takeOver(id, kind, stateManual)}
	if kind == envelope.KindTask {
		m.Status = envelope.StatusDone
	}
	return a.SendMessage(ctx, m)
}

// Decline refuses a task (or held question) and tells the sender.
func (a *Agent) Decline(ctx context.Context, id, reason string) (SendResult, error) {
	sender, kind, err := a.store.inboxKind(id)
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
// question, or explicitly retry one that was interrupted, failed or
// cancelled. Only the local user can do this; nothing received can.
func (a *Agent) Accept(id string) error {
	res, err := a.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ? AND
		((kind = ? AND state = ?) OR (kind = ? AND state = ?) OR (kind IN (?, ?) AND state IN (?, ?, ?)))`,
		stateAccepted, id, envelope.KindTask, stateAwaiting, envelope.KindQuestion, stateHeld,
		envelope.KindTask, envelope.KindQuestion, stateInterrupt, stateJobFailed, stateCancelled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotPending
	}
	notifyDaemon(a.home)
	return nil
}

// Cancel asks the worker to stop the question or task it is running.
func (a *Agent) Cancel(id string) error {
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

// Approve lets questions from address be answered automatically.
func (a *Agent) Approve(address string) error {
	_, err := a.store.db.Exec(`INSERT OR IGNORE INTO approvals(address, added_at) VALUES(?, ?)`, address, time.Now().Unix())
	return err
}

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
	return tx.Commit()
}
