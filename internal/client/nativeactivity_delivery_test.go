package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNativeThinkingLatestOfflineCipherRestartAndTerminal(t *testing.T) {
	a, j, h, peerID := activityDeliveryFixture(t)
	activityAvailability(a, j.From, false)
	stamp := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	write := func(summary string, at time.Time) {
		nativeProgressPersisted(t, a.store, j, nativeProgress{kind: activityWorking, summary: summary, at: at})
	}
	unchanged := func(candidate job, summary string, at time.Time) {
		t.Helper()
		before := nativeProgressSnapshot(t, a.store, j)
		changed, err := a.store.markNativeProgressOutcome(t.Context(), candidate, nativeProgress{kind: activityWorking, summary: summary, at: at})
		if changed || err != nil || nativeProgressSnapshot(t, a.store, j) != before {
			t.Fatal("guarded summary was not known unchanged")
		}
	}
	first := "Verifying service recovery status"
	latest := "Reviewing the latest test results"
	for _, summary := range []string{first, latest, first, latest} {
		write(summary, stamp)
		if !a.tellStatus(t.Context(), j.ID) {
			t.Fatal("valid latest summary was not retained")
		}
	}
	if n := activityCopies(t, a, j); n != 0 || len(h.posted) != 0 {
		t.Fatal("summary fell into ordinary offline status backlog", n, len(h.posted))
	}
	// Reopen the actual SQLite store: only latest detail/due persists, no raw
	// reasoning or replay queue. This is store restart evidence, not a model run.
	if err := a.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.store, err = openStore(filepath.Join(a.home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := a.store.db.QueryRow(`SELECT detail FROM inbox WHERE id=?`, j.ID).Scan(&detail); err != nil || detail != "Thinking: "+latest+"\nReported at 2026-10-16T15:00:00Z" {
		t.Fatal("latest summary/time did not survive reopen", detail, err)
	}
	activityAvailability(a, j.From, true)
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 1 {
		t.Fatal("reconnect did not send exactly latest summary", len(h.posted))
	}
	env := h.posted[0]
	inner, err := envelope.Open(env, peerID, j.From, a.Self())
	var status envelope.Status
	if err != nil || json.Unmarshal([]byte(inner.Body), &status) != nil || status.Detail != detail || status.Attempt != j.nativeAttempt || status.State != stateRunning {
		t.Fatal("summary lost normal signed/encrypted binding", status, err)
	}
	var sealedBefore, sealedAfter string
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&sealedBefore); err != nil {
		t.Fatal(err)
	}
	for _, summary := range []string{first, latest, first} {
		write(summary, stamp.Add(time.Second))
		if !a.tellStatus(t.Context(), j.ID) {
			t.Fatal("pending cipher blocked latest summary")
		}
	}
	if n := activityCopies(t, a, j); n != 1 || len(h.posted) != 1 {
		t.Fatal("pending summary cipher grew a backlog", n, len(h.posted))
	}
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&sealedAfter); err != nil || sealedBefore != sealedAfter {
		t.Fatal("pending ciphertext was mutated", err)
	}
	if err := a.store.setOutboxState(env.ID, protocol.StateDelivered, "", protocol.PathRelay); err != nil {
		t.Fatal(err)
	}
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 2 {
		t.Fatal("new latest summary not sent after receipt", len(h.posted))
	}
	if err := a.store.setOutboxState(h.posted[1].ID, protocol.StateDelivered, "", protocol.PathRelay); err != nil {
		t.Fatal(err)
	}
	unchanged(j, first, stamp.Add(2*time.Second))
	if _, err := a.store.db.Exec(`UPDATE inbox SET status_due=status_due+1 WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 2 {
		t.Fatal("timestamp-only duplicate re-sealed")
	}
	// A percent in an otherwise safe summary is literal, not a SQL LIKE pattern.
	for _, summary := range []string{"Checking 100% behavior", "Checking 100X behavior", "Checking 100% behavior"} {
		write(summary, stamp)
	}
	old := j
	old.nativeAttempt++
	unchanged(old, latest, stamp)
	if _, err := a.store.db.Exec(`UPDATE inbox SET state='answered',detail=NULL,status_due=status_due+1 WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	activityAvailability(a, j.From, false)
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 3 {
		t.Fatal("optional summary suppressed offline terminal status", len(h.posted))
	}
	unchanged(j, latest, stamp)
}

func TestNativeThinkingStoreFailureAndDeliveryFailureAreOptional(t *testing.T) {
	a, j, h, _ := activityDeliveryFixture(t)
	p := nativeProgress{kind: activityWorking, summary: "Checking current behavior", at: time.Now()}
	conn, err := a.store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	accepted := a.store.markNativeProgress(t.Context(), j, p)
	conn.Close()
	if accepted || time.Since(start) > time.Second {
		t.Fatal("optional summary store failure blocked work")
	}
	activityAvailability(a, j.From, true)
	h.fail = true
	for _, summary := range []string{"Checking current behavior", "Reviewing current behavior", "Verifying current behavior"} {
		p.summary = summary
		a.store.markNativeProgress(t.Context(), j, p)
		a.tellStatus(t.Context(), j.ID)
	}
	if n := activityCopies(t, a, j); n != 1 {
		t.Fatal("delivery failure queued a summary backlog", n)
	}
	if state, err := a.store.jobState(j.ID); err != nil || state != stateRunning {
		t.Fatal("optional failure changed execution state", state, err)
	}
	if err := a.store.finishJob(j.ID, stateAnswered, "original answer"); err != nil {
		t.Fatal("optional summary failure blocked completion", err)
	}
	if a.store.markNativeProgress(t.Context(), j, p) {
		t.Fatal("summary revived completed attempt")
	}
}

func TestNativeThinkingWatcherCoalescesObservedTime(t *testing.T) {
	a, j, _, _ := activityDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	activity := newRunActivity()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.watchRunActivity(ctx, ctx, j, activity, func() bool { return true })
	}()
	defer func() { cancel(); <-done }()
	wait := func(want string) {
		t.Helper()
		for {
			_, changed := a.Changed()
			var detail string
			if err := a.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, j.ID).Scan(&detail); err != nil {
				t.Fatal(err)
			}
			if detail == want {
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				t.Fatal("watcher did not retain latest summary observation time", detail, want)
			}
		}
	}
	stamp := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	activity.reportProgress(nativeProgress{kind: activityWorking, summary: "Checking current behavior", at: stamp})
	wait("Thinking: Checking current behavior\nReported at 2026-10-16T15:00:00Z")
	for i := range 1000 {
		activity.reportProgress(nativeProgress{kind: activityWorking, summary: "Verifying current behavior", at: stamp.Add(time.Duration(2*i+1) * time.Millisecond)})
		activity.reportProgress(nativeProgress{kind: activityWorking, summary: "Reviewing current behavior", at: stamp.Add(time.Duration(2*i+2) * time.Millisecond)})
	}
	wait("Thinking: Reviewing current behavior\nReported at 2026-10-16T15:00:02Z")
	var due int
	if err := a.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, j.ID).Scan(&due); err != nil || due != 2 {
		t.Fatal("summary burst created extra durable writes", due, err)
	}
}

