package client

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// storeNotice stores at a review notice from host with body, as received.
func storeNotice(t *testing.T, at, host *Agent, body string, ts int64) string {
	t.Helper()
	in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: host.Address, To: at.Address, TS: ts,
		Kind: envelope.KindMessage, Status: envelope.StatusReviewNotice, Body: body}
	if err := at.store.addInbox(in, host.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	return in.ID
}

func reportAt(host string, at int64, n int) string {
	data, _ := json.Marshal(Report{V: 2, At: at, Host: host, Items: []ReportItem{}, Count: n})
	return string(data)
}

func openNotices(t *testing.T, a *Agent) []string {
	t.Helper()
	var ids []string
	for _, m := range mustNotices(t, a) {
		ids = append(ids, m.ID)
	}
	return ids
}

// A newer report from a host replaces its older ones: one card per host
// (MEL-532). Other hosts' cards stay.
func TestReviewNoticeSupersedes(t *testing.T) {
	w := newWorld(t, "")
	carol := mustJoin(t, t.TempDir()+"/carol", w.aliceInvites("carol"), "desk")
	now := time.Now().Unix()
	first := storeNotice(t, w.alice, w.bob, reportAt(w.bob.Address, now-10, 1), now-10)
	other := storeNotice(t, w.alice, carol, reportAt(carol.Address, now-5, 1), now-5)
	second := storeNotice(t, w.alice, w.bob, reportAt(w.bob.Address, now, 2), now)
	if got := strings.Join(openNotices(t, w.alice), ","); !strings.Contains(got, second) || !strings.Contains(got, other) || strings.Contains(got, first) {
		t.Fatalf("open notices %s (first %s, other %s, second %s)", got, first, other, second)
	}
	// An older count text (no report time) is ordered by its envelope time.
	if id := storeNotice(t, w.alice, w.bob, "1 request(s) wait", now+1); !strings.Contains(strings.Join(openNotices(t, w.alice), ","), id) {
		t.Fatal("a newer count text did not replace the report")
	}
}

// Offline is normal: an older report arriving late is stored resolved and
// never replaces a newer one.
func TestLateOlderNoticeStaysResolved(t *testing.T) {
	w := newWorld(t, "")
	now := time.Now().Unix()
	newer := storeNotice(t, w.alice, w.bob, reportAt(w.bob.Address, now, 2), now)
	late := storeNotice(t, w.alice, w.bob, reportAt(w.bob.Address, now-60, 1), now-60)
	if got := openNotices(t, w.alice); len(got) != 1 || got[0] != newer {
		t.Fatalf("open %v (newer %s, late %s)", got, newer, late)
	}
}

// A report saying nothing waits any more settles the host's card: stored
// resolved, and the older ones with it.
func TestSettledSnapshotClearsCard(t *testing.T) {
	w := newWorld(t, "")
	now := time.Now().Unix()
	storeNotice(t, w.alice, w.bob, reportAt(w.bob.Address, now-10, 1), now-10)
	data, _ := json.Marshal(Report{V: 2, At: now, Host: w.bob.Address, Items: []ReportItem{}})
	storeNotice(t, w.alice, w.bob, string(data), now)
	if got := openNotices(t, w.alice); len(got) != 0 {
		t.Fatalf("a settled host keeps a card: %v", got)
	}
}

// The host sends the settled snapshot once everything it reported is
// decided, with no timer: the steward's card clears.
func TestSettledSnapshotSentWhenDecided(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice)
	dave := mustJoin(t, t.TempDir()+"/dave", w.aliceInvites("dave"), "desk")
	runAgent(t, dave)
	if _, err := w.bob.GrantOperatorPerson(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	task, _ := dave.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "export", Kind: envelope.KindTask})
	waitState(t, w.bob, task.ID, stateAwaiting)
	var report Message
	var item ReportItem
	eventually(t, "the named report", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok && len(r.Items) == 1 {
				report, item = m, r.Items[0]
				return true
			}
		}
		return false
	})
	if _, err := w.alice.Decide(tctx(t), w.bob.Address, item.ID, item.Key, "accept", item.State, item.Attempt, "", report.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAnswered)
	// Acceptance can briefly settle the card before a running snapshot
	// reports the same request again. Join the completed run and report pass,
	// then await that exact final snapshot, not an earlier empty card.
	var settled string
	eventually(t, "the host's final settled snapshot", func() bool {
		if !w.bob.executorsIdle() || !w.bob.reviewMu.TryLock() {
			return false
		}
		defer w.bob.reviewMu.Unlock()
		var n int
		if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM reported WHERE recipient = ?`, w.alice.Address).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			return false
		}
		var body string
		if err := w.bob.store.db.QueryRow(`SELECT id, body FROM outbox WHERE recipient = ? AND status = ? ORDER BY rowid DESC LIMIT 1`,
			w.alice.Address, envelope.StatusReviewNotice).Scan(&settled, &body); err != nil {
			t.Fatal(err)
		}
		return noticeSettled(body)
	})
	waitState(t, w.alice, settled, stateResolved)
	eventually(t, "the card clears", func() bool { return len(mustNotices(t, w.alice)) == 0 })
	var n int
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM reported WHERE recipient = ?`, w.alice.Address).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d reported marks left", n)
	}
}

