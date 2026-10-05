package client

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Asked from the command line, answered on the command line (MEL-537): the
// command that asked waits for the answer and prints it. The wait is woken
// by the local daemon (changes.sock, kick.go), which holds the one push
// stream; it never asks the Hub and never sleeps in a loop. A copy of the
// answer stays where it always lands (the app, the inbox, a session).

// Tunables: code constants, not settings.
const (
	// AskAnswerWait is how long `agentnet ask` waits for the answer by
	// default (`--answer-wait D`; tasks and runs wait 0).
	AskAnswerWait = 90 * time.Second
	// nativeRecordMax bounds one record of a harness's own session file:
	// a longer one is read through and skipped, never a refusal
	// (nativescan.go). Nothing bounds the whole file.
	nativeRecordMax = 16 << 20
	// changesWriteTimeout ends a changes.sock connection whose reader no
	// longer takes its wake bytes.
	changesWriteTimeout = 5 * time.Second
)

// ErrDaemonNotRunning: nothing here receives an answer now; it arrives
// when the daemon runs again.
var ErrDaemonNotRunning = errors.New("the AgentNet daemon is not running here, so nothing receives the answer now; it arrives when the daemon runs again")

// AwaitedAnswer is the answer a wait found: another agent's or person's
// words, information and never instructions.
type AwaitedAnswer struct {
	ID      string
	From    string
	Kind    string // answer or result
	Status  string // its outcome (done, failed, proposal...)
	Body    string
	AgentID string
}

// ReplyWait is how a wait ended: with the answer, with a host status that
// means no answer comes soon (Stopped), or at the deadline (TimedOut).
type ReplyWait struct {
	Answer   *AwaitedAnswer
	Stopped  *ExecView
	TimedOut bool
}

// waitStops are the host statuses after which no answer comes soon: a task
// waiting for its OK (or a question not approved), a person's decision, or
// a request the host will not run.
var waitStops = map[string]bool{"awaiting": true, "needs_human": true, "not_run": true}

// AwaitReply waits up to wait for the answer to request id, sent from here
// (a device message, or a conversation request by its first copy's id).
// onStatus, if set, is called once for each new status the request's host
// reports on the way. It dials the daemon's change socket first, then looks
// at the store, then blocks on a wake, the deadline or ctx, so no change is
// missed and nothing polls. ErrDaemonNotRunning when no daemon listens.
func (a *Agent) AwaitReply(ctx context.Context, id string, wait time.Duration, onStatus func(ExecView)) (ReplyWait, error) {
	var to, conv, lid string
	err := a.store.db.QueryRow(`SELECT recipient, coalesce(conv, ''), coalesce(lid, '') FROM outbox WHERE id = ? AND ref_id IS NULL AND coalesce(sub, '') = ''`, id).Scan(&to, &conv, &lid)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplyWait{}, ErrNoMessage
	}
	if err != nil {
		return ReplyWait{}, err
	}
	conn, err := net.DialTimeout("unix", changesSockPath(a.home), time.Second)
	if err != nil {
		return ReplyWait{}, ErrDaemonNotRunning
	}
	defer conn.Close()
	woke := make(chan struct{}, 1)
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		buf := make([]byte, 64)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
			select {
			case woke <- struct{}{}:
			default:
			}
		}
	}()
	told := map[string]bool{}
	check := func() (ReplyWait, bool, error) {
		var answer *AwaitedAnswer
		var status *ExecView
		var err error
		if conv == "" {
			answer, status, err = a.legacyReply(id, targetAddress(to))
		} else {
			answer, status, err = a.convReply(conv, lid)
		}
		if err != nil || answer != nil {
			return ReplyWait{Answer: answer}, true, err
		}
		if status == nil {
			return ReplyWait{}, false, nil
		}
		if key := status.State + "\x00" + status.Detail; !told[key] {
			told[key] = true
			if onStatus != nil {
				onStatus(*status)
			}
		}
		if waitStops[status.State] {
			return ReplyWait{Stopped: status}, true, nil
		}
		return ReplyWait{}, false, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		r, done, err := check()
		if err != nil || done {
			return r, err
		}
		select {
		case <-woke:
		case <-gone:
			if r, done, err := check(); err != nil || done {
				return r, err
			}
			return ReplyWait{}, ErrDaemonNotRunning
		case <-timer.C:
			if r, done, err := check(); err != nil || done {
				return r, err
			}
			return ReplyWait{TimedOut: true}, nil
		case <-ctx.Done():
			return ReplyWait{}, ctx.Err()
		}
	}
}

