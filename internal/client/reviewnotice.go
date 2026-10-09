package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
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
	reviewToKey     = "review_to"
	reviewToGenKey  = "review_to_gen"    // changes on every set or clear
	reviewToFailKey = "review_to_failed" // why the last notice to review_to could not be queued ("" or absent: none failed since)
)

// errNoNewReview aborts a notice whose items were already covered.
var errNoNewReview = errors.New("no review items left to report")

// SetReviewTo names the agent told when items here wait for a decision.
// Only the local user sets it; nothing received can. The Hub's directory
// must list address as an agent that is not revoked: a notice to any other
// address could never arrive, so it is refused, and nothing changes while
// the Hub cannot be asked.
func (a *Agent) SetReviewTo(ctx context.Context, address string) error {
	if _, _, err := protocol.SplitAddress(address); err != nil {
		return err
	}
	if address == a.Address {
		return errors.New("review notices go to another agent, not to this one")
	}
	e, err := a.directory(ctx, address)
	var he *HubError
	switch {
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		return fmt.Errorf("%s is not an agent on your Hub (agentnet members lists them): nothing changed", address)
	case errors.Is(err, ErrRevoked) || err == nil && e.Revoked:
		return fmt.Errorf("%s was revoked on your Hub: notices to it would never arrive; nothing changed", address)
	case err != nil:
		return fmt.Errorf("cannot check %s on your Hub now (%v): nothing changed; try again when it is reachable", address, err)
	}
	if err := a.store.setConfig(map[string]string{reviewToKey: address, reviewToGenKey: protocol.NewID(), reviewToFailKey: ""}); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}

