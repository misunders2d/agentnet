package client

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// BUG-07: a request a stopped daemon left interrupted waits for the person
// everywhere they look, as held, awaiting and needs_human items do: the
// review list (inbox --review), doctor, the desktop alert count, the
// review notice to another agent, and the page; nothing reruns it on its
// own, and resolve closes it without running or sending anything.
func TestInterruptedWaitsForThePerson(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "wrap the 400 cases", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	// As a daemon that stopped while it ran leaves it (startWorker).
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateRunning, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.interruptRunning(); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateInterrupt)
	listed := func(msgs []Message) bool {
		return slices.ContainsFunc(msgs, func(m Message) bool { return m.ID == task.ID })
	}
	if review, err := w.bob.Review(); err != nil || !listed(review) {
		t.Fatalf("inbox --review leaves the interrupted task out: %v %+v", err, review)
	}
	if p, err := w.bob.PageReview(); err != nil || !listed(p.Device) {
		t.Fatalf("the page's review leaves it out: %v %+v", err, p.Device)
	}
	if _, total, err := w.bob.store.unnotified(); err != nil || total != 1 {
		t.Fatalf("the desktop alert count leaves it out: %v %d", err, total)
	}
	for _, c := range w.bob.Doctor(tctx(t)) {
		if c.Name == "review" && c.Result != "1 item(s) wait for your decision: agentnet inbox --review" {
			t.Fatalf("doctor: %+v", c)
		}
	}
	// The review notice to the person's other agent counts it.
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	w.bob.wakeWorker()
	eventually(t, "alice to be told an item waits on bob", func() bool {
		n, err := w.alice.Notices()
		return err == nil && len(n) == 1 && n[0].From == w.bob.Address
	})
	// Current report-capable peers get a structured count/decider snapshot,
	// not the legacy text notice. Naming Alice grants no item visibility or
	// remote decision authority (headless.go's count-only report contract).
	notice := mustNotices(t, w.alice)[0]
	if report, ok := w.alice.NoticeReport(notice); !ok || report.Host != w.bob.Address || report.Count != 1 || len(report.Items) != 0 {
		t.Fatalf("interrupted task count report: %+v %v", report, ok)
	}
	if strings.Contains(notice.Body, task.ID) || strings.Contains(notice.Body, "wrap the 400 cases") {
		t.Fatal("count-only interrupted notice exposed the request")
	}
	// Closing it runs and sends nothing, and it leaves review.
	if err := w.bob.Resolve(task.ID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s, _ := w.bob.store.jobState(task.ID); s != stateResolved {
		t.Fatalf("resolved interrupted task is %s", s)
	}
	if review, _ := w.bob.Review(); listed(review) {
		t.Fatalf("a closed task still waits: %+v", review)
	}
	if _, ok := findReply(w.alice, task.ID); ok {
		t.Fatal("closing an interrupted task sent a reply")
	}
}

// BUG-07: a DM request to this device's agent that a stopped daemon left
// interrupted is listed in the page's needs-you, with its reason, as Accept
// takes it (to run it again); resolve closes it there too.
func TestInterruptedAgentRequestNeedsYou(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	pid := participate(t, w, conv, nil, nil)
	if _, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "rotate the key"); err != nil {
		t.Fatal(err)
	}
	var id string
	eventually(t, "the task waiting at bob", func() bool {
		w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND body = ? AND state = ?`, conv, "rotate the key", stateAwaiting).Scan(&id)
		return id != ""
	})
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateRunning, id); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.interruptRunning(); err != nil {
		t.Fatal(err)
	}
	if r, ok := needsYou(t, w.bob, pid, id); !ok || r.Reason != ReviewInterrupted || r.State != stateInterrupt {
		t.Fatalf("the interrupted request in needs-you: %+v %v", r, ok)
	}
	if review, _ := w.bob.Review(); !slices.ContainsFunc(review, func(m Message) bool { return m.ID == id }) {
		t.Fatalf("inbox --review leaves the interrupted request out: %+v", review)
	}
	if err := w.bob.Resolve(id); err != nil {
		t.Fatal(err)
	}
	if r, ok := needsYou(t, w.bob, pid, id); ok {
		t.Fatalf("a closed request is still listed: %+v", r)
	}
}

// An operator device of an older version (v0.6.2) drops a whole report
// that names an interrupted request, and with it the desktop alert for
// everything else that waits there. Until operators have updated, their
// reports leave interrupted requests out; everything else is still named
// (review finding 2).
func TestOperatorReportLeavesInterruptedOut(t *testing.T) {
	w := newWorld(t, "")
	dave := mustJoin(t, filepath.Join(t.TempDir(), "dave"), w.aliceInvites("dave"), "desk") // the requester
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	runAgent(t, dave)
	stopped, err := dave.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "wrap the 400 cases", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, stopped.ID, stateAwaiting)
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateRunning, stopped.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.interruptRunning(); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, stopped.ID, stateInterrupt)
	waiting, err := dave.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "label the pallets", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, waiting.ID, stateAwaiting)
	if _, err := w.bob.Send(tctx(t), w.alice.Address, "hi", ""); err != nil { // pins alice's key
		t.Fatal(err)
	}
	if _, err := w.bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the operator's report", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok && slices.ContainsFunc(r.Items, func(it ReportItem) bool { return it.ID == waiting.ID }) {
				return true
			}
		}
		return false
	})
	for _, m := range mustNotices(t, w.alice) {
		r, ok := w.alice.NoticeReport(m)
		if !ok {
			continue
		}
		for _, it := range r.Items {
			if it.State != stateAwaiting && it.State != stateHeld && it.State != stateNeedHuman { // what v0.6.2 reads
				t.Fatalf("an operator's report names a request in state %s, for which an older operator device drops the report: %+v", it.State, r.Items)
			}
		}
	}
}
