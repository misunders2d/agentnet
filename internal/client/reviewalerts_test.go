package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func reviewAlertItem(a *Agent) ReportItem {
	return ReportItem{ID: protocol.NewID(), From: a.Address, Key: a.Self().Fingerprint(), Kind: envelope.KindTask, State: stateAwaiting, Blocker: BlockerAcceptance, Since: 1, Attempt: 0, Excerpt: "PRIVATE prompt must never be notified", Actionable: true}
}
func insertReviewAlert(t *testing.T, a *Agent, host, key, body string) string {
	t.Helper()
	id := protocol.NewID()
	_, err := a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,status,state,verified_by) VALUES(?,?,?,?,?,?,?,?,?)`, id, host, 1, envelope.KindMessage, body, time.Now().Unix(), envelope.StatusReviewNotice, stateNeedHuman, key)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func reviewAlertBody(t *testing.T, host string, items ...ReportItem) string {
	t.Helper()
	b, err := json.Marshal(Report{V: 2, At: 1, Host: host, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReviewReportSilentMalformedAndCountOnly(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	it := reviewAlertItem(w.alice)
	falseItem := it
	falseItem.Actionable = false
	malformedItem := it
	malformedItem.Key = "not-a-key"
	spoof := reviewAlertBody(t, "other/host", it)
	for _, body := range []string{"5 requests wait", `{"v":2,"actionable":true`, reviewAlertBody(t, w.alice.Address, falseItem), reviewAlertBody(t, w.alice.Address, malformedItem), spoof} {
		insertReviewAlert(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), body)
		w.bob.notifyReview()
	}
	if n.count() != 0 {
		t.Fatalf("silent report alerted: %s", n.last())
	}
	if ids, total, err := w.bob.store.unnotified(); err != nil || len(ids) != 0 || total != 0 {
		t.Fatalf("remote rows counted locally %v %d %v", ids, total, err)
	}
	// A local held request remains exactly one decision despite all notices.
	id := protocol.NewID()
	_, err := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, w.alice.Address, 1, envelope.KindQuestion, "local PRIVATE", 1, stateHeld, w.alice.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	w.bob.notifyReview()
	if n.count() != 1 || !strings.HasPrefix(n.last(), "AgentNet: 1 request needs your decision") || strings.Contains(n.last(), "PRIVATE") {
		t.Fatalf("local alert %s", n.last())
	}
}

func TestReviewReportSnapshotCountDedupAndRestart(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	host, key := w.alice.Address, w.alice.Self().Fingerprint()
	a, b, c := reviewAlertItem(w.alice), reviewAlertItem(w.alice), reviewAlertItem(w.alice)
	insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, a, b))
	w.bob.notifyReview()
	if n.count() != 1 || !strings.Contains(n.last(), "2 remote request(s) on "+host) || strings.Contains(n.last(), "PRIVATE") {
		t.Fatalf("snapshot count/privacy: %s", n.last())
	}
	// Reordered JSON and repeated snapshots cover same exact transitions.
	raw := fmt.Sprintf(`{"items":[%s,%s],"host":%q,"at":2,"v":2}`, mustAlertJSON(t, b), mustAlertJSON(t, a), host)
	insertReviewAlert(t, w.bob, host, key, raw)
	w.bob.notifyReview()
	if n.count() != 1 {
		t.Fatal("same snapshot re-alerted")
	}
	insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, a, b, c))
	w.bob.notifyReview()
	if n.count() != 2 || !strings.Contains(n.last(), "3 remote request(s)") {
		t.Fatalf("new item count %s", n.last())
	}
	// Actual store reopen proves flags/report history, not memory, deduplicate.
	home := w.bob.home
	if err := w.bob.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	n2 := fakeNotify(reopened)
	insertReviewAlert(t, reopened, host, key, reviewAlertBody(t, host, c, b, a))
	reopened.notifyReview()
	if n2.count() != 0 {
		t.Fatal("restart repeated snapshot alerted")
	}
	a.Attempt++
	insertReviewAlert(t, reopened, host, key, reviewAlertBody(t, host, a, b, c))
	reopened.notifyReview()
	if n2.count() != 1 {
		t.Fatal("new attempt silent")
	}
	a.State = stateNeedHuman
	a.Blocker = BlockerNeedsHuman
	insertReviewAlert(t, reopened, host, key, reviewAlertBody(t, host, a, b, c))
	reopened.notifyReview()
	if n2.count() != 2 {
		t.Fatal("new state silent")
	}
	a.State = stateAwaiting
	a.Blocker = BlockerAcceptance
	insertReviewAlert(t, reopened, host, key, reviewAlertBody(t, host, a, b, c))
	reopened.notifyReview()
	if n2.count() != 3 {
		t.Fatal("return to review suppressed by old history")
	}
}
func mustAlertJSON(t *testing.T, it ReportItem) string {
	t.Helper()
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReviewReportExactKeyPartitionsAndMixedCount(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	host, key := w.alice.Address, w.alice.Self().Fingerprint()
	it := reviewAlertItem(w.alice)
	insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
	w.bob.notifyReview()
	it.Key = w.bob.Self().Fingerprint()
	insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
	w.bob.notifyReview()
	if n.count() != 2 {
		t.Fatal("request key conflated")
	}
	insertReviewAlert(t, w.bob, host, w.bob.Self().Fingerprint(), reviewAlertBody(t, host, it))
	w.bob.notifyReview()
	if n.count() != 3 {
		t.Fatal("host fingerprint conflated")
	}
	id := protocol.NewID()
	_, err := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, host, 1, envelope.KindTask, "PRIVATE local", 1, stateAwaiting, key)
	if err != nil {
		t.Fatal(err)
	}
	w.bob.notifyReview()
	if n.count() != 4 || !strings.Contains(n.last(), "1 local request(s)") || !strings.Contains(n.last(), "2 remote request(s) on "+host) || strings.Contains(n.last(), "PRIVATE") {
		t.Fatalf("mixed counts %s", n.last())
	}
}

func TestReviewReportFailedNotifierDoesNotPoll(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	n.setFail(errors.New("synthetic notifier failure"))
	host, key := w.alice.Address, w.alice.Self().Fingerprint()
	it := reviewAlertItem(w.alice)
	insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
	w.bob.notifyReview()
	insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
	w.bob.notifyReview()
	if n.count() != 1 {
		t.Fatal("failed unchanged snapshot retried notifier")
	}
	n.setFail(nil)
	w.bob.notifyTried = nil
	w.bob.notifyReview()
	if n.count() != 2 {
		t.Fatal("restart failed notification not retried")
	}
	insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
	w.bob.notifyReview()
	if n.count() != 2 {
		t.Fatal("successful retry not durable")
	}
}

func TestReviewReportBoundSettlementOnly(t *testing.T) {
	for _, change := range []string{"success", "refused", "foreign-host-key", "foreign-request-key", "foreign-report", "mismatched-status-report", "foreign-attempt", "foreign-state", "untrusted-body-result"} {
		t.Run(change, func(t *testing.T) {
			w := newWorld(t, "")
			host, key := w.alice.Address, w.alice.Self().Fingerprint()
			it := reviewAlertItem(w.alice)
			if change == "untrusted-body-result" {
				it.Result = &DecisionResult{State: "queued", Decision: protocol.NewID(), At: 1}
			}
			report := insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
			decisionID := protocol.NewID()
			d := envelope.Decision{Action: "accept", Expect: it.State, Attempt: it.Attempt, Report: report}
			status := envelope.Status{State: "queued", At: 1, Decision: decisionID, Report: report, Attempt: it.Attempt}
			statusKey, requestKey := key, it.Key
			switch change {
			case "refused":
				status.Refused = "not allowed"
			case "foreign-host-key":
				statusKey = w.bob.Self().Fingerprint()
			case "foreign-request-key":
				requestKey = w.bob.Self().Fingerprint()
			case "foreign-report":
				d.Report = protocol.NewID()
				status.Report = d.Report
			case "mismatched-status-report":
				status.Report = protocol.NewID()
			case "foreign-attempt":
				d.Attempt++
				status.Attempt = d.Attempt
			case "foreign-state":
				d.Expect = stateNeedHuman
			}
			db, _ := json.Marshal(d)
			sb, _ := json.Marshal(status)
			if change != "untrusted-body-result" {
				_, err := w.bob.store.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,sub,ref_id,ref_fp) VALUES(?,?,?,?,?,?,?,?,?)`, decisionID, host, string(db), "{}", "custody", 1, envelope.SubDecision, it.ID, requestKey)
				if err != nil {
					t.Fatal(err)
				}
				_, err = w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,sub,ref_id,ref_fp) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, protocol.NewID(), host, 1, envelope.KindMessage, string(sb), 1, stateAnswered, statusKey, envelope.SubStatus, it.ID, requestKey)
				if err != nil {
					t.Fatal(err)
				}
			}
			insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
			alerts, err := w.bob.remoteReviewAlerts()
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if change == "success" {
				want = 0
			}
			if alerts.Counts[host] != want || len(alerts.Fresh) != want {
				t.Fatalf("count %d fresh %d want %d", alerts.Counts[host], len(alerts.Fresh), want)
			}
			if change == "success" {
				it.Attempt++
				insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
				alerts, err = w.bob.remoteReviewAlerts()
				if err != nil || alerts.Counts[host] != 1 {
					t.Fatal("settlement widened to new attempt", err)
				}
				it.Attempt--
				it.State, it.Blocker = stateNeedHuman, BlockerNeedsHuman
				insertReviewAlert(t, w.bob, host, key, reviewAlertBody(t, host, it))
				alerts, err = w.bob.remoteReviewAlerts()
				if err != nil || alerts.Counts[host] != 1 {
					t.Fatal("settlement widened to new state", err)
				}
			}
		})
	}
}

func TestReviewReportClickOpensActivityReadOnly(t *testing.T) {
	if clickOS != "linux" {
		t.Skip("Linux URL routing")
	}
	log := stubTerminal(t)
	t.Setenv("INVOCATION_ID", "")
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	w.bob.openConv = func(string) []string { return []string{terminalLauncher, "agentnet://open#workspace=default"} }
	id := insertReviewAlert(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), reviewAlertBody(t, w.alice.Address, reviewAlertItem(w.alice)))
	w.bob.notifyReview()
	if n.lastClick() == nil {
		t.Fatal("no click")
	}
	n.lastClick()()
	eventually(t, "remote Activity opened", func() bool { b, _ := os.ReadFile(log); return strings.Contains(string(b), "#review") })
	argv, _ := w.bob.reviewClick(id)
	if len(argv) < 2 || argv[1] != "agentnet://open#review&workspace=default" {
		t.Fatalf("explicit report click dead end %v", argv)
	}
	if state, err := w.bob.store.jobState(id); err != nil || state != stateNeedHuman {
		t.Fatalf("click changed state %s %v", state, err)
	}
}
