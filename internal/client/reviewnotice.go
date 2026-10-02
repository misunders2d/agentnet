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

// Review notices: on a machine where no person sees desktop notifications
// (a server), the owner may name one agent of theirs to be told, without
// content, that items wait for a decision here. The notice is a plain
// message with envelope.StatusReviewNotice; the recipient files it for its
// own person and nothing else. Deciding still happens on this machine.

const (
	reviewToKey    = "review_to"
	reviewToGenKey = "review_to_gen" // changes on every set or clear
)

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
	if err := a.store.setConfig(map[string]string{reviewToKey: address, reviewToGenKey: protocol.NewID()}); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}

// ClearReviewTo stops review notices.
func (a *Agent) ClearReviewTo() error {
	if _, err := a.store.db.Exec(`DELETE FROM config WHERE k = ?`, reviewToKey); err != nil {
		return err
	}
	if err := a.store.setConfig(map[string]string{reviewToGenKey: protocol.NewID()}); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
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

// sendReviewNotice tells the review_to agent and every granted operator
// (operators.go), once per item, that items here wait for a decision.
// Received notices never count, so two agents naming each other cannot
// loop. The first notice is queued in the outbox in the same transaction
// that marks its items, so a crash or retry never sends a second one for
// them, and a failure before queueing leaves them to be reported later:
// tried once per item per daemon run and per setting, never on every ping.
// A recipient that reads reports (protocol.CapHeadless) gets the requests
// named (headless.go Report), with their first lines only if it is a
// granted operator; anyone else gets the count. Desktop notifications are
// tracked separately.
func (a *Agent) sendReviewNotice(ctx context.Context) {
	gen, err := a.store.config(reviewToGenKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.Logf("review notice: %v", err)
		return
	}
	to, err := a.ReviewTo()
	if err != nil {
		a.Logf("review notice: %v", err)
		return
	}
	operators, err := a.store.activeOperators()
	if err != nil {
		a.Logf("review notice: %v", err)
		return
	}
	var recipients []string
	isOperator := map[string]bool{}
	for _, addr := range operators {
		if addr != a.Address {
			recipients, isOperator[addr] = append(recipients, addr), true
		}
	}
	if to != "" && to != a.Address && !isOperator[to] {
		recipients = append(recipients, to)
	}
	if len(recipients) == 0 {
		return
	}
	// A new setting (e.g. a corrected address) retries items that failed
	// under the old one, once, without waiting for a restart.
	if gen != a.reviewGen {
		a.reviewGen, a.reviewTried = gen, nil
	}
	if a.reviewTried == nil {
		a.reviewTried = map[string]bool{}
	}
	// Every item waiting here counts, including follow-ups your responder
	// marked needs_human, except review notices received from others and
	// a person's DM turns (alertReviewStates: they follow the DM's alerts).
	args := append(append([]any{}, alertReviewStates...), envelope.KindMessage, envelope.StatusReviewNotice)
	rows, err := a.store.db.Query(`SELECT id, sender, coalesce(verified_by, ''), kind, state, received_at, attempts, body FROM inbox WHERE `+inAlertReview+` AND NOT (`+receivedNotice+`) ORDER BY received_at, id`, args...)
	if err != nil {
		a.Logf("review notice: %v", err)
		return
	}
	var items []ReportItem
	for rows.Next() {
		var it ReportItem
		if err := rows.Scan(&it.ID, &it.From, &it.Key, &it.Kind, &it.State, &it.Since, &it.Attempt, &it.Excerpt); err != nil {
			rows.Close()
			a.Logf("review notice: %v", err)
			return
		}
		it.Blocker, it.Excerpt = blockerOf(it.Kind, it.State), firstLine(it.Excerpt)
		items = append(items, it)
	}
	rows.Close()
	if len(items) == 0 {
		return
	}
	feats, ferr := a.relayFeatures(ctx)
	countText := fmt.Sprintf("%d request(s) wait for a person's decision on %s. Review there: agentnet inbox --review", len(items), a.Address)
	for _, to := range recipients {
		// Each recipient is told once per item (reported): a recipient
		// added later gets what waits now, and one that could not be told
		// keeps its items pending, whatever happened with the others.
		var ids []string
		for _, it := range items {
			var n int
			a.store.db.QueryRow(`SELECT count(*) FROM reported WHERE item = ? AND recipient = ?`, it.ID, to).Scan(&n)
			if n == 0 && !a.reviewTried[to+"\x00"+it.ID] {
				ids = append(ids, it.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		for _, id := range ids {
			a.reviewTried[to+"\x00"+id] = true
		}
		// The requests themselves go only to a granted operator that reads
		// reports; the review destination alone learns the count and no
		// more, as before (no identity or request state by accident).
		body := countText
		reportKey := ""
		if isOperator[to] && ferr == nil {
			if key, err := a.sendKey(ctx, to); err == nil {
				if ok, _ := a.capSupport(ctx, to, key, feats, protocol.CapHeadless); ok {
					body = a.reportBody(items)
					reportKey = key.Fingerprint()
				}
			}
		}
		claim := func(tx *sql.Tx, _ string) error {
			// A grant may have changed while this snapshot was prepared. A
			// stale count must not reclaim marks the promotion just cleared.
			var currentGen string
			if err := tx.QueryRow(`SELECT coalesce((SELECT v FROM config WHERE k = ?), '')`, reviewToGenKey).Scan(&currentGen); err != nil {
				return err
			}
			if currentGen != gen {
				return errNoNewReview
			}
			if reportKey != "" {
				active, err := operatorHolds(tx, to, reportKey)
				if err != nil {
					return err
				}
				if !active {
					return errNoNewReview
				}
			}
			now := time.Now().Unix()
			fresh := 0
			for _, id := range ids {
				res, err := tx.Exec(`INSERT OR IGNORE INTO reported(item, recipient, sent_at) VALUES(?, ?, ?)`, id, to, now)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 1 {
					fresh++
				}
			}
			if fresh == 0 {
				return errNoNewReview
			}
			// The older flag, for the review destination's own record.
			marks := append([]any{}, alertReviewStates...)
			for _, id := range ids {
				marks = append(marks, id)
			}
			_, err := tx.Exec(`UPDATE inbox SET review_sent = 1 WHERE `+inAlertReview+` AND id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, marks...)
			return err
		}
		if _, err := a.SendMessage(ctx, Outgoing{To: to, Kind: envelope.KindMessage, Status: envelope.StatusReviewNotice, Body: body, claim: claim}); err != nil {
			if !errors.Is(err, errNoNewReview) {
				a.Logf("review notice to %s not queued (%v); %d item(s) wait: see `agentnet inbox --review`", to, err, len(items))
			}
			continue
		}
		for _, id := range ids {
			delete(a.reviewTried, to+"\x00"+id) // reported; a later return to review is new
		}
	}
}

// reportBody is a version 2 report of items for a granted operator: the
// requests named, with their first lines, each actionable from there.
func (a *Agent) reportBody(items []ReportItem) string {
	r := Report{V: 2, At: time.Now().Unix(), Host: a.Address, Items: make([]ReportItem, 0, len(items))}
	for _, it := range items {
		it.Actionable = true
		r.Items = append(r.Items, it)
	}
	data, _ := json.Marshal(r)
	return string(data)
}

// receivedNotice matches exactly the rows isReviewNotice files (kind, status,
// no reply_to, no files); its two parameters are KindMessage and
// StatusReviewNotice.
const receivedNotice = `kind = ? AND status IS ? AND reply_to IS NULL AND NOT EXISTS (SELECT 1 FROM attachments WHERE message_id = inbox.id)`
