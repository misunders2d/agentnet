package client

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func strictCorrection(t *testing.T) (*Agent, job, string) {
	t.Helper()
	a, parent, ref := nativeFollowupFixture(t)
	parent.nativeAttempt = 1
	if _, err := a.store.db.Exec(`UPDATE inbox SET attempts=1,last_attempt_at=unixepoch() WHERE id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	if _, err := a.QueueRequestFollowup(t.Context(), RequestFollowup{Conv: parent.Conv, Ref: ref, Attempt: 1, ID: id, Body: "Use English instead"}); err != nil {
		t.Fatal(err)
	}
	return a, parent, id
}
func TestStrictSteerExactAttemptAndNeverOrdinaryWork(t *testing.T) {
	a, parent, id := strictCorrection(t)
	stored, err := storedRequestFollowup(a.store.db, "in", id)
	if err != nil || stored.Attempt != 1 {
		t.Fatal(stored, err)
	}
	if _, err := a.requestFollowupPrompt(job{ID: id}); err == nil {
		t.Fatal("strict steering became normal work")
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET attempts=2 WHERE id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
		t.Fatal("stale attempt claimed", found, err)
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET attempts=1 WHERE id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	claim, found, err := a.claimNativeFollowup(parent)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if !a.nativeFollowupCurrent(parent, claim.job) {
		t.Fatal("matching attempt unavailable")
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET attempts=2 WHERE id=?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	if a.nativeFollowupCurrent(parent, claim.job) {
		t.Fatal("prewrite fence accepted changed attempt")
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET attempts=1,state=? WHERE id=?`, stateCancelled, parent.ID); err != nil {
		t.Fatal(err)
	}
	if a.nativeFollowupCurrent(parent, claim.job) {
		t.Fatal("cancelled original accepted correction")
	}
}

type refusedFollowup struct {
	ready   chan struct{}
	outcome string
}

func (b refusedFollowup) followupReady() <-chan struct{}               { return b.ready }
func (b refusedFollowup) steer(context.Context, string, string) string { return b.outcome }
func TestStrictSteerRefusalNeverRestoresQueue(t *testing.T) {
	for _, outcome := range []string{steerQueued, steerUnsupported, steerUnknown} {
		t.Run(outcome, func(t *testing.T) {
			a, parent, id := strictCorrection(t)
			ready := make(chan struct{})
			close(ready)
			a.watchNativeFollowups(t.Context(), parent, refusedFollowup{ready, outcome}, nil)
			state, err := a.store.jobState(id)
			if err != nil || state != stateNeedHuman {
				t.Fatal(state, err)
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
				t.Fatal("uncertain/refused replay", found, err)
			}
		})
	}
}
func TestStrictSteerSignedSupportCustodyRelease(t *testing.T) {
	w := newWorld(t, "")
	a, b := w.alice, w.bob
	ref := envelope.FollowupRef{Ref: envelope.Ref{ID: protocol.NewID(), Fingerprint: a.Self().Fingerprint()}, Attempt: 1}
	in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: a.Address, To: b.Address, TS: 1, Kind: envelope.KindQuestion, Body: "Use English", ReplyTo: ref.ID, Followup: &ref}
	recipient, _ := b.Self().Recipient()
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.addOutbox(env, in, "", nil); err != nil {
		t.Fatal(err)
	}
	session := protocol.NewID()
	record := protocol.CapsRecord{Address: b.Address, Session: session, Caps: []string{protocol.CapRequestFollowup}, TS: time.Now().Unix()}
	record.Sign(b.id.Sign)
	raw, _ := json.Marshal(record)
	transport := &invitationCapsTransport{base: a.hub.http.Transport, profile: protocol.Profile{Live: true, Sessions: []string{session}, Caps: []json.RawMessage{raw}}}
	a.hub.http.Transport = transport
	result, err := a.deliver(t.Context(), env, nil)
	if err != nil || result.State != stateConvWaiting || transport.posts != 0 {
		t.Fatal("old reader received strict intent", result, err, transport.posts)
	}
	var before, after []byte
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	a.releaseConv(t.Context(), []string{protocol.FeatureCaps, protocol.FeatureEnv3})
	record.Caps = []string{protocol.CapRequestSteer}
	record.Sign(b.id.Sign)
	raw, _ = json.Marshal(record)
	transport.profile.Caps = []json.RawMessage{raw}
	if !record.Reads(protocol.CapRequestFollowup) {
		t.Fatal("replacement no longer reads legacy")
	}
	a.releaseConv(t.Context(), []string{protocol.FeatureCaps, protocol.FeatureEnv3})
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("release changed sealed bytes", err)
	}
	result, err = a.deliver(t.Context(), env, nil)
	if err != nil || result.State != protocol.StateCustody || transport.posts != 1 {
		t.Fatal("signed support did not release retained intent", result, err, transport.posts)
	}
}
func TestStrictSteerArchiveRequirementRetention(t *testing.T) {
	w := newWorld(t, "")
	ref := envelope.FollowupRef{Ref: envelope.Ref{ID: protocol.NewID(), Fingerprint: w.alice.Self().Fingerprint()}, Attempt: 1}
	item := HistoryItem{Followup: &ref}
	raw, _ := json.Marshal(item)
	required := copyRequirement(outCopy{required: protocol.CapHistoryArchive, in: envelope.Inner{Sub: envelope.SubHistory, Body: string(raw)}})
	base, strict := splitSteerRequirement(required)
	if base != protocol.CapHistoryArchive || !strict {
		t.Fatal(required)
	}
	if !strings.Contains(required, protocol.CapRequestSteer) {
		t.Fatal("strict retention absent")
	}
}
func TestStrictSteerGroupStatusPublication(t *testing.T) {
	a, parent, id := strictCorrection(t)
	body, _ := json.Marshal(envelope.Status{State: "steered", N: 42, At: time.Now().Unix()})
	sent, err := a.sendControlAs(t.Context(), ControlRef{Conv: parent.Conv, ID: id, Fingerprint: a.Self().Fingerprint()}, envelope.SubStatus, string(body), protocol.CapRequestFollowup)
	if err != nil || sent.ID == "" {
		t.Fatal("group steering status not published", sent, err)
	}
}
