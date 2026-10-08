package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type queuedRetractionTransport struct {
	base    http.RoundTripper
	id      string
	posts   atomic.Int32
	after   func(*http.Response) error
	offline atomic.Bool
}

func (x *queuedRetractionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if x.offline.Load() {
		return nil, errors.New("synthetic outage")
	}
	if r.URL.Path != "/v1/messages" {
		return x.base.RoundTrip(r)
	}
	var env envelope.Envelope
	if r.GetBody != nil {
		body, e := r.GetBody()
		if e != nil {
			return nil, e
		}
		data, e := io.ReadAll(body)
		body.Close()
		if e != nil {
			return nil, e
		}
		json.Unmarshal(data, &env)
	}
	resp, e := x.base.RoundTrip(r)
	if env.ID == x.id {
		x.posts.Add(1)
		if e == nil && x.after != nil {
			if e = x.after(resp); e != nil {
				resp.Body.Close()
				return nil, e
			}
		}
	}
	return resp, e
}

func queueRequestFixture(t *testing.T, a, b *Agent, kind string) envelope.Envelope {
	t.Helper()
	key, e := a.sendKey(tctx(t), b.Address)
	if e != nil {
		t.Fatal(e)
	}
	recipient, e := key.Recipient()
	if e != nil {
		t.Fatal(e)
	}
	in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: a.Address, To: b.Address, TS: time.Now().Unix(), Kind: kind, Body: "synthetic queued execution input"}
	env, e := envelope.Seal(in, a.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.store.addOutbox(env, in, "", nil); e != nil {
		t.Fatal(e)
	}
	return env
}

func waitRetractionCaps(t *testing.T, a, b *Agent) {
	t.Helper()
	eventually(t, "recipient control capability", func() bool {
		features, e := a.relayFeatures(tctx(t))
		if e != nil {
			return false
		}
		ok, _ := a.capSupport(tctx(t), b.Address, b.Self(), features, protocol.CapControl)
		return ok
	})
}

func requestViewFlag(view any, field string) bool {
	data, _ := json.Marshal(view)
	var values map[string]any
	json.Unmarshal(data, &values)
	return values[field] == true
}

func TestQueuedRequestRetractionStopsRestartHandover(t *testing.T) {
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, "")
			runAgent(t, w.bob)
			waitRetractionCaps(t, w.alice, w.bob)
			env := queueRequestFixture(t, w.alice, w.bob, kind)
			transport := &queuedRetractionTransport{base: w.alice.hub.http.Transport, id: env.ID}
			w.alice.hub.http.Transport = transport
			transport.offline.Store(true)
			ref, e := w.alice.RefOf("", env.ID, "out")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = w.alice.Retract(tctx(t), ref, ""); e != nil {
				t.Fatal(e)
			}
			transport.offline.Store(false)
			if e = w.alice.FlushOutbox(tctx(t)); e != nil {
				t.Fatal(e)
			}
			if transport.posts.Load() != 0 {
				t.Fatal("deleted queued request was handed over")
			}
			m := legacyView(t, w.alice, env.ID)
			if !requestViewFlag(m, "send_stopped") || requestViewFlag(m, "delivery_uncertain") || m.State != stateNotDelivered {
				t.Fatalf("local cancellation: %+v", m)
			}
			res, e := w.alice.deliver(tctx(t), env, nil)
			if e != nil || res.State != stateNotDelivered {
				t.Fatalf("stale delivery snapshot: %+v %v", res, e)
			}
			home := w.alice.home
			w.alice.Close()
			reopened, e := Open(home)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			transport.base = reopened.hub.http.Transport
			reopened.hub.http.Transport = transport
			if e = reopened.FlushOutbox(tctx(t)); e != nil {
				t.Fatal(e)
			}
			if transport.posts.Load() != 0 {
				t.Fatal("restart handed over deleted request")
			}
			if e = reopened.store.applyReceipt(protocol.ReceiptEvent{ID: env.ID, State: protocol.StateDelivered, Seq: 1}); e != nil {
				t.Fatal(e)
			}
			if m = legacyView(t, reopened, env.ID); m.State != stateNotDelivered {
				t.Fatal("a receipt cannot assert delivery of a request proven never handed over")
			}
		})
	}
}