// ClearReviewTo stops review notices.
func (a *Agent) ClearReviewTo() error {
	if _, err := a.store.db.Exec(`DELETE FROM config WHERE k IN (?, ?)`, reviewToKey, reviewToFailKey); err != nil {
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
		"; its report names them for a steward's devices, or says who decides them. Nothing here runs or can be accepted; agentnet resolve ID once seen"
}

// sendReviewNotice tells the review_to agent and every granted operator
// (operators.go: granted devices and each steward person's current
// devices), once per item, that items here wait for a decision. Received
// notices never count, so two agents naming each other cannot loop. The
// first notice is queued in the outbox in the same transaction that marks
// its items, so a crash or retry never sends a second one for them, and a
// failure before queueing leaves them to be reported later: tried once per
// item per daemon run and per setting, never on every ping. A recipient
// that reads reports (protocol.CapHeadless) gets a version 2 report
// (headless.go Report): an operator the requests named, with their first
// lines; anyone else how many wait and who can decide them (Deciders),
// never more. Each report is a snapshot of everything waiting now, so the
// newest replaces the older ones on arrival; once the items a recipient was
// told about have all left review, it gets one more snapshot, empty if
// nothing waits, which clears its card (no timer). Desktop notifications
// are tracked separately.
func (a *Agent) sendReviewNotice(ctx context.Context) {
	// The worker's pass and one during a run (worker.go) never overlap: a
	// pass skipped here is covered by the worker's next one, after the run.
	if !a.reviewMu.TryLock() {
		return
	}
	defer a.reviewMu.Unlock()
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
	isOwner := map[string]bool{}
	for _, addr := range operators {
		if addr != a.Address {
			recipients, isOperator[addr] = append(recipients, addr), true
		}
	}
	// Reuse encrypted reports for the person's current devices. This is
	// visibility of the agent's needs-you text, never an operator grant.
	if me, ok, err := a.store.selfPerson(a.Address); err != nil {
		a.Logf("review notice: %v", err)
		return
	} else if ok {
		for _, d := range me.info.Devices {
			if d.Address != a.Address {
				isOwner[d.Address] = true
				if !slices.Contains(recipients, d.Address) {
					recipients = append(recipients, d.Address)
				}
			}
		}
	}
	if to != "" && to != a.Address && !slices.Contains(recipients, to) {
		recipients = append(recipients, to)
	}
	// A recipient told about items earlier gets its settled snapshot even
	// after its grant or the setting changed (its card must not stay
	// behind), but nothing new.
	current := map[string]bool{}
	for _, r := range recipients {
		current[r] = true
	}
	if rows, err := a.store.db.Query(`SELECT DISTINCT recipient FROM reported ORDER BY recipient`); err == nil {
		for rows.Next() {
			var r string
			if rows.Scan(&r) == nil && r != a.Address && !slices.Contains(recipients, r) {
				recipients = append(recipients, r)
			}
		}
		rows.Close()
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
	since := a.reviewAgain.begin()
	for _, to := range a.reviewAgain.take() { // skipped devices a member list event since may concern
		for k := range a.reviewTried {
			if strings.HasPrefix(k, to+"\x00") {
				delete(a.reviewTried, k)
			}
		}
	}
	// Every item waiting here counts, including follow-ups your responder
	// marked needs_human, except review notices received from others and
	// a person's DM turns (alertReviewStates: they follow the DM's alerts).
	args := append(append([]any{}, alertReviewStates...), envelope.KindMessage, envelope.StatusReviewNotice)
	rows, err := a.store.db.Query(`SELECT id, sender, coalesce(verified_by, ''), kind, state, CASE WHEN state='running' THEN last_attempt_at ELSE received_at END, attempts, body, conv IS NOT NULL, coalesce(detail,'') FROM inbox WHERE `+inAlertReview+` AND NOT (`+receivedNotice+`) ORDER BY received_at, id`, args...)
	if err != nil {
		a.Logf("review notice: %v", err)
		return
	}
	var items []ReportItem
	agentText := map[string]string{}
	for rows.Next() {
		var it ReportItem
		var detail string
		if err := rows.Scan(&it.ID, &it.From, &it.Key, &it.Kind, &it.State, &it.Since, &it.Attempt, &it.Excerpt, &it.Conv, &detail); err != nil {
			rows.Close()
			a.Logf("review notice: %v", err)
			return
		}
		it.Blocker, it.Excerpt = blockerOf(it.Kind, it.State), firstLine(it.Excerpt)
		if it.State == stateNeedHuman {
			agentText[it.ID] = detail
		}
		if it.State == stateRunning {
			it.Excerpt = detail
			if strings.HasPrefix(detail, "Seems stuck:") {
				it.Blocker = BlockerStalled
			}
		}
		items = append(items, it)
	}
	rows.Close()
	for i := range items { // what a task carries out, for the operator approving it (MEL-521)
		if items[i].Excerpt == "" {
			if files, err := a.store.attachments(items[i].ID); err == nil && len(files) > 0 {
				items[i].Excerpt = reviewFileExcerpt(files)
			}
		}
		if items[i].Kind == envelope.KindTask && !items[i].Conv {
			items[i].Proposal, _ = a.ProposalOf(items[i].ID)
			if p := items[i].Proposal; p != nil {
				p.Question, p.Proposal, p.Task = firstLine(p.Question), firstLine(p.Proposal), firstLine(p.Task)
			}
		}
	}
	type plan struct {
		to         string
		list       []ReportItem
		ids, stale []string
		left       string // reviewTried key of the settled snapshot
	}
	var plans []plan
	for _, to := range recipients {
		list := items
		if isOwner[to] && !isOperator[to] {
			list = slices.DeleteFunc(slices.Clone(items), func(it ReportItem) bool { return it.State != stateNeedHuman })
		}
		if isOperator[to] {
			// An operator device of an older version (v0.6.2) drops a whole
			// report naming an interrupted request, and with it the alert
			// for everything else waiting here: until operators have
			// updated, they are not told of those (review here lists them).
			list = slices.DeleteFunc(slices.Clone(items), func(it ReportItem) bool { return it.State == stateInterrupt })
		}
		// Each recipient is told once per item (reported): a recipient
		// added later gets what waits now, and one that could not be told
		// keeps its items pending, whatever happened with the others.
		p := plan{to: to, list: list}
		listed := map[string]bool{}
		for _, it := range list {
			listed[it.ID] = true
			var n int
			a.store.db.QueryRow(`SELECT count(*) FROM reported WHERE item = ? AND recipient = ?`, it.ID, to).Scan(&n)
			if n == 0 && current[to] && !a.reviewTried[to+"\x00"+it.ID] {
				p.ids = append(p.ids, it.ID)
			}
		}
		// Items this recipient was told about that left its list since: a
		// fresh snapshot settles its card (settled snapshot).
		if told, err := a.store.db.Query(`SELECT item FROM reported WHERE recipient = ? ORDER BY item`, to); err == nil {
			for told.Next() {
				var id string
				if told.Scan(&id) == nil && !listed[id] {
					p.stale = append(p.stale, id)
				}
			}
			told.Close()
		}
		if len(p.stale) > 0 {
			p.left = to + "\x00left\x00" + strings.Join(p.stale, ",")
			if a.reviewTried[p.left] {
				p.stale, p.left = nil, ""
			}
		}
		if len(p.ids) > 0 || len(p.stale) > 0 {
			plans = append(plans, p)
		}
	}
	if len(plans) == 0 {
		return
	}
	feats, ferr := a.relayFeatures(ctx)
	var deciders []Decider
	for _, p := range plans {
		to, list, ids := p.to, p.list, p.ids
		// The requests themselves go only to a granted operator that reads
		// reports; current own devices also get read-only needs-you text. Other
		// recipients learn how many wait and who decides them
		// (no identity or request state by accident).
		body := countText(len(list), a.Address)
		reportKey := ""
		fullText := false
		capable := false
		if ferr == nil {
			if key, err := a.sendKey(ctx, to); err == nil {
				if ok, _ := a.capSupport(ctx, to, key, feats, protocol.CapHeadless); ok {
					capable = true
					if isOperator[to] || isOwner[to] {
						reportKey = key.Fingerprint()
						owner, err := ownerReportHolds(a.store.db, to, reportKey)
						if err != nil {
							a.Logf("review notice: %v", err)
							return
						}
						steward, err := stewardHolds(a.store.db, to, reportKey)
						if err != nil {
							a.Logf("review notice: %v", err)
							return
						}
						fullText = owner || steward
						body = a.reportBodyFor(list, agentText, fullText, isOperator[to])
					} else {
						if deciders == nil {
							if deciders, err = a.store.deciders(); err != nil {
								a.Logf("review notice: %v", err)
								return
							}
							if deciders == nil {
								deciders = []Decider{}
							}
						}
						body = a.countBody(len(list), deciders)
					}
				}
			}
		}
		for _, id := range ids {
			a.reviewTried[to+"\x00"+id] = true
		}
		if p.left != "" {
			a.reviewTried[p.left] = true
		}
		if (isOperator[to] || isOwner[to]) && !capable {
			// An operator gets the requests by name or nothing yet (a device
			// just added may not have said what it reads): a count now would
			// mark them told. Tried once, like any item: it is looked at
			// again only after a Hub member list event (reviewAgain), when a
			// new item waits or when the daemon starts again. A ping is no
			// change: nothing polls the Hub.
			if a.reviewAgain.skip(to, since) {
				a.wakeWorker()
			}
			continue
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
				// Its key here must be the granted one (a steward's device:
				// the key its roster lists), or nothing is sent.
				active, err := operatorHolds(tx, to, reportKey)
				if err != nil {
					return err
				}
				owner, err := ownerReportHolds(tx, to, reportKey)
				if err != nil {
					return err
				}
				if !active && !owner || isOperator[to] && !active {
					return errNoNewReview
				}
				if fullText && !owner {
					steward, err := stewardHolds(tx, to, reportKey)
					if err != nil {
						return err
					}
					if !steward {
						return errNoNewReview
					}
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
			for _, id := range p.stale {
				res, err := tx.Exec(`DELETE FROM reported WHERE item = ? AND recipient = ?`, id, to)
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
			if len(ids) == 0 {
				return nil
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
				a.Logf("review notice to %s not queued (%v); %d item(s) wait: see `agentnet inbox --review`", to, err, len(list))
				a.noteReviewFailure(to, gen, err.Error()) // doctor says so
			}
			continue
		}
		a.noteReviewFailure(to, gen, "")
		for _, id := range ids {
			delete(a.reviewTried, to+"\x00"+id) // reported; a later return to review is new
		}
		if p.left != "" {
			delete(a.reviewTried, p.left)
		}
	}
}

// reviewAgain holds the operator devices a review pass skipped because they
// could not read reports yet (an older program, or one that has not
// connected or published what it reads since it was linked or updated).
// Only a Hub member list event (pushed when a device connects, publishes its
// capabilities or gets another roster step: onMembers) has the next pass
// look at them again, or one that came while the skipping pass ran; a new
// item or the daemon's next run does too. A ping changes nothing, so
// nothing polls the Hub.
type reviewAgain struct {
	sync.Mutex
	events  uint64          // member list events this run
	skipped map[string]bool // waiting for a member list event
	changed map[string]bool // to look at again on the next pass
}

// begin is called as a pass starts; skip takes what it returns.
func (r *reviewAgain) begin() uint64 {
	r.Lock()
	defer r.Unlock()
	return r.events
}

// skip records address as skipped by the pass that began at since. It
// reports whether a member list event came since then: the address is then
// looked at again on the next pass (the caller wakes the worker), since
// that event may have been its news.
func (r *reviewAgain) skip(address string, since uint64) bool {
	r.Lock()
	defer r.Unlock()
	if r.events != since {
		if r.changed == nil {
			r.changed = map[string]bool{}
		}
		r.changed[address] = true
		return true
	}
	if r.skipped == nil {
		r.skipped = map[string]bool{}
	}
	r.skipped[address] = true
	return false
}

// note records a member list event; it reports whether a skipped device
// is now to be looked at again (the worker is then woken).
func (r *reviewAgain) note() bool {
	r.Lock()
	defer r.Unlock()
	r.events++
	if len(r.skipped) == 0 {
		return false
	}
	if r.changed == nil {
		r.changed = map[string]bool{}
	}
	for address := range r.skipped {
		r.changed[address] = true
	}
	r.skipped = nil
	return true
}

// take returns the skipped devices to look at again, once.
func (r *reviewAgain) take() []string {
	r.Lock()
	defer r.Unlock()
	out := make([]string, 0, len(r.changed))
	for address := range r.changed {
		out = append(out, address)
	}
	r.changed = nil
	return out
}

func (r *reviewAgain) reset() {
	r.Lock()
	r.skipped, r.changed = nil, nil
	r.Unlock()
}

// countText is a review notice for a device that reads no reports: how
// many requests wait on host (none: the earlier notice is settled).
func countText(n int, host string) string {
	if n == 0 {
		return "Nothing waits for a person's decision on " + host + " any more."
	}
	return fmt.Sprintf("%d request(s) wait for a person's decision on %s.", n, host)
}

// countBody is a version 2 report for a device that may not decide here:
// how many requests wait and who can decide them from their own devices.
func (a *Agent) countBody(n int, deciders []Decider) string {
	data, _ := json.Marshal(Report{V: 2, At: time.Now().Unix(), Host: a.Address, Items: []ReportItem{}, Count: n, Deciders: deciders})
	return string(data)
}

// noteReviewFailure records why the last notice to the review_to agent
// could not be queued (why ""; it was), for doctor, as long as the
// setting is still the one gen names.
func (a *Agent) noteReviewFailure(to, gen, why string) {
	if cur, _ := a.ReviewTo(); cur != to {
		return // an operator's notice: its own grant, not this setting
	}
	if why != "" {
		why = time.Now().UTC().Format(time.DateTime) + " UTC: " + why
	}
	if _, err := a.store.db.Exec(`INSERT OR REPLACE INTO config(k, v) SELECT ?, ? WHERE coalesce((SELECT v FROM config WHERE k = ?), '') = ?`,
		reviewToFailKey, why, reviewToGenKey, gen); err != nil {
		a.Logf("review notice: %v", err)
	}
}

// ReviewToHealth says whether notices to the review_to agent are getting
// out: the last notice that could not be queued since it was set, and how
// many queued ones the Hub refused for good since the last one that went
// out, with the last reason. Empty when none failed (or review notices are
// off).
func (a *Agent) ReviewToHealth() (string, error) {
	to, err := a.ReviewTo()
	if err != nil || to == "" {
		return "", err
	}
	var problems []string
	if why, err := a.store.config(reviewToFailKey); err == nil && why != "" {
		problems = append(problems, "the last notice could not be queued ("+why+")")
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var since int64 // the last notice that went out: refusals before it are over
	if err := a.store.db.QueryRow(`SELECT coalesce(max(created_ms), 0) FROM outbox WHERE recipient = ? AND status = ? AND state IN (?, ?)`,
		to, envelope.StatusReviewNotice, protocol.StateCustody, protocol.StateDelivered).Scan(&since); err != nil {
		return "", err
	}
	var refused int
	var last string
	if err := a.store.db.QueryRow(`SELECT count(*), coalesce((SELECT error FROM outbox WHERE recipient = ? AND status = ? AND state = ? AND created_ms > ? ORDER BY created_ms DESC LIMIT 1), '')
		FROM outbox WHERE recipient = ? AND status = ? AND state = ? AND created_ms > ?`, to, envelope.StatusReviewNotice, stateFailed, since, to, envelope.StatusReviewNotice, stateFailed, since).Scan(&refused, &last); err != nil {
		return "", err
	}
	if refused > 0 {
		problems = append(problems, fmt.Sprintf("the Hub refused %d notice(s) (last: %s)", refused, last))
	}
	if len(problems) == 0 {
		return "", nil
	}
	return "review notices to " + to + " are not getting out: " + strings.Join(problems, "; ") + "; check the address (agentnet review-to ADDRESS)", nil
}

// reportBody is a version 2 report of items for a granted operator: the
// requests named, with their first lines, each actionable from there.
func (a *Agent) reportBody(items []ReportItem) string {
	return a.reportBodyFor(items, nil, false, true)
}

// Full agent output travels only in ciphertext to current own devices or
// a current steward. Excerpt is an existing private report field; public
// execution statuses continue to carry only their bounded blocker text.
func (a *Agent) reportBodyFor(items []ReportItem, agentText map[string]string, full, actionable bool) string {
	r := Report{V: 2, At: time.Now().Unix(), Host: a.Address, Items: make([]ReportItem, 0, len(items))}
	for _, it := range items {
		it.Actionable = actionable
		if full && it.State == stateNeedHuman && agentText[it.ID] != "" {
			if it.Excerpt != "" {
				it.Excerpt = "Request: " + it.Excerpt + "\n\nAgent response:\n" + agentText[it.ID]
			} else {
				it.Excerpt = agentText[it.ID]
			}
		}
		r.Items = append(r.Items, it)
	}
	data, _ := json.Marshal(r)
	return string(data)
}

// These names come from the accepted request's encrypted manifests. Keep the
// summary bounded; local paths, file bytes and unrelated requests never travel.
func reviewFileExcerpt(files []FileInfo) string {
	names := make([]string, 0, min(len(files), 3))
	for _, f := range files[:min(len(files), 3)] {
		names = append(names, firstLine(f.Name))
	}
	return firstLine("Files: " + strings.Join(names, ", "))
}

func ownerReportHolds(q querier, address, fp string) (bool, error) {
	person, err := permissionPersonIn(q, address, fp)
	if err != nil || person == "" {
		return false, err
	}
	var self bool
	err = q.QueryRow(`SELECT state = ? FROM persons WHERE person = ?`, personSelf, person).Scan(&self)
	return self, err
}

// Another native device reads the same encrypted owner report as the
// browser. Project its result beside the original request, without any
// job or actions here. Local job details remain authoritative.
func (a *Agent) privateNeedsYou(msgs []ConvMessage) error {
	hosts := map[string]string{}
	for _, m := range msgs {
		if m.Job == "" && m.Target != nil && m.Exec != nil && m.Exec.State == stateNeedHuman && !m.History && m.ExcerptPID == "" {
			ok, err := ownerReportHolds(a.store.db, m.Target.Address, m.Target.Fingerprint)
			if err != nil {
				return err
			}
			if ok {
				hosts[m.Target.Address] = m.Target.Fingerprint
			}
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	rows, err := a.store.db.Query(`SELECT sender, coalesce(verified_by,''), body FROM inbox WHERE conv IS NULL AND local=0 AND replica=0 AND (`+receivedNotice+`) ORDER BY rowid`, envelope.KindMessage, envelope.StatusReviewNotice)
	if err != nil {
		return err
	}
	defer rows.Close()
	latest := map[string]Report{}
	for rows.Next() {
		var host, fp, body string
		if err := rows.Scan(&host, &fp, &body); err != nil {
			return err
		}
		if hosts[host] != fp || fp == "" {
			continue
		}
		if r, ok := notificationReport(body, host, fp); ok && r.At >= latest[host].At {
			latest[host] = r
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Job != "" || m.Target == nil || m.Exec == nil || m.Exec.State != stateNeedHuman || m.History || m.ExcerptPID != "" || hosts[m.Target.Address] != m.Target.Fingerprint {
			continue
		}
		for _, it := range latest[m.Target.Address].Items {
			if it.Conv && it.State == stateNeedHuman && it.Key == m.Key && it.Attempt == m.Exec.Attempt && (it.ID == m.ID || it.ID == m.LID || slices.ContainsFunc(m.Copies, func(c ConvCopy) bool { return c.ID == it.ID })) {
				m.JobDetail = it.Excerpt
				break
			}
		}
	}
	return nil
}

// receivedNotice matches exactly the rows isReviewNotice files (kind, status,
// no reply_to, no files); its two parameters are KindMessage and
// StatusReviewNotice.
const receivedNotice = `kind = ? AND status IS ? AND reply_to IS NULL AND NOT EXISTS (SELECT 1 FROM attachments WHERE message_id = inbox.id)`
