package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

// Every recorded state a host tells is one any reader admits (an unknown
// state makes an older reader refuse the whole status), and the told name
// is true: a waiting job is queued work, a held-back reply did run.
func TestStatusOfTellsRecordedStatesTruthfully(t *testing.T) {
	ref := &envelope.Ref{ID: protocol.NewID(), Fingerprint: "11111111-22222222-33333333-44444444"}
	for _, state := range []string{statePending, stateAccepted, stateHeld, stateConvHeld, stateAwaiting, stateRunning, stateCancelReq, stateAnswered,
		stateManual, stateDeclined, stateJobFailed, stateCancelled, stateInterrupt, stateSummary, stateNeedHuman, stateResolved,
		stateAgentWaiting, stateNotRun, stateNotDelivered, stateSteered} {
		public, detail, ok := statusOf(state)
		if !ok {
			continue
		}
		body, _ := json.Marshal(envelope.Status{State: public, N: 1, At: 1, Detail: detail})
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: string(body), Ref: ref}
		if err := envelope.ValidateControl(in); err != nil {
			t.Errorf("%s told as %q, which readers refuse: %v", state, public, err)
		}
	}
	if public, detail, ok := statusOf(stateAgentWaiting); !ok || public != "queued" || detail == "" {
		t.Fatalf("a waiting job is cleared or misnamed: %q %q %v", public, detail, ok)
	}
	public, detail, ok := statusOf(stateNotDelivered)
	if !ok || public == "not_run" || !strings.Contains(detail, "ran") || !topicExecutionClosed(public) {
		t.Fatalf("a held-back reply is told as %q (%q): it ran, and no reply follows", public, detail)
	}
}

