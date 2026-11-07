package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// An accepted ordinary message is durable before anything can make it wait:
// the network, the spool lock, a chain refresh or the history catch-up. It
// reuses the guarded scheduled-send record and dispatch, with one difference
// from a schedule: it is due at once and stays in its chat, not in a list.
// A per-conversation worker retries it under the same id, so a slow attempt or
// a restart neither loses it nor sends it twice, and holds up no other chat.
// Telegram's TDLib keeps the same shape: a yet-unsent message is stored and
// shown first, a failure keeps its place (MessagesManager.cpp at 4c6377aa,
// get_next_yet_unsent_message_id, fail_send_message); Signal Desktop's
// JobQueue is the durable retry counterpart.
const queuedSendSchema = `
ALTER TABLE scheduled_sends ADD COLUMN immediate INTEGER NOT NULL DEFAULT 0;
ALTER TABLE scheduled_sends ADD COLUMN diagnostic TEXT NOT NULL DEFAULT '';
CREATE INDEX scheduled_sends_immediate ON scheduled_sends(conv,created_at,id) WHERE immediate=1;
`

const (
	queuedAttemptBudget = 20 * time.Second // a first attempt; the send itself has no deadline
	queuedAttemptMax    = 2 * time.Minute  // a slow link still finishes: each failed attempt doubles the next one's bound
	queuedAttemptLanes  = 4                // attempts at once: enough that stalled chats leave others room
	queuedDiscardedKept = 24 * time.Hour   // a discarded id stays refused this long, its text already gone
	queuedDiagnosticMax = 2000
)

const queuedActive = `state IN ('pending','dispatching','needs_review')`

// ErrQueuedSendDiscarded refuses a late repeat of a message the person discarded.
var ErrQueuedSendDiscarded = errors.New("you discarded this message")
var errQueuedSendGone = errors.New("this message is no longer waiting to be sent")

// QueuedSendResult is what is known when QueueSend returns. Sent is set when an
// attempt stored the message within the wait; AttemptErr when it did not.
type QueuedSendResult struct {
	Row        ScheduledSend
	Sent       *ConvSent
	AttemptErr error
}

type queuedOutcome struct {
	row  ScheduledSend
	sent *ConvSent
	err  error
}

type queuedSends struct {
	sync.Mutex
	workers map[string]chan struct{} // conversation: its running worker's wake
	waiters map[string]chan queuedOutcome
	lanes   chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
	budget  time.Duration // one attempt's bound; zero: queuedAttemptBudget
}