func TestQueuedRequestRetractionPreservesUncertainHandover(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "custody", true: "lost-response"}[lost], func(t *testing.T) {
			w := newWorld(t, "")
			runAgent(t, w.bob)
			waitRetractionCaps(t, w.alice, w.bob)
			env := queueRequestFixture(t, w.alice, w.bob, envelope.KindTask)
			entered, release := make(chan struct{}), make(chan struct{})
			transport := &queuedRetractionTransport{base: w.alice.hub.http.Transport, id: env.ID, after: func(*http.Response) error {
				close(entered)
				<-release
				if lost {
					return errors.New("synthetic response lost")
				}
				return nil
			}}
			w.alice.hub.http.Transport = transport
			done := make(chan error, 1)
			go func() { _, e := w.alice.deliver(tctx(t), env, nil); done <- e }()
			select {
			case <-entered:
			case <-tctx(t).Done():
				t.Fatal("handover did not start")
			}
			ref, e := w.alice.RefOf("", env.ID, "out")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = w.alice.Retract(tctx(t), ref, ""); e != nil {
				t.Fatal(e)
			}
			m := legacyView(t, w.alice, env.ID)
			if !requestViewFlag(m, "send_stopped") || !requestViewFlag(m, "delivery_uncertain") {
				t.Fatalf("uncertain cancellation: %+v", m)
			}
			close(release)
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			if e = w.alice.FlushOutbox(tctx(t)); e != nil {
				t.Fatal(e)
			}
			if transport.posts.Load() != 1 {
				t.Fatal("deleted uncertain request was retried")
			}
			m = legacyView(t, w.alice, env.ID)
			if !lost && (m.State != protocol.StateCustody || requestViewFlag(m, "delivery_uncertain")) {
				t.Fatalf("proven custody: %+v", m)
			}
			for i, state := range []string{protocol.StateDelivered, protocol.StateQuarantined, protocol.StateExpired} {
				if e = w.alice.store.applyReceipt(protocol.ReceiptEvent{ID: env.ID, State: state, Seq: int64(i + 1)}); e != nil {
					t.Fatal(e)
				}
				if m = legacyView(t, w.alice, env.ID); m.State != protocol.StateDelivered || requestViewFlag(m, "delivery_uncertain") {
					t.Fatalf("receipt monotonicity: %+v", m)
				}
			}
		})
	}
}