// A store last written by v0.8.16 (every step through modelReportSchema)
// gains exactly one status to tell for each retained conversation job of
// this device whose recorded state is terminal or awaits a decision here.
// Nothing else is queued, no state changes and a restart repeats nothing.
func TestRetainedConversationStatusUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	shipped := slices.Index(schema, modelReportSchema) + 1
	old, err := sqlitedb.Open(path, schema[:shipped])
	if err != nil {
		t.Fatal(err)
	}
	const self, selfFP, otherFP = "own/host", "11111111-22222222-33333333-44444444", "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"
	if _, err = old.Exec(`PRAGMA foreign_keys=OFF`); err != nil { // a selected input's binding row is not this test's subject
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO config(k,v) VALUES('address',?)`, self); err != nil {
		t.Fatal(err)
	}
	type row struct {
		state, kind, conv, pid, verified, target string
		local, replica, selected                 bool
		want                                     int
	}
	ok := func(state string, want int) row {
		return row{state: state, kind: envelope.KindTask, conv: "chat", pid: "participant", verified: otherFP, target: self, want: want}
	}
	rows := map[string]row{}
	for _, s := range []string{"failed", "cancelled", "declined", "resolved", "not_run", "not_delivered", "needs_human", "interrupted", "awaiting", "held"} {
		rows["told "+s] = ok(s, 1)
	}
	// Possibly old news after a stop, or told already (answered), or never told.
	for _, s := range []string{"pending", "accepted", "part_waiting", "running", "cancel_requested", "steered", "answered", "conv_held", "manual"} {
		rows["kept "+s] = ok(s, 0)
	}
	change := func(name string, f func(*row)) {
		r := ok(stateJobFailed, 0)
		f(&r)
		rows[name] = r
	}
	change("replica", func(r *row) { r.replica = true })
	change("unverified", func(r *row) { r.verified = "" })
	change("device thread", func(r *row) { r.conv = "" })
	change("no participation", func(r *row) { r.pid = "" })
	change("another device's job", func(r *row) { r.target = "own/other" })
	change("not a request", func(r *row) { r.kind = envelope.KindMessage })
	change("selected input", func(r *row) { r.selected = true })
	change("local from another key", func(r *row) { r.local = true })
	change("local own request", func(r *row) { r.local, r.verified, r.want = true, selfFP, 1 })
	ids := map[string]string{}
	for name, r := range rows {
		id := protocol.NewID()
		ids[name] = id
		target, _ := json.Marshal(envelope.Target{Address: r.target, Fingerprint: selfFP})
		_, err = old.Exec(`INSERT INTO inbox(id,lid,sender,ts,kind,body,received_at,state,verified_by,conv,pid,target,replica,local,attempts)
			VALUES(?,?,'peer/desk',1,?,'retained',1,?,nullif(?,''),nullif(?,''),nullif(?,''),?,?,?,1)`,
			id, protocol.NewID(), r.kind, r.state, r.verified, r.conv, r.pid, string(target), r.replica, r.local)
		if err != nil {
			t.Fatal(name, err)
		}
		if r.selected {
			if _, err = old.Exec(`INSERT INTO reply_receiver_inputs(binding,inbox_id) VALUES('binding',?)`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	old.Close()
	check := func() {
		t.Helper()
		s, err := openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.db.Close()
		for name, r := range rows {
			var due, attempts int
			var state string
			if err = s.db.QueryRow(`SELECT status_due,state,attempts FROM inbox WHERE id=?`, ids[name]).Scan(&due, &state, &attempts); err != nil {
				t.Fatal(name, err)
			}
			if due != r.want || state != r.state || attempts != 1 {
				t.Errorf("%s: due %d state %s attempts %d, want due %d and nothing else changed", name, due, state, attempts, r.want)
			}
		}
	}
	check()
	check() // the appended step is recorded: a restart queues nothing again
}

// An old host recorded a conversation job's failure and told nobody (before
// v0.8.15 local jobs never told a state). Its sibling lists the request as
// pending with no result recorded, not as Waiting. The upgrade step tells the
// recorded failure once, so the sibling closes exactly that request; the
// other request, still waiting on the host, stays pending and nothing runs.
func TestDMAgentRetainedStateRecovery(t *testing.T) {
	w, conv, lids, stop := agentWorld(t)
	phone := linkedVia(t, w.bob, "phone", func(ctx context.Context, id string) error { return w.bob.DecideLink(ctx, id, true) })
	runAgent(t, phone)
	pid := participate(t, w, conv, lids[:1], nil)
	eventually(t, "sibling participation", func() bool { return stateAt(t, phone, pid).Claimable() })
	stop() // an old host: no responder runs
	ask := func() ConvSent {
		t.Helper()
		q, e := w.bob.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), pid, envelope.KindQuestion, "retained exact request")
		if e != nil {
			t.Fatal(e)
		}
		return q
	}
	failed, waiting := ask(), ask()
	flush := func() {
		t.Helper()
		if err := w.bob.FlushOutbox(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	observer := phone
	eventually(t, "sibling requests", func() bool {
		flush()
		_, n := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == failed.LID })
		_, p := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == waiting.LID })
		return n == 1 && p == 1
	})
	if held, _ := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == failed.LID }); held.ID == failed.ID {
		observer = w.alice
	}
	eventually(t, "distinct recipient requests", func() bool {
		flush()
		m, n := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == failed.LID })
		_, p := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == waiting.LID })
		return n == 1 && p == 1 && m.ID != failed.ID
	})
	if _, e := w.bob.store.db.Exec(`UPDATE inbox SET state=?,detail='the agent run failed',attempts=1,status_due=0 WHERE id=?`, stateJobFailed, failed.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := w.bob.store.db.Exec(`UPDATE inbox SET status_due=0 WHERE id=?`, waiting.ID); e != nil {
		t.Fatal(e)
	}
	main := func() ThreadSummary {
		t.Helper()
		msgs, e := observer.ConversationMessages(conv)
		if e != nil {
			t.Fatal(e)
		}
		view := summarizeChatTopicView(conv, msgs, nil, time.Now().Unix(), true)
		if len(view) != 1 {
			t.Fatalf("main flow: %+v", view)
		}
		return view[0]
	}
	if v := main(); len(v.PendingIDs) != 2 || v.Waiting || v.Unconfirmed != 2 {
		t.Fatalf("no word from the host is shown as waiting: %+v", v)
	}
	if _, e := w.bob.store.db.Exec(retainedConversationStatusSchema); e != nil {
		t.Fatal(e)
	}
	due := func(id string) (n int) {
		t.Helper()
		if e := w.bob.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, id).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if due(failed.ID) != 1 || due(waiting.ID) != 0 {
		t.Fatalf("recorded failure due %d, waiting job due %d", due(failed.ID), due(waiting.ID))
	}
	if !w.bob.tellStatus(tctx(t), failed.ID) {
		t.Fatal("recorded state was not told")
	}
	eventually(t, "recorded failure visible", func() bool {
		flush()
		m, n := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == failed.LID })
		return n == 1 && m.Exec != nil && m.Exec.State == "failed"
	})
	waitingCopy, _ := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == waiting.LID })
	if v := main(); !reflect.DeepEqual(v.PendingIDs, []string{waitingCopy.ID}) || v.Waiting || v.Unconfirmed != 1 {
		t.Fatalf("exact pending after the recorded failure: %+v", v)
	}
	var state string
	var attempts int
	if e := w.bob.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE id=?`, failed.ID).Scan(&state, &attempts); e != nil || state != stateJobFailed || attempts != 1 || due(failed.ID) != 0 {
		t.Fatalf("telling changed the job: %s %d %v", state, attempts, e)
	}
	if jobState(t, w.bob, waiting.ID) != stateAgentWaiting {
		t.Fatal("the waiting job ran or changed")
	}
	// Told now, a waiting job is queued work there, never silently cleared.
	w.bob.noteStatus(waiting.ID)
	if !w.bob.tellStatus(tctx(t), waiting.ID) {
		t.Fatal("waiting state was not told")
	}
	var told ConvMessage
	eventually(t, "waiting job visible", func() bool {
		flush()
		told, _ = convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == waiting.LID })
		return told.Exec != nil && told.Exec.State == "queued" && told.Exec.Detail == "waiting here until it may run"
	})
	if v := main(); v.Waiting == told.Exec.Stale || v.Unconfirmed != map[bool]int{true: 1, false: 0}[told.Exec.Stale] {
		t.Fatalf("a queued word is Waiting only while current (stale %v): %+v", told.Exec.Stale, v)
	}
}
