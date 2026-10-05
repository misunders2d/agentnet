package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
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

// profileCount counts the Hub profile reads made for one address and,
// while bare is set, answers them as for a device that has said nothing of
// what it reads (no capability records).
type profileCount struct {
	base http.RoundTripper
	path string
	n    *atomic.Int32
	bare *atomic.Bool
}

func (c profileCount) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != "GET" || r.URL.Path != c.path {
		return c.base.RoundTrip(r)
	}
	c.n.Add(1)
	resp, err := c.base.RoundTrip(r)
	if err != nil || !c.bare.Load() {
		return resp, err
	}
	var prof protocol.Profile
	err = json.NewDecoder(resp.Body).Decode(&prof)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	prof.Caps = nil
	data, _ := json.Marshal(prof)
	resp.Body, resp.ContentLength = io.NopCloser(bytes.NewReader(data)), int64(len(data))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// A steward's device that cannot read reports yet (an older program, or
// one that has not connected since it was linked) is skipped like a failed
// notice: tried once per item, never again on every worker wake (each Hub
// ping wakes the worker: that would poll the Hub). A member list showing
// that device changed has the host look once more, and then it is told.
func TestNonCapableStewardDeviceNotPolled(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	host := w.bob
	if _, err := host.GrantOperatorPerson(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	phone := linkedVia(t, w.alice, "phone", approveLink(w.alice))
	me, _, _ := w.alice.Person()
	if _, err := host.refreshPerson(tctx(t), me.Person, false); err != nil {
		t.Fatal(err)
	}
	if ops, _ := host.store.activeOperators(); !slices.Contains(ops, phone.Address) {
		t.Fatalf("the phone is no steward device: %v", ops)
	}
	label, name, _ := protocol.SplitAddress(phone.Address)
	var lookups atomic.Int32
	var bare atomic.Bool
	bare.Store(true)
	host.hub.http.Transport = profileCount{host.hub.http.Transport, "/v1/agents/" + label + "/" + name + "/profile", &lookups, &bare}
	task := protocol.NewID()
	if _, err := host.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state)
		VALUES(?, ?, 0, ?, 'run the export', 0, ?)`, task, w.alice.Address, envelope.KindTask, stateAwaiting); err != nil {
		t.Fatal(err)
	}
	told := func(a *Agent) bool {
		var n int
		host.store.db.QueryRow(`SELECT count(*) FROM reported WHERE item = ? AND recipient = ?`, task, a.Address).Scan(&n)
		return n == 1
	}
	host.sendReviewNotice(tctx(t))
	first := lookups.Load()
	if first == 0 || told(phone) || !told(w.alice) {
		t.Fatalf("first pass: %d phone lookup(s), phone told %v, laptop told %v", first, told(phone), told(w.alice))
	}
	for range 5 { // as five pings would wake the worker
		host.sendReviewNotice(tctx(t))
	}
	if n := lookups.Load(); n != first {
		t.Fatalf("the Hub was asked about the phone %d more time(s) on wakes that changed nothing", n-first)
	}
	// The Hub's member list says the phone is connected (as after an
	// update): looked at once more, and now it reads reports.
	bare.Store(false)
	raw, _ := json.Marshal(protocol.Members{Members: []protocol.Member{{Address: phone.Address, Presence: protocol.PresenceConnected, Joined: 1}}})
	host.onMembers(raw)
	host.sendReviewNotice(tctx(t))
	again := lookups.Load()
	if again == first || !told(phone) {
		t.Fatalf("a member change: %d more lookup(s), phone told %v", again-first, told(phone))
	}
	host.onMembers(raw) // the same entry: no change
	host.sendReviewNotice(tctx(t))
	if n := lookups.Load(); n != again {
		t.Fatalf("an unchanged member list looked again (%d more)", n-again)
	}
}