func (q *queuedSends) await(id string) chan queuedOutcome {
	q.Lock()
	defer q.Unlock()
	if q.waiters == nil {
		q.waiters = map[string]chan queuedOutcome{}
	}
	ch := make(chan queuedOutcome, 1)
	q.waiters[id] = ch
	return ch
}
func (q *queuedSends) forget(id string) {
	q.Lock()
	defer q.Unlock()
	delete(q.waiters, id)
}
func (q *queuedSends) settle(id string, o queuedOutcome) {
	q.Lock()
	defer q.Unlock()
	if ch := q.waiters[id]; ch != nil {
		select {
		case ch <- o:
		default:
		}
	}
}
func (q *queuedSends) wakeAll() {
	q.Lock()
	defer q.Unlock()
	for _, wake := range q.workers {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
func (q *queuedSends) stop() {
	q.Lock()
	q.closed = true
	cancel := q.cancel
	q.Unlock()
	if cancel != nil {
		cancel()
	}
	q.wg.Wait()
}

// QueueSend accepts one ordinary message under its stable id: stored here
// before it returns, sent by the worker. wait is how long the caller stays to
// hear how the first attempt went; the message does not depend on it. A repeat
// of the same id reaches the same record and never a second send.
func (a *Agent) QueueSend(in ScheduledSendInput, pid string, wait time.Duration) (QueuedSendResult, error) {
	if !protocol.ValidID(in.ID) {
		return QueuedSendResult{}, errors.New("invalid local send id")
	}
	conv, err := a.queuedConversation(in.Conv)
	if err != nil {
		return QueuedSendResult{}, err
	}
	in.Conv, in.immediate, in.pid, in.Timezone = conv, true, pid, "UTC"
	in.Due = time.Now()
	outcome := a.queued.await(in.ID)
	defer a.queued.forget(in.ID)
	row, err := a.saveScheduled("", 0, in)
	if err != nil {
		return QueuedSendResult{}, err
	}
	switch row.State {
	case ScheduledCancelled:
		return QueuedSendResult{Row: row}, ErrQueuedSendDiscarded
	case ScheduledSent:
		return QueuedSendResult{Row: row}, nil
	case ScheduledNeedsReview: // pressing Send again on the same message is its retry,
		// unless the person held it (HoldQueuedSend): then only their Retry resumes it.
		if row, err = a.retryQueuedSend(row.ID, true); err != nil {
			return QueuedSendResult{}, err
		}
		if row.State == ScheduledNeedsReview {
			return QueuedSendResult{Row: row}, nil
		}
	}
	a.startQueuedWorker(row.Conv)
	if wait <= 0 {
		return QueuedSendResult{Row: row}, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case o := <-outcome:
		return QueuedSendResult{Row: o.row, Sent: o.sent, AttemptErr: o.err}, nil
	case <-timer.C:
		return QueuedSendResult{Row: row}, nil
	}
}

// The local part of currentDM: the recovered person's conversation when it
// already exists here. Otherwise the ordinary sender creates it, on the network.
func (a *Agent) queuedConversation(conv string) (string, error) {
	rows, err := a.continuityDMs(conv)
	if err != nil || len(rows) == 0 {
		return conv, err
	}
	peer, err := a.store.logicalPerson(rows[0].Peer.Person)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Peer.Person == peer {
			return r.ID, nil
		}
	}
	return "", errors.New("this conversation must be created on the server first")
}

func (a *Agent) queuedAlreadySent(in ScheduledSendInput) (ScheduledSend, error) {
	var same bool
	err := a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE lid=? AND body=?) OR EXISTS(SELECT 1 FROM inbox WHERE lid=? AND local=1 AND body=?)`, in.ID, in.Body, in.ID, in.Body).Scan(&same)
	if err != nil {
		return ScheduledSend{}, err
	}
	if !same {
		return ScheduledSend{}, ErrScheduledIDConflict
	}
	return ScheduledSend{ID: in.ID, Conv: in.Conv, Topic: in.Topic, Body: in.Body, State: ScheduledSent, Immediate: true}, nil
}

// RetryQueuedSend is the person's explicit retry of a stopped message: its
// audience is read anew because they are approving it now, under the same id.
// A message still on its way only gets its attempt sooner.
func (a *Agent) RetryQueuedSend(id string) (ScheduledSend, error) {
	return a.retryQueuedSend(id, false)
}

func (a *Agent) retryQueuedSend(id string, admission bool) (ScheduledSend, error) {
	s, found, err := a.ScheduledSend(id)
	if err != nil {
		return s, err
	}
	if !found || !s.Immediate || s.State == ScheduledCancelled {
		return s, errQueuedSendGone
	}
	if s.State == ScheduledNeedsReview {
		conv, e := a.queuedConversation(s.Conv)
		if e != nil {
			return s, e
		}
		audience, _, e := a.scheduledAudience(a.store.db, conv, s.Topic)
		if e != nil {
			return s, e
		}
		encoded, _ := json.Marshal(audience)
		now := time.Now().UnixNano()
		// An admission request (not the person's Retry) never resumes a message
		// they held, even from a snapshot read before their hold: the same row
		// condition as dispatch's commit decides, so exactly one wins.
		held := ""
		if admission {
			held = queuedHeldDetail
		}
		result, e := a.store.db.Exec(`UPDATE scheduled_sends SET conv=?,audience=?,state='pending',rev=rev+1,detail='',diagnostic='',due_at=?,updated_at=? WHERE id=? AND immediate=1 AND state='needs_review' AND (?='' OR detail<>?)`, conv, string(encoded), now, now, id, held, held)
		if e != nil {
			return s, e
		}
		if n, _ := result.RowsAffected(); n == 1 {
			a.store.changed()
		}
		if s, _, e = a.ScheduledSend(id); e != nil {
			return s, e
		}
	}
	if s.State == ScheduledPending || s.State == ScheduledDispatching {
		a.startQueuedWorker(s.Conv)
	}
	return s, nil
}

// DiscardQueuedSend is the person's explicit cancel, never a timeout's. It
// races an attempt in the database: the attempt's commit needs the row still
// dispatching, so exactly one of them wins. A message already stored stays sent.
func (a *Agent) DiscardQueuedSend(id string) (ScheduledSend, error) {
	now := time.Now().UnixNano()
	result, err := a.store.db.Exec(`UPDATE scheduled_sends SET state='cancelled',rev=rev+1,body='',files='[]',detail='',diagnostic='',updated_at=? WHERE id=? AND immediate=1 AND state IN ('pending','dispatching','needs_review')`, now, id)
	if err != nil {
		return ScheduledSend{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		var stored bool
		if err = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE lid=?)`, id).Scan(&stored); err != nil {
			return ScheduledSend{}, err
		}
		if stored {
			return ScheduledSend{}, errors.New("this message was already sent")
		}
		return ScheduledSend{}, errQueuedSendGone
	}
	a.store.changed()
	return ScheduledSend{ID: id, State: ScheduledCancelled, Immediate: true}, nil
}