// Notices stored before they superseded each other are settled once, at
// Open: all but the newest per host (the 11:58 leftovers).
func TestOpenResolvesLeftoverNotices(t *testing.T) {
	w := newWorld(t, "")
	now := time.Now().Unix()
	var ids []string
	for i := range 3 {
		in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: now - int64(30-i),
			Kind: envelope.KindMessage, Status: envelope.StatusReviewNotice, Body: "1 request(s) wait for a person's decision on " + w.bob.Address}
		// As an older version stored them: open, none superseded.
		if _, err := w.alice.store.db.Exec(insertInbox, inboxArgs(in, stateNeedHuman, w.bob.Self().Fingerprint())...); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
	}
	if _, err := w.alice.store.db.Exec(`DELETE FROM config WHERE k = 'review_notices_settled'`); err != nil {
		t.Fatal(err)
	}
	home := w.alice.home
	w.alice.Close()
	again, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := openNotices(t, again); len(got) != 1 || got[0] != ids[2] {
		t.Fatalf("open after Open %v, want %s", got, ids[2])
	}
	// Once: a notice opened again by hand stays open on the next Open.
	again.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateNeedHuman, ids[0])
	again.Close()
	third, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if got := openNotices(t, third); len(got) != 2 {
		t.Fatalf("the cleanup ran twice: %v", got)
	}
}

// A decision made from a card a newer report replaced still shows its
// outcome on the newer card: results match by request, key and attempt
// across the host's reports.
func TestDecisionResultAcrossReports(t *testing.T) {
	w := newWorld(t, "")
	key := w.alice.Self().Fingerprint()
	id := protocol.NewID()
	decision := protocol.NewID()
	body, _ := json.Marshal(envelope.Decision{Action: "accept", Expect: stateAwaiting, Attempt: 0, Report: "old-report"})
	if _, err := w.alice.store.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at, kind, sub, created_ms, ref_id, ref_fp) VALUES(?, ?, ?, '{}', 'delivered', 1, 'message', ?, 1, ?, ?)`,
		decision, w.bob.Address, string(body), envelope.SubDecision, id, key); err != nil {
		t.Fatal(err)
	}
	status, _ := json.Marshal(envelope.Status{State: "queued", N: 1, At: 100, Decision: decision, Report: "old-report", Attempt: 0})
	if _, err := w.alice.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, verified_by, sub, received_ms, ref_id, ref_fp) VALUES(?, ?, 1, 'message', ?, 1, '', ?, ?, 1, ?, ?)`,
		protocol.NewID(), w.bob.Address, string(status), w.bob.Self().Fingerprint(), envelope.SubStatus, id, key); err != nil {
		t.Fatal(err)
	}
	r := Report{V: 2, At: 200, Host: w.bob.Address, Items: []ReportItem{{ID: id, Key: key, State: stateAwaiting, Attempt: 0}}}
	data, _ := json.Marshal(r)
	got, ok := w.alice.NoticeReport(Message{ID: "new-report", From: w.bob.Address, Body: string(data)})
	if !ok || got.Items[0].Result == nil || got.Items[0].Result.Decision != decision || got.Items[0].Result.State != "queued" {
		t.Fatalf("result across reports: %+v", got.Items)
	}
	// Another attempt keeps its own.
	r.Items[0].Attempt = 1
	data, _ = json.Marshal(r)
	if got, _ := w.alice.NoticeReport(Message{ID: "new-report", From: w.bob.Address, Body: string(data)}); got.Items[0].Result != nil {
		t.Fatalf("another attempt's result: %+v", got.Items[0].Result)
	}
}

