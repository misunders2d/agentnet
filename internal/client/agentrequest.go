package client

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// AgentRequest identifies an already stored request. Reading it never sends,
// requeues, accepts or executes work.
type AgentRequest struct {
	Conv string
	PID  string
	Sent ConvSent
}

var ErrAgentRequestConflict = errors.New("send id belongs to a different stored request; use its original participation, kind, text and reply receiver")

type retainedAgentRequest struct {
	AgentRequest
	kind, body, binding string
	files               int
}

func (a *Agent) retainedAgentRequest(ref string) (retainedAgentRequest, error) {
	var r retainedAgentRequest
	if !protocol.ValidID(ref) {
		return r, errors.New("invalid request id")
	}
	err := a.store.db.QueryRow(`SELECT conv,lid FROM outbox WHERE (id=? OR lid=?) AND conv IS NOT NULL AND ref_id IS NULL AND coalesce(sub,'')='' ORDER BY rowid LIMIT 1`, ref, ref).Scan(&r.Conv, &r.Sent.LID)
	if errors.Is(err, sql.ErrNoRows) {
		err = a.store.db.QueryRow(`SELECT conv,lid FROM inbox WHERE (id=? OR lid=?) AND sender=? AND local=1 AND conv IS NOT NULL AND ref_id IS NULL AND coalesce(sub,'')='' ORDER BY rowid LIMIT 1`, ref, ref, a.Address).Scan(&r.Conv, &r.Sent.LID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNoMessage
	}
	if err != nil {
		return r, err
	}
	err = a.store.db.QueryRow(`SELECT id,coalesce(pid,''),kind,body,coalesce((SELECT reply_receiver FROM outbox x WHERE x.conv=o.conv AND x.lid=o.lid AND x.reply_receiver IS NOT NULL LIMIT 1),''),(SELECT count(*) FROM sent_attachments WHERE message_id=o.id) FROM outbox o WHERE conv=? AND lid=? AND ref_id IS NULL AND coalesce(sub,'')='' ORDER BY rowid LIMIT 1`, r.Conv, r.Sent.LID).Scan(&r.Sent.ID, &r.PID, &r.kind, &r.body, &r.binding, &r.files)
	if errors.Is(err, sql.ErrNoRows) {
		err = a.store.db.QueryRow(`SELECT id,coalesce(pid,''),kind,body,coalesce((SELECT id FROM reply_receivers WHERE conv=i.conv AND request_ref=i.lid LIMIT 1),''),(SELECT count(*) FROM attachments WHERE message_id=i.id)+(SELECT count(*) FROM sent_attachments WHERE message_id=i.id) FROM inbox i WHERE conv=? AND lid=? AND sender=? AND local=1 AND ref_id IS NULL AND coalesce(sub,'')='' LIMIT 1`, r.Conv, r.Sent.LID, a.Address).Scan(&r.Sent.ID, &r.PID, &r.kind, &r.body, &r.binding, &r.files)
	}
	if err != nil {
		return r, err
	}
	if r.PID == "" || r.kind != envelope.KindQuestion && r.kind != envelope.KindTask {
		return r, ErrAgentRequestConflict
	}
	r.Sent.Copies, err = a.SentCopies(r.Sent.LID)
	if err != nil {
		return r, err
	}
	r.Sent.State = protocol.StateDelivered
	for _, c := range r.Sent.Copies {
		if rank(c.State) < rank(r.Sent.State) {
			r.Sent.State, r.Sent.Detail = c.State, c.Detail
		}
	}
	return r, nil
}

func (a *Agent) AgentRequest(ref string) (AgentRequest, error) {
	r, err := a.retainedAgentRequest(ref)
	return r.AgentRequest, err
}

func requestReceiver(r *ReplyReceiver) *ReplyReceiver {
	if r == nil {
		return nil
	}
	copy := *r
	copy.BindingID, copy.Preset = "", ""
	copy.Instructions = strings.TrimSpace(copy.Instructions)
	if copy.OnClose != nil {
		onClose := *copy.OnClose
		onClose.Instructions = strings.TrimSpace(onClose.Instructions)
		copy.OnClose = &onClose
	}
	return &copy
}

func (a *Agent) sameAgentRequest(r retainedAgentRequest, pid, kind, body string, receiver *ReplyReceiver) bool {
	if r.PID != pid || r.kind != kind || r.body != body || r.files != 0 {
		return false
	}
	var stored *ReplyReceiver
	if r.binding != "" {
		binding, err := replyReceiverIn(a.store.db, r.binding)
		if err != nil {
			return false
		}
		stored = &binding.Receiver
	}
	if receiver != nil && (receiver.BindingID != "" && receiver.BindingID != r.binding || receiver.Preset != "" && (stored == nil || receiver.Preset != stored.Preset)) {
		return false
	}
	return reflect.DeepEqual(requestReceiver(receiver), requestReceiver(stored))
}

// QueueAgentRequest returns promptly after the existing durable send. Exact
// retries recover its stored IDs; changed input cannot retarget a retained ID.
// This bounded CLI path has no attachment inputs; file-bearing IDs are refused.
func (a *Agent) QueueAgentRequest(ctx context.Context, id, pid, kind, body string, receiver *ReplyReceiver) (ConvSent, error) {
	if !protocol.ValidID(id) {
		return ConvSent{}, errors.New("invalid local send id")
	}
	recover := func() (ConvSent, error) {
		r, err := a.retainedAgentRequest(id)
		if err != nil {
			return ConvSent{}, err
		}
		if !a.sameAgentRequest(r, pid, kind, body, receiver) {
			return ConvSent{}, ErrAgentRequestConflict
		}
		return r.Sent, nil
	}
	if sent, err := recover(); !errors.Is(err, ErrNoMessage) {
		return sent, err
	}
	sent, err := a.AskAgentWithReceiver(WithQueuedSend(ctx, id), pid, kind, body, receiver)
	if err != nil {
		// A concurrent exact submit may have committed while this one was
		// preparing its normal authority-checked send. Read; never resend.
		if prior, recovered := recover(); !errors.Is(recovered, ErrNoMessage) {
			return prior, recovered
		}
	}
	return sent, err
}

// AwaitAgentRequest accepts a retained physical or logical ID. It uses the
// daemon's existing push wake; a reply already stored needs no running daemon.
func (a *Agent) AwaitAgentRequest(ctx context.Context, ref string, wait time.Duration, onStatus func(ExecView)) (ReplyWait, error) {
	r, err := a.retainedAgentRequest(ref)
	if err != nil {
		return ReplyWait{}, err
	}
	result, err := a.AwaitReply(ctx, r.Sent.ID, wait, onStatus)
	if errors.Is(err, ErrDaemonNotRunning) || errors.Is(err, ErrNoMessage) {
		answer, status, readErr := a.convReply(r.Conv, r.Sent.LID)
		if readErr != nil {
			return ReplyWait{}, readErr
		}
		if answer != nil {
			return ReplyWait{Answer: answer}, nil
		}
		if status != nil && waitStops[status.State] {
			return ReplyWait{Stopped: status}, nil
		}
	}
	return result, err
}
