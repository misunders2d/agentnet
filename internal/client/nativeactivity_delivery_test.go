package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

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
	activityAvailability(a, j.From, false)
	for _, k := range []nativeActivity{activityReading, activityEditing, activityCommand, activitySearch, activityAnswer} {
		if !a.store.markNativeActivity(t.Context(), j, k) || !a.tellStatus(t.Context(), j.ID) {
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
	if err := json.Unmarshal([]byte(inner.Body), &st); err != nil || !strings.HasPrefix(st.Detail, "Preparing answer\nReported at ") || st.Attempt != j.nativeAttempt || st.State != stateRunning {
		t.Fatal(st, err)
	}
	var sealedBefore string
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, first.ID).Scan(&sealedBefore); err != nil {
		t.Fatal(err)
	}
	// Connected but stalled: its previous control remains in relay custody.
	for _, k := range []nativeActivity{activityReading, activityEditing, activityCommand, activityAnswer} {
		a.store.markNativeActivity(t.Context(), j, k)
		if !a.tellStatus(t.Context(), j.ID) {
			t.Fatal("deferred telemetry blocked unrelated status work")
		}
	}
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
	// The latest identical value already has its copy; no redundant resend.
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 1 {
		t.Fatal("unchanged latest value was resent", len(h.posted))
	}
	a.store.markNativeActivity(t.Context(), j, activitySearch)
	if !a.tellStatus(t.Context(), j.ID) || len(h.posted) != 2 {
		t.Fatal("new latest value not sent after delivery", len(h.posted))
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
	if a.store.markNativeActivity(t.Context(), j, activityReading) {
		t.Fatal("late native event reopened a terminal request")
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
