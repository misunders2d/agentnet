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
// this device whose recorded state is terminal, awaits a decision here, or
// waits here to run (no shipped version told that one). Nothing else is
// queued, no state changes and a restart repeats nothing.
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
	for _, s := range []string{"failed", "cancelled", "declined", "resolved", "not_run", "not_delivered", "needs_human", "interrupted", "awaiting", "held", "part_waiting"} {
		rows["told "+s] = ok(s, 1)
	}
	// Possibly old news after a stop, or told already (queued once accepted;
	// answered), or never told.
	for _, s := range []string{"pending", "accepted", "running", "cancel_requested", "steered", "answered", "conv_held", "manual"} {
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

// A connected host holds two requests for its agent that may not run yet (no
// responder chosen there, as when its agent is busy): one asked by the other
// member, one by the host's own person. The host tells each as queued from
// its admission, so the requester and a linked sibling read the topic as
// Waiting, not unconfirmed. Nothing runs, and a restart tells nothing again.
func TestConversationRequestWaitingHereIsTold(t *testing.T) {
	w, conv, lids, stopBob := agentWorld(t)
	phone := linkedVia(t, w.bob, "phone", func(ctx context.Context, id string) error { return w.bob.DecideLink(ctx, id, true) })
	pid := participate(t, w, conv, lids[:1], nil)
	eventually(t, "sibling participation", func() bool { return stateAt(t, phone, pid).Claimable() })
	remote, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "queued at bob for alice")
	if err != nil {
		t.Fatal(err)
	}
	own, err := w.bob.AskAgent(tctx(t), pid, envelope.KindQuestion, "queued at bob for bob")
	if err != nil {
		t.Fatal(err)
	}
	var held []string // the host's own job rows
	eventually(t, "the host holds both", func() bool {
		held = held[:0]
		for _, lid := range []string{remote.LID, own.LID} {
			m, n := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == lid })
			if n != 1 || m.Job != stateAgentWaiting {
				return false
			}
			held = append(held, m.ID)
		}
		return true
	})
	queued := func(a *Agent) bool {
		for _, lid := range []string{remote.LID, own.LID} {
			m, n := convMsg(t, a, conv, func(m ConvMessage) bool { return m.LID == lid })
			if n != 1 || m.Exec == nil || m.Exec.State != "queued" || m.Exec.Detail != "waiting here until it may run" || m.Exec.Stale {
				return false
			}
		}
		return true
	}
	for _, a := range []*Agent{w.alice, phone} {
		eventually(t, "the host's queued word at "+a.Address, func() bool { return queued(a) })
		msgs, err := a.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		view := summarizeChatTopicView(conv, msgs, nil, time.Now().Unix(), true)
		if len(view) != 1 || len(view[0].PendingIDs) != 2 || !view[0].Waiting || view[0].Unconfirmed != 0 {
			t.Fatalf("%s: requests queued on a connected host are not Waiting: %+v", a.Address, view)
		}
	}
	told := func() (n int) {
		t.Helper()
		if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND ref_id IN (?,?)`, envelope.SubStatus, remote.LID, own.LID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := told()
	stopBob()
	runAgent(t, w.bob)
	eventually(t, "the restarted host connected", func() bool { return w.bob.FlushOutbox(tctx(t)) == nil })
	time.Sleep(300 * time.Millisecond)
	if n := told(); n != before {
		t.Fatalf("a restart told the waiting requests again: %d status copies, had %d", n, before)
	}
	for _, id := range held {
		var state string
		var attempts, due int
		if err := w.bob.store.db.QueryRow(`SELECT state,attempts,status_due FROM inbox WHERE id=?`, id).Scan(&state, &attempts, &due); err != nil {
			t.Fatal(err)
		}
		if state != stateAgentWaiting || attempts != 0 || due != 0 {
			t.Fatalf("telling changed or ran %s: %s attempts %d due %d", id, state, attempts, due)
		}
	}
}

// Every remote path admitting a request for this device's agent (a member's
// turn, a human guest's, an outside participation's) stores it through
// addConvInbox, which keeps the status mark with the row in the same
// transaction: one for a question or task waiting here, none for history,
// a request held for the person or a message, and none again for a copy
// already stored.
func TestAdmissionStoresWaitingStatusMark(t *testing.T) {
	w, conv, _, stopBob := agentWorld(t)
	pid := participate(t, w, conv, nil, nil)
	stopBob() // nothing tells or claims meanwhile
	aliceFP := w.alice.Self().Fingerprint()
	for _, c := range []struct {
		kind, state string
		want        int
	}{
		{envelope.KindQuestion, stateAgentWaiting, 1},
		{envelope.KindTask, stateAgentWaiting, 1},
		{envelope.KindMessage, stateAgentWaiting, 0},
		{envelope.KindQuestion, "", 0}, // another device's agent's: history here
		{envelope.KindTask, stateConvHeld, 0},
	} {
		in := agentRequest(t, w, conv, pid, c.kind)
		for i, want := range []string{admitted, admittedAgain} {
			res, err := w.bob.store.addConvInbox(in, aliceFP, c.state, false, nil)
			if err != nil || res != want {
				t.Fatalf("%s %q admission %d: %s %v", c.kind, c.state, i, res, err)
			}
			var due int
			if err := w.bob.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, in.ID).Scan(&due); err != nil || due != c.want {
				t.Fatalf("%s %q admission %d: status due %d (%v), want %d", c.kind, c.state, i, due, err, c.want)
			}
		}
	}
}

// An old host recorded a conversation job's failure and told nobody (before
// v0.8.15 local jobs never told a state). Its sibling lists the request as
// pending with no result recorded, not as Waiting. The upgrade step tells the
// recorded failure once, so the sibling closes exactly that request; the
// other request, still waiting on the host, stays pending and nothing runs.
func TestDMAgentRetainedStateRecovery(t *testing.T) {
	w, conv, lids, stop := agentWorld(t)
	phone := linkedVia(t, w.bob, "phone", func(ctx context.Context, id string) error { return w.bob.DecideLink(ctx, id, true) })
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
	if _, e := w.bob.store.db.Exec(`UPDATE inbox SET status_due=0 WHERE id=?`, waiting.ID); e != nil { // an old host noted nothing at admission
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
	// The next upgrade step tells the waiting job as queued work there, never
	// silently cleared; the recorded failure is not told again.
	if _, e := w.bob.store.db.Exec(waitingConversationStatusSchema); e != nil {
		t.Fatal(e)
	}
	if due(waiting.ID) != 1 || due(failed.ID) != 0 {
		t.Fatalf("waiting job due %d, recorded failure due %d", due(waiting.ID), due(failed.ID))
	}
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
