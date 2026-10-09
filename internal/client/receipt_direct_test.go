package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func directReceiptEnvelope(t *testing.T, w *world) (envelope.Envelope, []byte) {
	t.Helper()
	if err := w.bob.store.pin(w.alice.Self()); err != nil {
		t.Fatal(err)
	}
	recipient, err := w.bob.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Seal(envelope.Inner{V: envelope.Version, ID: protocol.NewID(), TS: time.Now().Unix(), From: w.alice.Address, To: w.bob.Address, Kind: envelope.KindMessage, Body: "one delivered original"}, w.alice.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return env, raw
}

func relayReceiptDuplicate(t *testing.T, w *world, env envelope.Envelope, raw []byte) {
	t.Helper()
	var custody protocol.Receipt
	if err := w.alice.hub.do(tctx(t), "POST", "/v1/messages", env, &custody); err != nil {
		t.Fatal(err)
	}
	if custody.State != protocol.StateCustody {
		t.Fatalf("relay fallback did not retain custody: %+v", custody)
	}
	if err := w.bob.dispatch(tctx(t), "message", string(raw)); err != nil {
		t.Fatal(err)
	}
	rows, err := w.bob.store.unsentReceipts()
	if err != nil || len(rows) != 1 || rows[0].id != env.ID || rows[0].state != protocol.StateDelivered {
		t.Fatalf("relay duplicate did not queue its exact delivered receipt: %+v %v", rows, err)
	}
}

func requireRelayReceiptDelivered(t *testing.T, w *world, env envelope.Envelope) {
	t.Helper()
	if err := w.bob.flushReceipts(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var got protocol.Receipt
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/messages/"+env.ID, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != protocol.StateDelivered {
		t.Fatalf("a newer relay receipt was lost: Hub state=%s", got.State)
	}
	if n := count(t, w.bob, "inbox"); n != 1 {
		t.Fatalf("duplicate transport changed visible originals: %d", n)
	}
}

func TestDirectCompletionPreservesConcurrentRelayReceipt(t *testing.T) {
	w := newWorld(t, "")
	env, raw := directReceiptEnvelope(t, w)
	original := w.bob.store.onChange
	defer func() { w.bob.store.onChange = original }()
	interleaved := false
	w.bob.store.onChange = func() {
		if original != nil {
			original()
		}
		if interleaved {
			return
		}
		seen, err := w.bob.store.seen(env.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !seen {
			return
		}
		// Admission committed, but the direct HTTP handler has not returned.
		// The sender times out and the same envelope arrives through the Hub.
		interleaved = true
		relayReceiptDuplicate(t, w, env, raw)
	}
	s := &directServer{a: w.bob, nonces: map[string]time.Time{}, members: map[string]member{}}
	r := httptest.NewRequest("POST", "/v1/direct/messages", bytes.NewReader(raw))
	protocol.SignRequest(r, w.alice.Address, w.alice.id.Sign, raw)
	response := httptest.NewRecorder()
	s.handleMessage(response, r)
	if response.Code != http.StatusOK || !interleaved {
		t.Fatalf("direct admission/interleaving failed: status=%d interleaved=%t", response.Code, interleaved)
	}
	requireRelayReceiptDelivered(t, w, env)
}

type receipt404InterleaveRT struct {
	base  http.RoundTripper
	after func()
}

func (rt *receipt404InterleaveRT) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := rt.base.RoundTrip(r)
	if err == nil && response.StatusCode == http.StatusNotFound && r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/messages/") && strings.HasSuffix(r.URL.Path, "/ack") && rt.after != nil {
		after := rt.after
		rt.after = nil
		after()
	}
	return response, err
}

func TestReceipt404PreservesNewerRelayDuplicate(t *testing.T) {
	w := newWorld(t, "")
	env, raw := directReceiptEnvelope(t, w)
	if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	// A receipt for a direct-only delivery gets an actual Hub 404. Before
	// that response is consumed, relay fallback stores the same envelope and
	// the stream queues its receipt. Completing the older 404 must not erase it.
	interleaved := false
	w.bob.hub.http.Transport = &receipt404InterleaveRT{base: w.bob.hub.http.Transport, after: func() {
		interleaved = true
		relayReceiptDuplicate(t, w, env, raw)
	}}
	if err := w.bob.flushReceipts(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if !interleaved {
		t.Fatal("the fixture did not exercise a real pre-fallback Hub 404")
	}
	requireRelayReceiptDelivered(t, w, env)
}

func TestReceiptGenerationSurvivesRestartAndPromotion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.db.Close() }()
	inboxID, heldID, historyID := protocol.NewID(), protocol.NewID(), protocol.NewID()
	for _, statement := range []struct {
		sql string
		id  string
	}{
		{`INSERT INTO inbox(id,sender,ts,kind,body,received_at) VALUES(?,'alice/laptop',1,'message','original',1)`, inboxID},
		{`INSERT INTO quarantine(id,sender,reason,envelope,received_at) VALUES(?,'alice/laptop','proof_pending','{}',1)`, heldID},
		{`INSERT INTO history_receipts(id) VALUES(?)`, historyID},
	} {
		if _, err := s.db.Exec(statement.sql, statement.id); err != nil {
			t.Fatal(err)
		}
	}
	old, err := s.unsentReceipts()
	if err != nil || len(old) != 3 {
		t.Fatalf("initial receipt snapshots: %+v %v", old, err)
	}
	for _, r := range old {
		if err := s.resendReceipt(r.id); err != nil {
			t.Fatal(err)
		}
	}
	// The normal history admission upsert is another reset, including when
	// that carrier was already pending rather than previously acknowledged.
	if _, err := s.db.Exec(`INSERT INTO history_receipts(id,acked) VALUES(?,0) ON CONFLICT(id) DO UPDATE SET acked=0`, historyID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range old {
		if err := s.markAcked(r); err != nil {
			t.Fatal(err)
		}
	}
	current, err := s.unsentReceipts()
	if err != nil || len(current) != 3 {
		t.Fatalf("stale completion erased receipts across restart: %+v %v", current, err)
	}
	for _, r := range current {
		want := int64(1)
		if r.id == historyID {
			want = 2
		}
		if r.generation != want {
			t.Fatalf("reset generation did not survive: %+v want=%d", r, want)
		}
		if err := s.markAcked(r); err != nil {
			t.Fatal(err)
		}
	}
	if remaining, err := s.unsentReceipts(); err != nil || len(remaining) != 0 {
		t.Fatalf("matching completions did not drain: %+v %v", remaining, err)
	}

	// A successful promotion changes the owning table/disposition. Even a
	// same-generation response for the old quarantine cannot clear delivery.
	id := protocol.NewID()
	if err := s.holdAs(envelope.Envelope{ID: id, From: "alice/laptop"}, reasonProof); err != nil {
		t.Fatal(err)
	}
	held, err := s.unsentReceipts()
	if err != nil || len(held) != 1 {
		t.Fatal("missing held receipt", err)
	}
	if err := s.promote(envelope.Inner{ID: id, From: "alice/laptop", TS: 1, Kind: envelope.KindMessage, Body: "accepted original"}, "verified-fixture-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.markAcked(held[0]); err != nil {
		t.Fatal(err)
	}
	delivered, err := s.unsentReceipts()
	if err != nil || len(delivered) != 1 || delivered[0].id != id || delivered[0].state != protocol.StateDelivered || delivered[0].table != "inbox" {
		t.Fatalf("old held completion erased promoted delivery: %+v %v", delivered, err)
	}
}
