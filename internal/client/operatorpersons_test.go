package client

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func holds(t *testing.T, host *Agent, a *Agent) bool {
	t.Helper()
	ok, err := operatorHolds(host.store.db, a.Address, a.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func approveLink(a *Agent) func(context.Context, string) error {
	return func(ctx context.Context, id string) error { return a.DecideLink(ctx, id, true) }
}

// A steward is a person: named once on the host, it covers every current
// device of that person as its signed roster pinned there lists it, a
// device added later once the host pins the newer step, and stops for a
// removed device, a frozen person or a revoked grant (MEL-532).
func TestOperatorPersonGrant(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	host := w.bob
	if err := host.SetService(); err != nil {
		t.Fatal(err)
	}
	g, err := host.GrantOperatorPerson(tctx(t), w.alice.Address)
	me, _, _ := w.alice.Person()
	if err != nil || g.Person != me.Person || g.Label != me.Label || len(g.Devices) != 1 || g.Devices[0].Address != w.alice.Address {
		t.Fatalf("grant %+v %v", g, err)
	}
	if !holds(t, host, w.alice) {
		t.Fatal("the steward's device does not decide")
	}
	if ops, _ := host.store.activeOperators(); !slices.Contains(ops, w.alice.Address) {
		t.Fatalf("reports not for the steward's device: %v", ops)
	}
	if who, _ := host.store.deciders(); len(who) != 1 || who[0].Person != me.Person || who[0].Label != me.Label {
		t.Fatalf("deciders %+v", who)
	}

	// A device added to the steward's roster decides once the host pins
	// the newer step: no grant on the host again.
	phone := linkedVia(t, w.alice, "phone", approveLink(w.alice))
	if holds(t, host, phone) {
		t.Fatal("a device the host has not seen in the roster decides")
	}
	if _, err := host.refreshPerson(tctx(t), me.Person, false); err != nil {
		t.Fatal(err)
	}
	if ops, _ := host.store.activeOperators(); !slices.Contains(ops, phone.Address) {
		t.Fatalf("the new device gets no reports: %v", ops)
	}
	if _, err := host.sendKey(tctx(t), phone.Address); err != nil { // as sending its report pins it
		t.Fatal(err)
	}
	if !holds(t, host, phone) {
		t.Fatal("the steward's new device does not decide")
	}
	// Another key for that address is not the roster's.
	if ok, _ := operatorHolds(host.store.db, phone.Address, w.alice.Self().Fingerprint()); ok {
		t.Fatal("a key the roster does not list decides")
	}

	// Removed from the roster: it stops once the host pins that step.
	if err := w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := host.refreshPerson(tctx(t), me.Person, false); err != nil {
		t.Fatal(err)
	}
	if holds(t, host, phone) || !holds(t, host, w.alice) {
		t.Fatal("a removed device still decides, or the rest stopped")
	}

	// The host's own person is refused; revoking ends it.
	if _, err := w.alice.GrantOperatorPerson(tctx(t), w.alice.Address); err == nil {
		t.Fatal("this installation's own person granted")
	}
	if _, err := host.RevokeOperatorPerson(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if holds(t, host, w.alice) {
		t.Fatal("a revoked steward decides")
	}
	if _, err := host.RevokeOperatorPerson(me.Person); err == nil {
		t.Fatal("revoked twice")
	}
}

// A frozen (conflicting) steward person covers nothing.
func TestOperatorPersonFrozen(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	if _, err := w.bob.GrantOperatorPerson(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	freeze(t, w.bob, w.alice)
	if holds(t, w.bob, w.alice) {
		t.Fatal("a frozen steward decides")
	}
	if ops, _ := w.bob.store.activeOperators(); len(ops) != 0 {
		t.Fatalf("a frozen steward gets reports: %v", ops)
	}
}

// The steward decides a request waiting on the host from a device added
// after the grant: the host sends that device the waiting requests by name
// once it pins the newer roster step, and the decision applies (MEL-532).
func TestStewardNewDeviceGetsReport(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	host := w.bob
	setResponder(t, host, "stub", st.dir, time.Minute)
	runAgent(t, w.alice)
	runAgent(t, host)
	persons(t, w.alice)
	dave := mustJoin(t, t.TempDir()+"/dave", w.aliceInvites("dave"), "desk")
	runAgent(t, dave)
	if _, err := host.GrantOperatorPerson(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	task, err := dave.SendMessage(tctx(t), Outgoing{To: host.Address, Body: "run the export", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, host, task.ID, stateAwaiting)
	named := func(a *Agent) (Message, ReportItem, bool) {
		for _, m := range mustNotices(t, a) {
			if r, ok := a.NoticeReport(m); ok {
				for _, it := range r.Items {
					if it.ID == task.ID && it.Actionable {
						return m, it, true
					}
				}
			}
		}
		return Message{}, ReportItem{}, false
	}
	eventually(t, "the steward's laptop gets the request by name", func() bool { _, _, ok := named(w.alice); return ok })
	phone := linkedVia(t, w.alice, "phone", approveLink(w.alice))
	me, _, _ := w.alice.Person()
	if _, err := host.refreshPerson(tctx(t), me.Person, false); err != nil { // as the members list makes it
		t.Fatal(err)
	}
	var report Message
	var item ReportItem
	eventually(t, "the new phone gets the waiting request by name", func() bool {
		var ok bool
		report, item, ok = named(phone)
		return ok
	})
	if _, err := phone.Decide(tctx(t), host.Address, item.ID, item.Key, "accept", item.State, item.Attempt, "", report.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, host, task.ID, stateAnswered)
	eventually(t, "dave gets the result", func() bool { r, ok := findReply(dave, task.ID); return ok && r.Kind == envelope.KindResult })
}

// A request in a DM to the host's agent that waits for its OK is listed by
// name for the steward, marked conv (no answer by hand), and the steward's
// accept or decline applies there (MEL-532); a reply is refused.
func TestStewardDecidesDMRequest(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	carol := mustJoin(t, t.TempDir()+"/carol", w.aliceInvites("carol"), "desk")
	runAgent(t, carol)
	persons(t, carol)
	if _, err := w.bob.GrantOperatorPerson(tctx(t), carol.Address); err != nil {
		t.Fatal(err)
	}
	pid := participate(t, w, conv, nil, nil)
	ask := func(body string) (Message, ReportItem) {
		task, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, body)
		if err != nil {
			t.Fatal(err)
		}
		var report Message
		var item ReportItem
		var id string
		eventually(t, "the task waits on bob", func() bool {
			id = ""
			w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND lid = ? AND replica = 0`, conv, task.LID).Scan(&id)
			return id != "" && jobState(t, w.bob, id) == stateAwaiting
		})
		eventually(t, "carol's report names it", func() bool {
			for _, m := range mustNotices(t, carol) {
				if r, ok := carol.NoticeReport(m); ok {
					for _, it := range r.Items {
						if it.ID == id && it.Actionable {
							report, item = m, it
							return true
						}
					}
				}
			}
			return false
		})
		return report, item
	}
	report, item := ask("restart the deploy")
	if !item.Conv || item.Kind != envelope.KindTask || item.State != stateAwaiting {
		t.Fatalf("item %+v", item)
	}
	reply, err := carol.Decide(tctx(t), w.bob.Address, item.ID, item.Key, "reply", item.State, item.Attempt, "done by hand", report.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "a reply is refused", func() bool {
		r, _ := carol.NoticeReport(report)
		for _, it := range r.Items {
			if it.ID == item.ID && it.Result != nil && it.Result.Decision == reply.ID {
				return strings.Contains(it.Result.Refused, "answered there")
			}
		}
		return false
	})
	if _, err := carol.Decide(tctx(t), w.bob.Address, item.ID, item.Key, "accept", item.State, item.Attempt, "", report.ID); err != nil {
		t.Fatal(err)
	}
	if res := replyAt(t, w.alice, conv, item.ID); res.Kind != envelope.KindResult {
		t.Fatalf("result %+v", res)
	}

	report, item = ask("drop the tables")
	if _, err := carol.Decide(tctx(t), w.bob.Address, item.ID, item.Key, "decline", item.State, item.Attempt, "not today", report.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "declined on bob", func() bool { return jobState(t, w.bob, item.ID) == stateDeclined })
	if st.runs() != 1 {
		t.Fatalf("ran %d time(s)", st.runs())
	}
}

// A device that may not decide gets how many wait and who decides them,
// never "decide on that machine" (MEL-532).
func TestCountReportNamesDeciders(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice)
	carol := mustJoin(t, t.TempDir()+"/carol", w.aliceInvites("carol"), "desk")
	runAgent(t, carol)
	if err := w.bob.SetReviewTo(tctx(t), carol.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.GrantOperatorPerson(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	q, _ := carol.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "held question", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q.ID, stateHeld)
	me, _, _ := w.alice.Person()
	eventually(t, "carol's count names the steward", func() bool {
		for _, m := range mustNotices(t, carol) {
			if r, ok := carol.NoticeReport(m); ok && r.Count == 1 && len(r.Items) == 0 && len(r.Deciders) == 1 && r.Deciders[0].Person == me.Person && r.Deciders[0].Label == me.Label {
				return !strings.Contains(m.Body, "held question") && !strings.Contains(m.Body, q.ID)
			}
		}
		return false
	})
}
