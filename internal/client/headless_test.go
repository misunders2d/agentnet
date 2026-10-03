package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// execOf is alice's view of the execution state of her sent request id.
func execOf(t *testing.T, a *Agent, id string) *ExecView {
	t.Helper()
	return legacyView(t, a, id).Exec
}

// A bot asks back in the chat when it cannot answer, the person answers
// in the same thread and gets the correlated answer; meanwhile the
// requester sees the request's execution state as the bot's device
// reports it, apart from delivery.
func TestHeadlessClarificationAndStatus(t *testing.T) {
	st := installStub(t, "askback")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "what's the weather today?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	var ask Message
	eventually(t, "the bot asks back in the chat", func() bool {
		r, ok := findReply(w.alice, q.ID)
		ask = r
		return ok && r.Kind == envelope.KindAnswer && strings.Contains(r.Body, "Which city?")
	})
	if bob := inboxRow(t, w.bob, q.ID); bob.State != stateAnswered {
		t.Fatalf("the clarification became a decision on the bot: %+v", bob)
	}
	if review, _ := w.bob.Review(); len(review) != 0 {
		t.Fatalf("a clarification waits for a person on the bot: %+v", review)
	}
	// The person answers in the same thread; the follow-up is a question
	// again (never upgraded to a task), answered with the earlier context.
	follow, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "Riga", Kind: envelope.KindQuestion, ReplyTo: ask.ID})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, follow.ID, stateAnswered)
	eventually(t, "the correlated answer", func() bool {
		r, ok := findReply(w.alice, follow.ID)
		return ok && strings.Contains(r.Body, "Riga: bring a jacket")
	})
	if st.count() != 2 {
		t.Fatalf("the agent ran %d time(s); the original was not rerun", st.count())
	}
	// Status: the bot's device reported the states of the first question
	// (queued, then running), separate from delivery; its answer, the
	// bot's last word on it, supersedes them in the view at once.
	eventually(t, "alice sees the bot's word on her question", func() bool {
		e := execOf(t, w.alice, q.ID)
		return e != nil && e.Host == w.bob.Address && e.State == envelope.StatusDone
	})
	e := execOf(t, w.alice, q.ID)
	if e.Stale {
		t.Fatalf("a connected host is shown stale: %+v", e)
	}
	if m := legacyView(t, w.alice, q.ID); m.State != protocol.StateDelivered && m.State != protocol.StateCustody {
		t.Fatalf("delivery got mixed with execution: %+v", m)
	}
	// Statuses are never jobs, decisions, chat messages or alerts.
	if msgs, _ := w.alice.Inbox(false, false); len(msgs) != 2 {
		t.Fatalf("statuses in the inbox: %d messages", len(msgs))
	}
	if review, _ := w.alice.Review(); len(review) != 0 {
		t.Fatalf("a status waits for a decision: %+v", review)
	}
}

// A forged status (from a device that does not hold the request) is
// refused; a device without the capability is told nothing.
func TestForgedStatusRefused(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	runAgent(t, carol)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds it", func() bool { return inboxCount(t, w.bob, `id = ?`, q.ID) == 1 })
	// Carol claims bob's request is running.
	body, _ := json.Marshal(envelope.Status{State: "running", N: 1, At: time.Now().Unix()})
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: carol.Address, To: w.alice.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: string(body), Ref: &envelope.Ref{ID: q.ID, Fingerprint: w.alice.Self().Fingerprint()}}
	key, _ := carol.sendKey(tctx(t), w.alice.Address)
	rcpt, _ := key.Recipient()
	env, _ := envelope.Seal(in, carol.id.Sign, rcpt)
	if err := carol.store.addControlOutbox(env, in); err != nil {
		t.Fatal(err)
	}
	if _, err := carol.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice refuses it", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ? AND reason = ?`, in.ID, reasonInvalid).Scan(&n)
		return n == 1
	})
	if e := execOf(t, w.alice, q.ID); e != nil && e.Host == carol.Address {
		t.Fatalf("a forged status was shown: %+v", e)
	}
}