// Real signed capabilities and sealed controls, without a listener, external
// service or native model. Custody/delivery are explicit fixture receipts.
type activityHub struct {
	t       *testing.T
	peer    identity.Public
	profile protocol.Profile
	realm   string
	posted  []envelope.Envelope
	fail    bool
}

func (h *activityHub) RoundTrip(r *http.Request) (*http.Response, error) {
	var response any
	switch {
	case r.URL.Path == "/v1/version":
		response = protocol.VersionInfo{Protocol: protocol.ProtocolVersion, Version: protocol.Version, RealmID: h.realm, Features: []string{protocol.FeatureEnv3, protocol.FeatureCaps}}
	case r.URL.Path == "/v1/agents/peer/device/profile":
		response = h.profile
	case r.URL.Path == "/v1/agents/peer/device":
		response = protocol.DirectoryEntry{Public: h.peer}
	case r.Method == "POST" && r.URL.Path == "/v1/messages":
		if h.fail {
			return nil, errors.New("isolated fixture delivery unavailable")
		}
		var env envelope.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			h.t.Fatal(err)
		}
		h.posted = append(h.posted, env)
		response = protocol.Receipt{ID: env.ID, State: protocol.StateCustody}
	default:
		return nil, fmt.Errorf("unexpected isolated fixture request: %s %s", r.Method, r.URL.Path)
	}
	raw, _ := json.Marshal(response)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header), Request: r}, nil
}