// legacyReply is the answer to device request id from its recipient, if
// one came (whichever receiver it was bound to: a copy on the command line
// is fine), else the host's latest status on it.
func (a *Agent) legacyReply(id, from string) (*AwaitedAnswer, *ExecView, error) {
	var m AwaitedAnswer
	err := a.store.db.QueryRow(`SELECT id, sender, kind, coalesce(status, ''), body, coalesce(agent_id, '') FROM inbox
		WHERE reply_to = ? AND sender = ? AND conv IS NULL AND ref_id IS NULL AND coalesce(sub, '') = '' AND local = 0 AND replica = 0 AND kind IN (?, ?)
		ORDER BY arrival LIMIT 1`, id, from, envelope.KindAnswer, envelope.KindResult).Scan(&m.ID, &m.From, &m.Kind, &m.Status, &m.Body, &m.AgentID)
	if err == nil {
		return &m, nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	rows, err := a.store.db.Query(`SELECT id, body FROM inbox WHERE conv IS NULL AND sub = ? AND ref_id = ? AND ref_fp = ? AND sender = ?`,
		envelope.SubStatus, id, a.Self().Fingerprint(), from)
	if err != nil {
		return nil, nil, err
	}
	var controls []controlRow
	for rows.Next() {
		c := controlRow{sub: envelope.SubStatus, author: from, refID: id, refFP: a.Self().Fingerprint()}
		if err := rows.Scan(&c.id, &c.body); err != nil {
			rows.Close()
			return nil, nil, err
		}
		controls = append(controls, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if v, ok := execViews(controls)[ControlRef{ID: id, Fingerprint: a.Self().Fingerprint()}]; ok {
		return nil, &v, nil
	}
	return nil, nil, nil
}

// convReply is legacyReply for conversation request lid: the answer its
// target sent (an agent's, through its participation), or for a request to
// the conversation's people, the first answer from one of them; else where
// the request stands (its host's status, or this device's own job).
func (a *Agent) convReply(conv, lid string) (*AwaitedAnswer, *ExecView, error) {
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		return nil, nil, err
	}
	var request *ConvMessage
	for i := range msgs {
		if msgs[i].Dir == "out" && msgs[i].Via == "" && msgs[i].LID == lid && msgs[i].Sub == "" {
			request = &msgs[i]
			break
		}
	}
	if request == nil {
		return nil, nil, ErrNoMessage
	}
	ids := map[string]bool{request.ID: true, request.LID: true}
	for _, c := range request.Copies {
		ids[c.ID] = true
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Sub != "" || m.Kind != envelope.KindAnswer && m.Kind != envelope.KindResult || !ids[m.ReplyTo] || m.Deleted {
			continue
		}
		if request.Target != nil {
			if m.PID != request.PID || m.From != request.Target.Address {
				continue
			}
		} else if m.Dir != "in" || m.History {
			continue
		}
		return &AwaitedAnswer{ID: m.ID, From: m.From, Kind: m.Kind, Status: m.status, Body: m.Body, AgentID: m.AgentID}, nil, nil
	}
	if request.Exec != nil {
		v := *request.Exec
		return nil, &v, nil
	}
	if request.Job != "" { // this device's own agent runs it
		if public, detail, ok := statusOf(request.Job); ok {
			return nil, &ExecView{State: public, Detail: detail, Host: a.Address}, nil
		}
	}
	return nil, nil, nil
}