// An operator granted on the bot decides a waiting task from its own
// device once: the report names the request (with its first line, for the
// operator only), the decision applies exactly once whatever arrives again,
// a stale or revoked decision is refused with the reason, and the
// operator learns the resulting state each time.
func TestOperatorDecidesOnHeadlessHost(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	bob := w.bob // the headless host
	setResponder(t, bob, "stub", st.dir, time.Minute)
	dave := mustJoin(t, filepath.Join(t.TempDir(), "dave"), w.aliceInvites("dave"), "desk") // the requester
	runAgent(t, w.alice)
	runAgent(t, bob)
	runAgent(t, dave)
	n := fakeNotify(w.alice)
	// Alice is granted once, on the host, for her exact pinned key.
	if _, err := bob.GrantOperator(w.alice.Address); !errors.Is(err, ErrNoPinnedKey) {
		t.Fatalf("a grant before any key is pinned: %v", err)
	}
	if _, err := bob.Send(tctx(t), w.alice.Address, "hello from the host", ""); err != nil { // pins alice's key on bob
		t.Fatal(err)
	}
	fp, err := bob.GrantOperator(w.alice.Address)
	if err != nil || fp != w.alice.Self().Fingerprint() {
		t.Fatalf("grant: %q %v", fp, err)
	}
	task, err := dave.SendMessage(tctx(t), Outgoing{To: bob.Address, Body: "please run the nightly export", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, task.ID, stateAwaiting)
	// The report reaches the operator with the request named, and counts
	// as a decision for her; dave (not an operator) gets nothing.
	var report Message
	var item ReportItem
	eventually(t, "alice's report", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok && len(r.Items) == 1 {
				report, item = m, r.Items[0]
				return true
			}
		}
		return false
	})
	if item.ID != task.ID || item.From != dave.Address || item.Key != dave.Self().Fingerprint() || item.Kind != envelope.KindTask ||
		item.State != stateAwaiting || item.Blocker != BlockerAcceptance || item.Excerpt != "please run the nightly export" || !item.Actionable {
		t.Fatalf("report item: %+v", item)
	}
	eventually(t, "the operator is alerted once", func() bool { return n.count() == 1 })
	if !strings.HasPrefix(n.last(), "AgentNet: 1 remote request(s) on "+bob.Address+" need your decision") {
		t.Fatalf("alert: %q", n.last())
	}
	if notices := mustNotices(t, dave); len(notices) != 0 {
		t.Fatalf("the requester got a report: %+v", notices)
	}
	// Decide: accept once. The host runs it; the operator learns the state.
	sent, err := w.alice.Decide(tctx(t), bob.Address, item.ID, item.Key, "accept", item.State, item.Attempt, "", report.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, task.ID, stateAnswered)
	eventually(t, "the host answers the decision", func() bool {
		r, _ := w.alice.NoticeReport(report)
		return len(r.Items) == 1 && r.Items[0].Result != nil && r.Items[0].Result.Decision == sent.ID && r.Items[0].Result.Refused == ""
	})
	eventually(t, "dave gets the result", func() bool { r, ok := findReply(dave, task.ID); return ok && r.Kind == envelope.KindResult })
	if st.count() != 1 {
		t.Fatalf("ran %d time(s)", st.count())
	}
	// The same decision delivered again: nothing runs again.
	env, _ := w.alice.store.outboxEnvelope(sent.ID)
	if _, err := w.alice.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	// A new decision naming the state the operator saw before: stale, refused.
	again, err := w.alice.Decide(tctx(t), bob.Address, item.ID, item.Key, "accept", stateAwaiting, item.Attempt, "", report.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the stale decision is refused with the reason", func() bool {
		r, _ := w.alice.NoticeReport(report)
		return r.Items[0].Result != nil && r.Items[0].Result.Decision == again.ID && strings.Contains(r.Items[0].Result.Refused, "no longer as you saw it")
	})
	time.Sleep(300 * time.Millisecond)
	if st.count() != 1 {
		t.Fatalf("a replayed or stale decision ran the task again: %d run(s)", st.count())
	}
	// Revoked: refused, and nothing about the request is told.
	if err := bob.RevokeOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	task2, _ := dave.SendMessage(tctx(t), Outgoing{To: bob.Address, Body: "second task", Kind: envelope.KindTask})
	waitState(t, bob, task2.ID, stateAwaiting)
	denied, err := w.alice.Decide(tctx(t), bob.Address, task2.ID, dave.Self().Fingerprint(), "accept", stateAwaiting, 0, "", report.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the revoked operator is refused", func() bool {
		var body string
		w.alice.store.db.QueryRow(`SELECT body FROM inbox WHERE sender = ? AND sub = ? AND body LIKE ?`, bob.Address, envelope.SubStatus, `%"decision":"`+denied.ID+`"%`).Scan(&body)
		return strings.Contains(body, ErrNotOperator.Error())
	})
	if s, _ := bob.store.jobState(task2.ID); s != stateAwaiting {
		t.Fatalf("a revoked operator's decision applied: %s", s)
	}
	// The refused decision replayed: the same refusal again, never a
	// fresh look that could pass.
	env, _ = w.alice.store.outboxEnvelope(denied.ID)
	if _, err := w.alice.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if s, _ := bob.store.jobState(task2.ID); s != stateAwaiting {
		t.Fatalf("a replayed refused decision applied: %s", s)
	}
	// Granted again: a held question is answered by the operator's reply
	// through the ordinary reply path, exactly once even when replayed.
	if _, err := bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	held, _ := dave.SendMessage(tctx(t), Outgoing{To: bob.Address, Body: "which region?", Kind: envelope.KindQuestion})
	waitState(t, bob, held.ID, stateHeld)
	var heldReport Message
	var heldItem ReportItem
	eventually(t, "the operator's report names the held question", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok {
				for _, it := range r.Items {
					if it.ID == held.ID && it.Blocker == BlockerApproval {
						heldReport, heldItem = m, it
						return true
					}
				}
			}
		}
		return false
	})
	reply, err := w.alice.Decide(tctx(t), bob.Address, heldItem.ID, heldItem.Key, "reply", heldItem.State, heldItem.Attempt, "eu-west, decided by the operator", heldReport.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, held.ID, stateManual)
	eventually(t, "dave gets the operator's answer", func() bool {
		r, ok := findReply(dave, held.ID)
		return ok && r.Body == "eu-west, decided by the operator"
	})
	env, _ = w.alice.store.outboxEnvelope(reply.ID)
	if _, err := w.alice.deliver(tctx(t), env, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	var answers int
	bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE reply_to = ?`, held.ID).Scan(&answers)
	if answers != 1 {
		t.Fatalf("the replayed reply decision sent %d answers", answers)
	}
	eventually(t, "the reply decision's result", func() bool {
		r, _ := w.alice.NoticeReport(heldReport)
		for _, it := range r.Items {
			if it.ID == held.ID {
				return it.Result != nil && it.Result.Decision == reply.ID && it.Result.Refused == ""
			}
		}
		return false
	})
	// Nothing came to alice as a chat turn or a local decision.
	if review, _ := w.alice.Review(); len(review) != 0 {
		t.Fatalf("decisions or statuses wait for alice: %+v", review)
	}
}

// An operator granted while requests already wait is told about them (a
// report is per recipient); a review destination told earlier is not told
// twice.
func TestNewOperatorGetsExistingWaitingItems(t *testing.T) {
	w := newWorld(t, "")
	dave := mustJoin(t, filepath.Join(t.TempDir(), "dave"), w.aliceInvites("dave"), "desk")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	runAgent(t, dave)
	if err := w.bob.SetReviewTo(dave.Address); err != nil {
		t.Fatal(err)
	}
	task, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "waiting before any grant", Kind: envelope.KindTask})
	waitState(t, w.bob, task.ID, stateAwaiting)
	eventually(t, "dave gets the count", func() bool { return len(mustNotices(t, dave)) == 1 })
	if _, err := w.bob.Send(tctx(t), w.alice.Address, "hi", ""); err != nil { // pins alice's key
		t.Fatal(err)
	}
	if _, err := w.bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new operator gets the waiting item", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok && len(r.Items) == 1 && r.Items[0].ID == task.ID && r.Items[0].Actionable {
				return true
			}
		}
		return false
	})
	quiet(t, dave, fakeNotify(dave), 0)
	if n := len(mustNotices(t, dave)); n != 1 {
		t.Fatalf("dave was told %d times", n)
	}
}

func mustNotices(t *testing.T, a *Agent) []Message {
	t.Helper()
	ns, err := a.Notices()
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// A reply without a valid emotion line is delivered and shown neutral: a
// formatting slip is not a decision for a person.
func TestReplyWithoutEmotionIsNeutral(t *testing.T) {
	st := installStub(t, "answer") // "stub answer", no emotion line
	w, conv, lids := dmWithHistory(t)
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	p, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, lids[:1], nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob sees the invite", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	if _, err := w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice sees it active", func() bool { return stateAt(t, w.alice, p.PID).State == PartActive })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "what failed?")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	eventually(t, "alice gets the neutral answer", func() bool {
		msgs, _ := w.alice.ConversationMessages(conv)
		for _, m := range msgs {
			if m.Kind == envelope.KindAnswer && m.PID == p.PID {
				return m.Body == "stub answer" && m.Emotion == "neutral"
			}
		}
		return false
	})
	if review, _ := w.bob.Review(); len(review) != 0 {
		t.Fatalf("a missing emotion line became a decision: %+v", review)
	}
}

// A review destination that is not a granted operator gets the count and
// nothing else, even though it could read reports; it stays quiet and
// read-only. (A granted operator's report alerts: TestOperatorDecidesOnHeadlessHost.)
func TestCountOnlyReportsStayQuiet(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.alice)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	if err := w.bob.SetReviewTo(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	q, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "held question", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q.ID, stateHeld)
	eventually(t, "alice gets the report", func() bool { return len(mustNotices(t, w.alice)) == 1 })
	m := mustNotices(t, w.alice)[0]
	if _, ok := w.alice.NoticeReport(m); ok || strings.Contains(m.Body, q.ID) || strings.Contains(m.Body, "held question") || !strings.HasPrefix(m.Body, "1 request(s) wait") {
		t.Fatalf("a non-operator's notice carries more than the count: %q", m.Body)
	}
	quiet(t, w.alice, n, 0)
	if review, _ := w.alice.Review(); len(review) != 0 {
		t.Fatalf("a report waits as a decision: %+v", review)
	}
}

// The click on a decision notification opens the messenger at the exact
// message when the daemon serves a page; without one, the terminal review.
func TestReviewClickOpensMessengerWhenServed(t *testing.T) {
	if clickOS != "linux" {
		t.Skip("clicks are handled on Linux only")
	}
	log := stubTerminal(t) // records argv; reused as the URL opener
	old := urlOpener
	urlOpener = terminalLauncher
	t.Cleanup(func() { urlOpener = old })
	t.Setenv("INVOCATION_ID", "")
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	os.WriteFile(filepath.Join(w.bobHome, "ui-url"), []byte("http://127.0.0.1:43111/?token=secret\n"), 0o600)
	runWith(t, w, w.bob, RunOptions{})
	q, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q.ID, stateHeld)
	eventually(t, "notification", func() bool { return n.count() == 1 })
	click := n.lastClick()
	if click == nil {
		t.Fatal("no click handler")
	}
	click()
	eventually(t, "the page opened", func() bool { data, _ := os.ReadFile(log); return strings.Contains(string(data), "#msg=") })
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "http://127.0.0.1:43111/#msg="+q.ID+"&dir=in") || strings.Contains(string(data), "secret") {
		t.Fatalf("opened: %s", data)
	}
	n.mu.Lock()
	args := append([]string{}, n.argvs[len(n.argvs)-1]...)
	n.mu.Unlock()
	if strings.Contains(strings.Join(args, " "), "secret") {
		t.Fatal("notification command exposed the UI token")
	}
	if s, _ := w.bob.store.jobState(q.ID); s != stateHeld {
		t.Fatalf("a click changed the request: %s", s)
	}
}

// Two quick state changes of one request (running, then needs a person)
// are told to the requester as two statuses with increasing counters, the
// last one the latest state. Each status used to pick its counter in its
// own goroutine from what was stored, so both could carry the same counter
// and the requester could keep "running" for a request that waits for a
// person.
func TestQuickStatusesOrdered(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	for i := range 6 {
		task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "quick change " + string(rune('a'+i)), Kind: envelope.KindTask})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, w.bob, task.ID, stateAwaiting)
		for _, state := range []string{stateRunning, stateNeedHuman} {
			if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, state, task.ID); err != nil {
				t.Fatal(err)
			}
			w.bob.noteStatus(task.ID)
		}
		eventually(t, "alice sees the request needs a person", func() bool {
			e := execOf(t, w.alice, task.ID)
			return e != nil && e.State == "needs_human"
		})
		rows, err := w.bob.store.db.Query(`SELECT body FROM outbox WHERE sub = ? AND ref_id = ?`, envelope.SubStatus, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[int64]bool{}
		for rows.Next() {
			var body string
			var s envelope.Status
			if err := rows.Scan(&body); err != nil || json.Unmarshal([]byte(body), &s) != nil {
				t.Fatalf("status row: %v", err)
			}
			if seen[s.N] {
				t.Fatalf("two statuses of %s carry counter %d", task.ID, s.N)
			}
			seen[s.N] = true
		}
		rows.Close()
	}
}
