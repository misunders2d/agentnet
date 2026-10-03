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
	"github.com/misunders2d/agentnet/internal/protocol"
)

// SendProgress sends a nonterminal update in an existing request's own
// thread: a version 1 reply, or the participation's output in its
// conversation. The stored request binds peer, executor and participation;
// nothing is taken from the caller but the text.
func (a *Agent) SendProgress(ctx context.Context, to, replyTo, body string, wait time.Duration, fallback bool, files ...string) (SendResult, error) {
	if strings.TrimSpace(body) == "" {
		return SendResult{}, errors.New("progress update text is empty")
	}
	if len(files) != 0 {
		return SendResult{}, errors.New("progress updates cannot attach files")
	}
	req, err := a.store.progressRequest(replyTo)
	if err != nil {
		return SendResult{}, err
	}
	if req.conv != "" {
		return a.sendConvProgress(ctx, to, req, body)
	}
	if err := a.CheckReplyTo(replyTo, to); err != nil {
		return SendResult{}, err
	}
	agentID := ""
	if req.target != nil {
		if req.target.Address != a.Address || req.target.Fingerprint != a.Self().Fingerprint() {
			return SendResult{}, errors.New("progress speaks only for this device's own named executor")
		}
		agentID = req.target.AgentID
	}
	return a.SendMessage(ctx, Outgoing{To: to, Body: body, ReplyTo: replyTo, Fallback: fallback, Wait: wait,
		Kind: envelope.KindMessage, Status: envelope.StatusProgress, AgentID: agentID})
}

// sendConvProgress sends progress as the participation's nonterminal output:
// the same request, PID, agent and audience as its answer, decided like it.
func (a *Agent) sendConvProgress(ctx context.Context, to string, req progressReq, body string) (SendResult, error) {
	addr, _, err := protocol.SplitTarget(to)
	if err != nil {
		return SendResult{}, err
	}
	if addr != req.sender {
		return SendResult{}, fmt.Errorf("request %s is from %s, not %s", req.id, req.sender, addr)
	}
	if req.pid == "" || req.target == nil {
		return SendResult{}, ErrConversationItem
	}
	j := job{ID: req.id, From: req.sender, Key: req.key, Kind: req.kind, Conv: req.conv, PID: req.pid, Target: req.target, AgentID: req.target.AgentID}
	selfFP := a.Self().Fingerprint()
	claim := func(tx *sql.Tx, _ string) error {
		v, why, err := agentVerdict(tx, j.agentReq(stateRunning), a.Address, selfFP, true, map[string]*partView{})
		if err != nil {
			return err
		}
		if v != verdictRun {
			return &heldBack{why}
		}
		return nil
	}
	harness := req.responder
	if harness == "" {
		harness = "agent"
	}
	res, err := a.SendConv(ctx, req.conv, ConvOutgoing{Kind: envelope.KindMessage, Body: body, ReplyTo: req.id, Origin: envelope.OriginAgentPrefix + harness,
		AgentID: j.AgentID, PID: req.pid, status: envelope.StatusProgress, Emotion: "neutral", claim: claim}) // agent turns carry an emotion; progress claims none
	var hb *heldBack
	if errors.As(err, &hb) {
		return SendResult{}, errors.New("progress not sent: " + hb.why)
	}
	if err != nil {
		return SendResult{ID: res.ID, State: res.State}, err
	}
	return SendResult{ID: res.ID, State: res.State, Detail: res.Detail}, nil
}

func isResponderProgress(in envelope.Inner) bool {
	return in.Kind == envelope.KindMessage && in.Status == envelope.StatusProgress && in.ReplyTo != ""
}

type progressReq struct {
	id, sender, key, kind, conv, pid, responder string
	target                                      *envelope.Target
}

// progressRequest allows progress only on a question or task this device
// received to run: version 1 (its default responder or a named executor
// targeting this device), or a conversation participation's request. Plain
// conversation items, local, replica or other messages have no progress.
func (s *store) progressRequest(id string) (progressReq, error) {
	r := progressReq{id: id}
	var conv, pid, target, responder sql.NullString
	var local, replica bool
	err := s.db.QueryRow(`SELECT sender, coalesce(verified_by,''), kind, conv, pid, target, responder, local, replica FROM inbox WHERE id=?`, id).
		Scan(&r.sender, &r.key, &r.kind, &conv, &pid, &target, &responder, &local, &replica)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("no message %s here to reply to", id)
	}
	if err != nil {
		return r, err
	}
	if (r.kind != envelope.KindQuestion && r.kind != envelope.KindTask) || local || replica {
		return r, errors.New("progress updates only a question or task sent here")
	}
	r.conv, r.pid, r.responder = conv.String, pid.String, responder.String
	if target.String != "" { // a version 1 target always names its executor
		r.target = &envelope.Target{}
		if json.Unmarshal([]byte(target.String), r.target) != nil || r.conv == "" && r.target.AgentID == "" {
			return r, errors.New("progress request has an invalid executor")
		}
	}
	if r.conv != "" && (r.pid == "" || r.target == nil) {
		return r, ErrConversationItem // a conversation turn for its person, never progress
	}
	return r, nil
}