func activityDeliveryFixture(t *testing.T) (*Agent, job, *activityHub, *identity.Identity) {
	t.Helper()
	a := enrolledAt(t, "https://isolated.invalid", "")
	a.Logf = t.Logf
	t.Cleanup(func() { a.Close() })
	peerID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	peer := peerID.Public("peer/device")
	if err := a.store.pin(peer); err != nil {
		t.Fatal(err)
	}
	session := protocol.NewID()
	rec := protocol.CapsRecord{Address: peer.Address, Session: session, TS: time.Now().Unix(), Caps: []string{protocol.CapHeadless}}
	rec.Sign(peerID.Sign)
	if err := rec.Verify(peer.SignKey); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rec)
	h := &activityHub{t: t, peer: peer, realm: protocol.NewID(), profile: protocol.Profile{Live: false, Sessions: []string{session}, Caps: []json.RawMessage{raw}}}
	if !h.profile.Supports(peer.Address, peer.SignKey, protocol.CapHeadless) {
		t.Fatal("offline fixture lost its retained valid signed support")
	}
	a.hub.http.Transport = h
	if ok, why := a.capSupport(t.Context(), peer.Address, peer, []string{protocol.FeatureEnv3, protocol.FeatureCaps}, protocol.CapHeadless); !ok {
		t.Fatal("fixture reader support refused", why)
	}
	j := job{ID: protocol.NewID(), From: peer.Address, Key: peer.Fingerprint(), Kind: envelope.KindQuestion, nativeAttempt: 1}
	if _, err := a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,attempts) VALUES(?,?,1,'question','isolated request',1,'running',?,1)`, j.ID, j.From, j.Key); err != nil {
		t.Fatal(err)
	}
	return a, j, h, peerID
}

func activityAvailability(a *Agent, peer string, connected bool) {
	presence := protocol.PresenceOffline
	if connected {
		presence = protocol.PresenceConnected
	}
	a.members.mu.Lock()
	a.members.view = MemberView{Current: true, Members: protocol.Members{Members: []protocol.Member{{Address: peer, Presence: presence}}}}
	a.members.mu.Unlock()
}

func activityCopies(t *testing.T, a *Agent, j job) int {
	t.Helper()
	var count int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub='status' AND ref_id=? AND ref_fp=?`, j.ID, j.Key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNativeActivityOfflineRetainedSupportPendingLatestAndTerminal(t *testing.T) {
	a, j, h, peerID := activityDeliveryFixture(t)
	const deliveredDetail = "Preparing answer\nReported at 2026-10-15T22:00:00Z"
	const latestDetail = "Preparing answer\nReported at 2026-10-15T22:00:01Z"
	stamp := time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)
	write := func(k nativeActivity) {
		nativeProgressPersisted(t, a.store, j, nativeProgress{kind: k, at: stamp})
	}
	activityAvailability(a, j.From, false)
	for _, k := range []nativeActivity{activityReading, activityEditing, activityCommand, activitySearch, activityAnswer} {
		write(k)
		if !a.tellStatus(t.Context(), j.ID) {
			t.Fatal("latest optional activity lost")
		}
	}
	if n := activityCopies(t, a, j); n != 0 || len(h.posted) != 0 {
		t.Fatalf("offline retained-capability reader accumulated %d copies (%d posted)", n, len(h.posted))
	}
	var due int
	if err := a.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, j.ID).Scan(&due); err != nil || due == 0 {
		t.Fatal("latest value not retained for reconnect", due, err)
	}
	if _, err := a.store.db.Exec(`UPDATE inbox SET detail=? WHERE id=?`, deliveredDetail, j.ID); err != nil {
		t.Fatal(err)
	}
	activityAvailability(a, j.From, true)
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 1 {
		t.Fatal("reconnect did not send exactly latest value", len(h.posted))
	}
	first := h.posted[0]
	inner, err := envelope.Open(first, peerID, j.From, a.Self())
	if err != nil || !strings.HasPrefix(inner.Body, "{") {
		t.Fatal("native status not signed/encrypted normally", err)
	}
	var st envelope.Status
	if err := json.Unmarshal([]byte(inner.Body), &st); err != nil || st.Detail != deliveredDetail || st.Attempt != j.nativeAttempt || st.State != stateRunning {
		t.Fatal(st, err)
	}
	var sealedBefore string
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, first.ID).Scan(&sealedBefore); err != nil {
		t.Fatal(err)
	}
	// Connected but stalled: its previous control remains in relay custody.
	for _, k := range []nativeActivity{activityReading, activityEditing, activityCommand, activityAnswer} {
		write(k)
		if !a.tellStatus(t.Context(), j.ID) {
			t.Fatal("deferred telemetry blocked unrelated status work")
		}
	}
	// Force different valid report seconds without depending on wall-clock speed.
	if _, err := a.store.db.Exec(`UPDATE inbox SET detail=?,status_due=status_due+1 WHERE id=?`, latestDetail, j.ID); err != nil {
		t.Fatal(err)
	}
	t.Logf("same attempt=%d state=%s: sealed detail=%q latest detail=%q", j.nativeAttempt, stateRunning, st.Detail, latestDetail)
	if n := activityCopies(t, a, j); n != 1 || len(h.posted) != 1 {
		t.Fatalf("stalled reader accumulated %d copies (%d posts)", n, len(h.posted))
	}
	var sealedAfter string
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, first.ID).Scan(&sealedAfter); err != nil || sealedBefore != sealedAfter {
		t.Fatal("coalescing rewrote an immutable sealed copy", err)
	}
	if err := a.store.setOutboxState(first.ID, protocol.StateDelivered, "", protocol.PathRelay); err != nil {
		t.Fatal(err)
	}
	// The same public category already has its copy; a later report second
	// after unseen intervening categories does not warrant another copy.
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 1 {
		t.Fatal("unchanged latest value was resent", len(h.posted))
	}
	for _, candidate := range []struct {
		name, detail string
		attempt      int64
		want         bool
	}{
		{"same category", latestDetail, j.nativeAttempt, false},
		{"changed category", "Searching\nReported at 2026-10-15T22:00:01Z", j.nativeAttempt, true},
		{"invalid report", "Preparing answer\nReported at PRIVATE", j.nativeAttempt, true},
		{"private suffix", "Preparing answer PRIVATE", j.nativeAttempt, true},
		{"next attempt", latestDetail, j.nativeAttempt + 1, true},
	} {
		selected := a.nativeActivityRecipients(t.Context(), ControlRef{ID: j.ID, Fingerprint: j.Key}, envelope.Status{State: stateRunning, Attempt: candidate.attempt, Detail: candidate.detail})
		if send, err := selected(j.From); send != candidate.want || err != nil {
			t.Fatalf("%s: send=%v want=%v err=%v", candidate.name, send, candidate.want, err)
		}
	}
	write(activitySearch)
	var changedDetail string
	if err := a.store.db.QueryRow(`SELECT detail FROM inbox WHERE id=?`, j.ID).Scan(&changedDetail); err != nil || !strings.HasPrefix(changedDetail, "Searching\nReported at ") || !publicActivityDetail(changedDetail) {
		t.Fatal("changed category lost its actual report time", changedDetail, err)
	}
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 2 {
		t.Fatal("new latest value not sent after delivery", len(h.posted))
	}
	changed, err := envelope.Open(h.posted[1], peerID, j.From, a.Self())
	var changedStatus envelope.Status
	if err != nil || json.Unmarshal([]byte(changed.Body), &changedStatus) != nil || changedStatus.Detail != changedDetail {
		t.Fatal("changed category report time changed during delivery", changedStatus, err)
	}
	activityAvailability(a, j.From, false)
	if _, err := a.store.db.Exec(`UPDATE inbox SET state='answered',detail=NULL,status_due=status_due+1 WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 3 {
		t.Fatal("offline/pending telemetry suppressed terminal status", len(h.posted))
	}
	last, err := envelope.Open(h.posted[2], peerID, j.From, a.Self())
	st = envelope.Status{} // omitted terminal detail must not retain prior decode fields
	if err != nil || json.Unmarshal([]byte(last.Body), &st) != nil || st.State != "answered" || st.Detail != "" {
		t.Fatal("terminal status changed", st, err)
	}
	before := nativeProgressSnapshot(t, a.store, j)
	changedOutcome, outcomeErr := a.store.markNativeProgressOutcome(t.Context(), j, nativeProgress{kind: activityReading, at: stamp})
	if changedOutcome || outcomeErr != nil || nativeProgressSnapshot(t, a.store, j) != before {
		t.Fatal("late native event was not known unchanged")
	}
}

func TestNativeActivityDeliveryFailureDoesNotChangeExecution(t *testing.T) {
	a, j, h, _ := activityDeliveryFixture(t)
	activityAvailability(a, j.From, true)
	h.fail = true
	a.store.markNativeActivity(t.Context(), j, activityReading)
	a.tellStatus(t.Context(), j.ID)
	if state, err := a.store.jobState(j.ID); err != nil || state != stateRunning {
		t.Fatal("telemetry failure affected execution", state, err)
	}
	for _, k := range []nativeActivity{activityEditing, activityCommand, activitySearch} {
		a.store.markNativeActivity(t.Context(), j, k)
		a.tellStatus(t.Context(), j.ID)
	}
	if n := activityCopies(t, a, j); n != 1 {
		t.Fatal("delivery failure grew telemetry backlog", n)
	}
	if err := a.store.finishJob(j.ID, stateCancelled, "stopped by owner"); err != nil {
		t.Fatal(err)
	}
	if state, err := a.store.jobState(j.ID); err != nil || state != stateCancelled {
		t.Fatal("telemetry delivery blocked cancellation", state, err)
	}
	if a.store.markNativeActivity(t.Context(), j, activityAnswer) {
		t.Fatal("post-cancel activity persisted")
	}
}

func TestNativeActivityHealthyRecipientSharesPendingSnapshot(t *testing.T) {
	a, j, _, _ := activityDeliveryFixture(t)
	activityAvailability(a, j.From, true)
	a.store.markNativeActivity(t.Context(), j, activityReading)
	a.tellStatus(t.Context(), j.ID) // this reader's valid sealed copy stays in custody
	a.members.mu.Lock()
	a.members.view.Members.Members = append(a.members.view.Members.Members, protocol.Member{Address: "healthy/device", Presence: protocol.PresenceConnected})
	a.members.mu.Unlock()
	selectRecipient := a.nativeActivityRecipients(t.Context(), ControlRef{ID: j.ID, Fingerprint: j.Key}, envelope.Status{State: stateRunning, Attempt: 1, Detail: "Editing files"})
	if send, err := selectRecipient(j.From); send || !errors.Is(err, errNativeActivityDeferred) {
		t.Fatal("pending reader received another copy", send, err)
	}
	// Occupy SQLite after snapshot construction: another recipient must neither
	// rescan this history nor inherit its sibling's pending state.
	conn, err := a.store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if send, err := selectRecipient("healthy/device"); !send || err != nil {
		t.Fatal("healthy sibling blocked or queried history again", send, err)
	}
}

func TestNativeActivityWatcherCoalescesLatestWithoutOutputWrites(t *testing.T) {
	t.Run("after-step-error", testNativeProgressAfterStepError)
	t.Run("before-step-omission", testNativeProgressBeforeStepOmission)
	t.Run("rows-affected-error", testNativeProgressRowsAffectedError)
	t.Run("repeated-unknown", testNativeProgressRepeatedUnknown)
	t.Run("latest-wins", testNativeProgressLatestWins)
	t.Run("unchanged-guards", testNativeProgressUnchangedGuards)
	a, j, _, _ := activityDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	activity := newRunActivity()
	ready, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(done)
		a.watchRunActivity(ctx, ctx, j, activity, func() bool {
			select {
			case ready <- struct{}{}:
			default:
			}
			return true
		})
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("watcher did not start")
	}
	waitDetail := func(label string) {
		t.Helper()
		for {
			_, changed := a.Changed()
			var detail string
			if err := a.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, j.ID).Scan(&detail); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(detail, label+"\nReported at ") {
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				t.Fatal("latest native category not annotated", label)
			}
		}
	}
	activity.report(activityReading)
	waitDetail("Reading files")
	for range 10000 {
		activity.report(activityCommand)
		activity.report(activityEditing)
	}
	w := activityWriter{Writer: io.Discard, activity: activity}
	for range 10000 {
		w.Write([]byte("PRIVATE output"))
	}
	waitDetail("Editing files")
	var due int
	if err := a.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, j.ID).Scan(&due); err != nil || due != 2 {
		t.Fatal("burst/output created extra status writes", due, err)
	}
	if _, err := a.store.db.Exec(`UPDATE inbox SET state='cancel_requested' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	activity.report(activitySearch)
	cancel()
	<-done
	if state, err := a.store.jobState(j.ID); err != nil || state != stateCancelReq {
		t.Fatal("optional watcher affected cancellation", state, err)
	}
}

func TestNativeActivityOnlyOptionalSnapshotsAreGated(t *testing.T) {
	for _, st := range []envelope.Status{
		{State: "answered", Detail: "Reading files"},
		{State: "needs_human", Detail: "Reading files"},
		{State: stateSteered, Detail: "accepted into the current native run"},
		{State: stateRunning, Detail: nativeInputDetail},
		{State: stateRunning, Detail: roomWaitDetail},
		{State: stateRunning, Detail: "Reading files", Decision: "exact-decision"},
	} {
		body, _ := json.Marshal(st)
		if nativeActivityStatus(envelope.SubStatus, string(body)) != nil {
			t.Fatal("durable terminal/approval/steer/decision status became optional", st)
		}
	}
}