// queuedHeldDetail is what a held message says in its chat.
const queuedHeldDetail = "Stopped. Its files are kept on this device: Retry sends it, Discard removes it."

// HoldQueuedSend is the person's Cancel of a message whose files the page can
// no longer put back in a draft (its File objects ended with a restart). It
// races an attempt exactly as DiscardQueuedSend does: the attempt's commit needs
// the row still dispatching at its revision, so one of them wins. The body,
// file pointers and kept encrypted bytes stay; a held row is never attempted
// again without the person's Retry (needs_review is not resumed after a
// restart, and the kept-file GC already protects it).
func (a *Agent) HoldQueuedSend(id string) (ScheduledSend, error) {
	// A message already stopped by a failure is held too: the person's stop is
	// recorded, so nothing but their Retry resumes it (it keeps its diagnostic).
	result, err := a.store.db.Exec(`UPDATE scheduled_sends SET state='needs_review',rev=rev+1,detail=?,diagnostic=CASE WHEN state='needs_review' THEN diagnostic ELSE '' END,updated_at=? WHERE id=? AND immediate=1 AND (state IN ('pending','dispatching') OR state='needs_review' AND detail<>?)`, queuedHeldDetail, time.Now().UnixNano(), id, queuedHeldDetail)
	if err != nil {
		return ScheduledSend{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		var stored bool
		if err = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE lid=?)`, id).Scan(&stored); err != nil {
			return ScheduledSend{}, err
		}
		if stored {
			return ScheduledSend{}, errors.New("this message was already sent")
		}
		if s, found, e := a.ScheduledSend(id); e == nil && found && s.Immediate && s.State == ScheduledNeedsReview {
			return s, nil // already stopped, with its files kept: the same answer again
		}
		return ScheduledSend{}, errQueuedSendGone
	}
	a.store.changed()
	s, _, err := a.ScheduledSend(id)
	return s, err
}

// QueuedSends lists messages accepted here and not yet stored for sending, in
// one conversation (or its recovered family) oldest first, for its view.
func (a *Agent) QueuedSends(conv string) ([]ScheduledSend, error) {
	family, err := a.continuityDMs(conv)
	if err != nil {
		return nil, err
	}
	ids := []any{conv}
	for _, r := range family {
		if r.ID != conv {
			ids = append(ids, r.ID)
		}
	}
	rows, err := a.store.db.Query(`SELECT `+scheduledCols+` FROM scheduled_sends WHERE immediate=1 AND `+queuedActive+` AND conv IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY created_at,id`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledSend
	for rows.Next() {
		s, e := scanScheduled(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// driveQueuedSends resumes what a restart interrupted. A row left dispatching
// resumes under its id: a store that already happened is found, not repeated.
func (a *Agent) driveQueuedSends() {
	_, err := a.store.db.Exec(`DELETE FROM scheduled_sends WHERE immediate=1 AND (state='sent' OR state='cancelled' AND updated_at<?)`, time.Now().Add(-queuedDiscardedKept).UnixNano())
	if err != nil {
		a.Logf("queued sends: cleanup failed")
	}
	rows, err := a.store.db.Query(`SELECT DISTINCT conv FROM scheduled_sends WHERE immediate=1 AND state IN ('pending','dispatching')`)
	if err != nil {
		a.Logf("queued sends: lookup failed")
		return
	}
	var convs []string
	for rows.Next() {
		var conv string
		if err = rows.Scan(&conv); err != nil {
			break
		}
		convs = append(convs, conv)
	}
	rows.Close()
	for _, conv := range convs {
		a.startQueuedWorker(conv)
	}
}

func (a *Agent) startQueuedWorker(conv string) {
	q := &a.queued
	q.Lock()
	defer q.Unlock()
	if q.closed {
		return
	}
	if wake, running := q.workers[conv]; running {
		select {
		case wake <- struct{}{}:
		default:
		}
		return
	}
	if q.workers == nil {
		q.workers = map[string]chan struct{}{}
		q.lanes = make(chan struct{}, queuedAttemptLanes)
		q.ctx, q.cancel = context.WithCancel(context.Background())
	}
	wake := make(chan struct{}, 1)
	q.workers[conv] = wake
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		a.queuedWorker(q.ctx, conv, wake)
	}()
}

// One conversation's messages leave in the order they were accepted. A message
// waiting on the network holds back only later ones in its own chat; a stopped
// one holds back nothing, as in Telegram. Waiting between attempts follows the
// same 1 s doubling to 45 s as the outbox, and any sign of life ends it early.
func (a *Agent) queuedWorker(ctx context.Context, conv string, wake chan struct{}) {
	q := &a.queued
	wait := time.Second
	budget := q.budget
	if budget == 0 {
		budget = queuedAttemptBudget
	}
	first := budget
	sleep := func() {
		select {
		case <-wake: // spent: only news after this point ends the wait
		default:
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-wake:
		case <-timer.C:
		}
		wait = min(wait*2, sendRetryMax)
	}
	exit := func() bool {
		q.Lock()
		defer q.Unlock()
		select {
		case <-wake:
			return false // something arrived while this worker was leaving
		default:
		}
		delete(q.workers, conv)
		return true
	}
	for {
		if ctx.Err() != nil && exit() {
			return
		}
		id, err := a.nextQueuedSend(conv)
		if err != nil && ctx.Err() == nil {
			a.Logf("queued sends: lookup failed")
			sleep()
			continue
		}
		if id == "" {
			if exit() {
				return
			}
			continue
		}
		select {
		case q.lanes <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		var stored ConvSent
		row, err := a.dispatchScheduledLocked(attempt, id, &stored)
		cancel()
		<-q.lanes
		outcome := queuedOutcome{row: row, err: err}
		if err == nil && stored.LID != "" {
			outcome.sent = &stored
		}
		q.settle(id, outcome)
		if ctx.Err() != nil {
			continue
		}
		switch row.State {
		case ScheduledPending, ScheduledDispatching:
			budget = min(budget*2, queuedAttemptMax)
			sleep()
		case ScheduledSent:
			wait, budget = time.Second, first
			if _, e := a.store.db.Exec(`DELETE FROM scheduled_sends WHERE id=? AND immediate=1 AND state='sent'`, id); e != nil {
				a.Logf("queued sends: cleanup failed")
			}
		default:
			wait, budget = time.Second, first
		}
	}
}

func (a *Agent) nextQueuedSend(conv string) (string, error) {
	var id string
	err := a.store.db.QueryRow(`SELECT id FROM scheduled_sends WHERE immediate=1 AND conv=? AND state IN ('pending','dispatching') ORDER BY created_at,id LIMIT 1`, conv).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// queuedSendAccepted is when the message with this id was accepted, zero when
// it was not queued. The local record keeps that time, so the row does not jump
// when it stops being pending; the copies sent still carry their real send time.
func queuedSendAccepted(tx *sql.Tx, lid string) (time.Time, error) {
	if lid == "" {
		return time.Time{}, nil
	}
	var at int64
	err := tx.QueryRow(`SELECT created_at FROM scheduled_sends WHERE id=? AND immediate=1`, lid).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, at), nil
}

// TechnicalError reports a failure whose text is a diagnostic, not a sentence
// for a person: a request that got no usable answer.
func TechnicalError(err error) bool {
	var address *url.Error
	var network net.Error
	return err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &address) || errors.As(err, &network) || transient(err))
}

// RefusedUntilUpdate reports the relay refusing this build until it is updated.
func RefusedUntilUpdate(err error) bool { return refusedUntilUpdate(err) }

const scheduleReviewWords = " Review the schedule."

// queuedSendWords turns what happened to an attempt into a short sentence for
// the chat and the technical cause for its details. Waiting is not a failure.
func queuedSendWords(state, detail string, cause error) (string, string) {
	diagnostic := ""
	if cause != nil {
		diagnostic = cause.Error()
		if len(diagnostic) > queuedDiagnosticMax {
			diagnostic = diagnostic[:queuedDiagnosticMax]
		}
	}
	if state == ScheduledPending {
		if refusedUntilUpdate(cause) {
			return "This device needs the latest AgentNet. The message goes out after the update.", diagnostic
		}
		return "Waiting to send. AgentNet keeps trying.", diagnostic
	}
	detail = strings.TrimSuffix(detail, scheduleReviewWords)
	if strings.HasPrefix(detail, "The message could not be queued safely") {
		detail = "This message could not be sent."
		if cause != nil && !TechnicalError(cause) {
			words := strings.TrimPrefix(cause.Error(), "cannot be retried: ")
			detail = strings.ToUpper(words[:1]) + words[1:]
			if !strings.ContainsAny(detail[len(detail)-1:], ".!?") {
				detail += "."
			}
		}
	}
	return detail, diagnostic
}
