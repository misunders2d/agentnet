package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Review notices: on a machine where no person sees desktop notifications
// (a server), the owner may name one agent of theirs to be told, without
// content, that items wait for a decision here. The notice is a plain
// message with envelope.StatusReviewNotice; the recipient files it for its
// own person and nothing else. Deciding still happens on this machine.

const reviewToKey = "review_to"

// errNoNewReview aborts a notice whose items were already covered.
var errNoNewReview = errors.New("no review items left to report")

// SetReviewTo names the agent told when items here wait for a decision.
// Only the local user sets it; nothing received can.
func (a *Agent) SetReviewTo(address string) error {
	if _, _, err := protocol.SplitAddress(address); err != nil {
		return err
	}
	if address == a.Address {
		return errors.New("review notices go to another agent, not to this one")
	}
	return a.store.setConfig(map[string]string{reviewToKey: address})
}

// ClearReviewTo stops review notices.
func (a *Agent) ClearReviewTo() error {
	_, err := a.store.db.Exec(`DELETE FROM config WHERE k = ?`, reviewToKey)
	return err
}

// ReviewTo returns the agent told about waiting items, or "".
func (a *Agent) ReviewTo() (string, error) {
	v, err := a.store.config(reviewToKey)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func reviewNoticeDetail(from string) string {
	return "review notice: requests wait for a person's decision on " + from +
		"; decide there (agentnet inbox --review on that machine). Nothing here runs or can be accepted; agentnet resolve ID once seen"
}

// sendReviewNotice tells the review_to agent, once per item, that questions
// or tasks here wait for a decision. Only items that arrived here count,
// never received notices, so two agents naming each other cannot loop. The
// notice is queued in the outbox in the same transaction that marks its
// items, so a crash or retry never sends a second one for them, and a
// failure before queueing leaves them to be reported later. It is tried once
// per item per daemon run; desktop notifications are tracked separately.
func (a *Agent) sendReviewNotice(ctx context.Context) {
	to, err := a.ReviewTo()
	if err != nil || to == "" {
		if err != nil {
			a.Logf("review notice: %v", err)
		}
		return
	}
	if to == a.Address {
		a.Logf("review notice: not sent to this agent itself")
		return
	}
	args := append(append([]any{}, reviewStates...), envelope.KindQuestion, envelope.KindTask)
	rows, err := a.store.db.Query(`SELECT id, review_sent FROM inbox WHERE `+inReview+` AND kind IN (?, ?)`, args...)
	if err != nil {
		a.Logf("review notice: %v", err)
		return
	}
	var ids []string
	total := 0
	for rows.Next() {
		var id string
		var sent bool
		if err := rows.Scan(&id, &sent); err != nil {
			rows.Close()
			a.Logf("review notice: %v", err)
			return
		}
		total++
		if !sent && !a.reviewTried[id] {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return
	}
	if a.reviewTried == nil {
		a.reviewTried = map[string]bool{}
	}
	for _, id := range ids {
		a.reviewTried[id] = true
	}
	claim := func(tx *sql.Tx, _ string) error {
		marks := append([]any{}, reviewStates...)
		for _, id := range ids {
			marks = append(marks, id)
		}
		res, err := tx.Exec(`UPDATE inbox SET review_sent = 1 WHERE review_sent = 0 AND `+inReview+
			` AND id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, marks...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errNoNewReview
		}
		return nil
	}
	body := fmt.Sprintf("%d request(s) wait for a person's decision on %s. Review there: agentnet inbox --review", total, a.Address)
	if _, err := a.SendMessage(ctx, Outgoing{To: to, Kind: envelope.KindMessage, Status: envelope.StatusReviewNotice, Body: body, claim: claim}); err != nil && !errors.Is(err, errNoNewReview) {
		a.Logf("review notice to %s not queued (%v); %d item(s) wait: see `agentnet inbox --review`", to, err, total)
	}
}