func TestQueuedRequestRetractionFanoutLegacyAndPrivateWrapper(t *testing.T) {
	w := newWorld(t, "")
	originals := []envelope.Envelope{queueRequestFixture(t, w.alice, w.bob, envelope.KindQuestion), queueRequestFixture(t, w.alice, w.bob, envelope.KindTask)}
	wrapper := queueRequestFixture(t, w.alice, w.bob, envelope.KindTask)
	untouched := queueRequestFixture(t, w.alice, w.bob, envelope.KindQuestion)
	conv, lid := protocol.NewID(), protocol.NewID()
	for _, env := range originals {
		if _, e := w.alice.store.db.Exec(`UPDATE outbox SET conv=?,lid=?,kind=?,created_ms=created_at*1000 WHERE id=?`, conv, lid, env.Kind, env.ID); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := w.alice.store.db.Exec(`UPDATE outbox SET state='custody',handover_started=1 WHERE id=?`, originals[0].ID); e != nil {
		t.Fatal(e)
	}
	if _, e := w.alice.store.db.Exec(`UPDATE outbox SET handover_started=NULL WHERE id=?`, originals[1].ID); e != nil {
		t.Fatal(e)
	}
	receiver, _ := json.Marshal(map[string]any{"remote": map[string]any{"route": map[string]any{"delegation_id": wrapper.ID}, "request": map[string]any{"from_key": w.alice.Self().Fingerprint(), "kind": "task"}}})
	if _, e := w.alice.store.db.Exec(`INSERT INTO reply_receivers(id,conv,request_ref,receiver,created_at) VALUES(?,?,?,?,?)`, protocol.NewID(), conv, lid, string(receiver), time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	ref := ControlRef{Conv: conv, ID: lid, Fingerprint: w.bob.Self().Fingerprint()}
	if e := w.alice.stopRetractedRequests(ref); e != nil {
		t.Fatal(e)
	}
	state, _, _, _ := w.alice.store.outboxState(originals[1].ID)
	if state != stateQueued {
		t.Fatal("another sender key stopped a local request")
	}
	ref.Fingerprint = w.alice.Self().Fingerprint()
	if e := w.alice.stopRetractedRequests(ref); e != nil {
		t.Fatal(e)
	}
	for _, env := range append(originals, wrapper, untouched) {
		var state, detail string
		var stopped, uncertain bool
		if e := w.alice.store.db.QueryRow(`SELECT state,coalesce(error,''),send_stopped,send_stopped=1 AND state='not_delivered' AND coalesce(handover_started,1)=1 FROM outbox WHERE id=?`, env.ID).Scan(&state, &detail, &stopped, &uncertain); e != nil {
			t.Fatal(e)
		}
		switch env.ID {
		case originals[0].ID:
			if state != "custody" || stopped {
				t.Fatal("fanout custody was falsely canceled")
			}
		case originals[1].ID:
			if state != stateNotDelivered || !stopped || !uncertain {
				t.Fatal("legacy handover was falsely proven unsent")
			}
		case wrapper.ID:
			if state != stateNotDelivered || !stopped || uncertain {
				t.Fatal("private queued wrapper was not stopped before handover")
			}
		case untouched.ID:
			if state != stateQueued || stopped {
				t.Fatal("unrelated request stopped")
			}
		}
	}
	messages, e := w.alice.store.convMessages(conv, w.alice.Address, w.alice.Self().Fingerprint(), map[string]bool{})
	if e != nil {
		t.Fatal(e)
	}
	if len(messages) != 1 || messages[0].Delivery != "custody" || messages[0].SendStopped || messages[0].DeliveryUncertain {
		t.Fatalf("fanout summary erased proven custody: %+v", messages)
	}
	if len(messages[0].Copies) != 2 {
		t.Fatal("fanout per-copy proof missing")
	}
	var uncertainCopy bool
	for _, copy := range messages[0].Copies {
		uncertainCopy = uncertainCopy || copy.DeliveryUncertain
	}
	if !uncertainCopy {
		t.Fatal("fanout details hid legacy delivery uncertainty")
	}
}

func TestQueuedRequestRetractionDirectFinalFence(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0"})
	waitRetractionCaps(t, w.alice, w.bob)
	env := queueRequestFixture(t, w.alice, w.bob, envelope.KindTask)
	sessions, e := w.alice.sessions(tctx(t), w.bob.Address)
	if e != nil {
		t.Fatal(e)
	}
	var route protocol.SessionAd
	for _, s := range sessions {
		if s.Ad.Endpoint != "" {
			route = s.Ad
		}
	}
	if route.Endpoint == "" {
		t.Fatal("no direct route")
	}
	relay := &queuedRetractionTransport{base: w.alice.hub.http.Transport, id: env.ID}
	w.alice.hub.http.Transport = relay
	oldWrap := wrapTransport
	defer func() { wrapTransport = oldWrap }()
	var direct *directFenceTransport
	wrapTransport = func(base http.RoundTripper) http.RoundTripper {
		direct = &directFenceTransport{base: base}
		ref, e := w.alice.RefOf("", env.ID, "out")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.alice.Retract(tctx(t), ref, ""); e != nil {
			t.Fatal(e)
		}
		return direct
	}
	result, e := w.alice.deliver(tctx(t), env, &route)
	if e != nil || result.State != stateNotDelivered || direct == nil || direct.posts != 0 || relay.posts.Load() != 0 {
		t.Fatalf("direct deletion gate/fallback: %+v %v direct=%+v relay=%d", result, e, direct, relay.posts.Load())
	}
}

func TestQueuedRequestRetractionParticipationStillNeedsControlCapability(t *testing.T) {
	w := newWorld(t, "")
	original := queueRequestFixture(t, w.alice, w.bob, envelope.KindQuestion)
	key, e := w.alice.sendKey(tctx(t), w.bob.Address)
	if e != nil {
		t.Fatal(e)
	}
	recipient, e := key.Recipient()
	if e != nil {
		t.Fatal(e)
	}
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubRetraction, Body: `{}`, Ref: &envelope.Ref{ID: original.ID, Fingerprint: w.alice.Self().Fingerprint()}}
	env, e := envelope.Seal(in, w.alice.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.store.addConvOutbox([]outCopy{{env: env, in: in, state: stateQueued, required: protocol.CapHumanParticipation, recipientFP: key.Fingerprint()}}, envelope.Inner{}, nil, ""); e != nil {
		t.Fatal(e)
	}
	session := protocol.NewID()
	record := protocol.CapsRecord{Address: w.bob.Address, Session: session, Caps: []string{protocol.CapEnv2, protocol.CapHumanParticipation, protocol.CapGroupHumanParticipation}, TS: time.Now().Unix()}
	slices.Sort(record.Caps)
	record.Sign(w.bob.id.Sign)
	caps, _ := json.Marshal(record)
	transport := &invitationCapsTransport{base: w.alice.hub.http.Transport, profile: protocol.Profile{Live: true, Sessions: []string{session}, Caps: []json.RawMessage{caps}}}
	w.alice.hub.http.Transport = transport
	if !transport.profile.Supports(key.Address, key.SignKey, protocol.CapHumanParticipation) || transport.profile.Supports(key.Address, key.SignKey, protocol.CapControl) {
		t.Fatal("fixture must prove participation and omit controls")
	}
	if e = w.alice.requireParticipationCaps(tctx(t), key, protocol.CapHumanParticipation); e != nil {
		t.Fatalf("participation prerequisite: %v", e)
	}
	result, e := w.alice.deliver(tctx(t), env, nil)
	if e != nil || result.State != stateConvWaiting || transport.posts != 0 {
		t.Fatalf("participation-only reader received a control: %+v %v posts=%d", result, e, transport.posts)
	}
}

func TestQueuedRequestRetractionLookupFailureStopsHandover(t *testing.T) {
	w := newWorld(t, "")
	env := queueRequestFixture(t, w.alice, w.bob, envelope.KindQuestion)
	// Keep the queued row and receiver lookup intact, but make the exact
	// retraction evidence query unavailable at the final handover fence.
	if _, e := w.alice.store.db.Exec(`ALTER TABLE inbox RENAME TO unavailable_inbox`); e != nil {
		t.Fatal(e)
	}
	allowed, e := w.alice.beginHandover(env)
	if allowed || e == nil {
		t.Fatalf("unreadable retraction evidence allowed handover: %v %v", allowed, e)
	}
	var started int
	if e = w.alice.store.db.QueryRow(`SELECT handover_started FROM outbox WHERE id=?`, env.ID).Scan(&started); e != nil {
		t.Fatal(e)
	}
	if started != 0 {
		t.Fatal("failed evidence lookup recorded permission to hand over")
	}
}