// Doctor on a service with nobody to decide says so and gives the steward
// command; with a steward it names them.
func TestDoctorNamesSteward(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	if err := w.bob.SetService(); err != nil {
		t.Fatal(err)
	}
	check := func() Check {
		for _, c := range w.bob.Doctor(tctx(t)) {
			if c.Name == "stewards" {
				return c
			}
		}
		t.Fatal("no stewards check")
		return Check{}
	}
	if c := check(); c.OK || !strings.Contains(c.Result, "agentnet operator grant --person ADDRESS") {
		t.Fatalf("no steward: %+v", c)
	}
	if _, err := w.bob.GrantOperatorPerson(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	me, _, _ := w.alice.Person()
	if c := check(); !c.OK || !strings.Contains(c.Result, me.Label) {
		t.Fatalf("with a steward: %+v", c)
	}
}

// supersedeCase is a shared Go/browser vector: notices from hosts stored
// in order, and the indexes left open (Open: after the one-time cleanup of
// the same notices stored without superseding).
type supersedeCase struct {
	Name    string `json:"name"`
	Notices []struct {
		From    string `json:"from"`
		TS      int64  `json:"ts"`
		Body    string `json:"body"`
		Dismiss bool   `json:"dismiss,omitempty"` // the person dismisses it once stored
	} `json:"notices"`
	Open        []int `json:"open"`
	CleanupOpen []int `json:"cleanup_open"`
}

// TestReviewSupersedeVectors writes the shared vectors for the browser
// engine (AGENTNET_SUPERSEDE_VECTORS=PATH) after checking Go's own result.
func TestReviewSupersedeVectors(t *testing.T) {
	report := func(host string, at int64, items, count int) string {
		r := Report{V: 2, At: at, Host: host, Items: []ReportItem{}, Count: count}
		for i := range items {
			r.Items = append(r.Items, ReportItem{ID: strings.Repeat(string(rune('a'+i)), 32), State: stateAwaiting})
		}
		data, _ := json.Marshal(r)
		return string(data)
	}
	type n = struct {
		From    string `json:"from"`
		TS      int64  `json:"ts"`
		Body    string `json:"body"`
		Dismiss bool   `json:"dismiss,omitempty"`
	}
	cases := []supersedeCase{
		{Name: "newer replaces older", Notices: []n{{"bot/a", 10, report("bot/a", 100, 1, 0), false}, {"bot/a", 11, report("bot/a", 200, 2, 0), false}}, Open: []int{1}, CleanupOpen: []int{1}},
		{Name: "late older stays resolved", Notices: []n{{"bot/a", 11, report("bot/a", 200, 2, 0), false}, {"bot/a", 10, report("bot/a", 100, 1, 0), false}}, Open: []int{0}, CleanupOpen: []int{0}},
		{Name: "hosts apart", Notices: []n{{"bot/a", 10, report("bot/a", 100, 1, 0), false}, {"bot/b", 11, report("bot/b", 50, 1, 0), false}}, Open: []int{0, 1}, CleanupOpen: []int{0, 1}},
		{Name: "settled clears", Notices: []n{{"bot/a", 10, report("bot/a", 100, 1, 0), false}, {"bot/a", 11, report("bot/a", 200, 0, 0), false}}, Open: []int{}, CleanupOpen: []int{1}},
		{Name: "count report", Notices: []n{{"bot/a", 10, report("bot/a", 100, 0, 3), false}, {"bot/a", 11, report("bot/a", 200, 0, 2), false}}, Open: []int{1}, CleanupOpen: []int{1}},
		{Name: "count text by envelope time", Notices: []n{{"bot/a", 300, "2 request(s) wait", false}, {"bot/a", 10, report("bot/a", 200, 1, 0), false}}, Open: []int{0}, CleanupOpen: []int{0}},
		{Name: "same second: later stored", Notices: []n{{"bot/a", 10, report("bot/a", 100, 1, 0), false}, {"bot/a", 10, report("bot/a", 100, 2, 0), false}}, Open: []int{1}, CleanupOpen: []int{1}},
		// Offline is normal: the person dismissed the newer card before a
		// late, older notice came; it must not bring a stale card back.
		{Name: "newer dismissed, late older", Notices: []n{{"bot/a", 11, report("bot/a", 200, 2, 0), true}, {"bot/a", 10, report("bot/a", 100, 1, 0), false}}, Open: []int{}, CleanupOpen: []int{}},
		{Name: "older dismissed, newer", Notices: []n{{"bot/a", 10, report("bot/a", 100, 1, 0), true}, {"bot/a", 11, report("bot/a", 200, 2, 0), false}}, Open: []int{1}, CleanupOpen: []int{1}},
	}
	w := newWorld(t, "")
	for _, c := range cases {
		for _, cleanup := range []bool{false, true} {
			var ids []string
			for _, x := range c.Notices {
				in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: x.From, To: w.alice.Address, TS: x.TS, Kind: envelope.KindMessage, Status: envelope.StatusReviewNotice, Body: x.Body}
				var err error
				if cleanup {
					_, err = w.alice.store.db.Exec(insertInbox, inboxArgs(in, stateNeedHuman, "")...)
				} else {
					err = w.alice.store.addInbox(in, "")
				}
				if err != nil {
					t.Fatal(err)
				}
				if x.Dismiss {
					if _, err := w.alice.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateResolved, in.ID); err != nil {
						t.Fatal(err)
					}
				}
				ids = append(ids, in.ID)
			}
			if cleanup {
				w.alice.store.db.Exec(`DELETE FROM config WHERE k = 'review_notices_settled'`)
				if err := w.alice.store.settleLeftoverNotices(); err != nil {
					t.Fatal(err)
				}
			}
			want := c.Open
			if cleanup {
				want = c.CleanupOpen
			}
			open := map[string]bool{}
			for _, id := range openNotices(t, w.alice) {
				open[id] = true
			}
			for i, id := range ids {
				if open[id] != slices.Contains(want, i) {
					t.Errorf("%s (cleanup %v): notice %d open=%v", c.Name, cleanup, i, open[id])
				}
			}
			w.alice.store.db.Exec(`DELETE FROM inbox WHERE kind = 'message' AND status = ?`, envelope.StatusReviewNotice)
		}
	}
	if path := os.Getenv("AGENTNET_SUPERSEDE_VECTORS"); path != "" {
		data, _ := json.MarshalIndent(cases, "", "  ")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
