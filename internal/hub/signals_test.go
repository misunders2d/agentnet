package hub

import (
	"crypto/ed25519"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func hubSignal(t *testing.T, from, to member) protocol.Signal {
	t.Helper()
	s := protocol.Signal{V: 1, ID: protocol.NewID(), From: from.addr, To: to.addr, TS: time.Now().UnixMilli(), Session: protocol.NewID(), CT: []byte{1, 2, 3}}
	s.Sig = ed25519.Sign(from.id.Sign, s.Canonical())
	return s
}
func postSignal(t *testing.T, h *Hub, who member, s protocol.Signal) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(s)
	w := httptest.NewRecorder()
	h.handleSignal(w, signed(t, who.id, who.addr, http.MethodPost, "/v1/signal", raw))
	return w
}
func TestSignalsHubLiveOnlyAuthenticationReplayAndNoRows(t *testing.T) {
	h, _, _ := testHub(t)
	a, b := enroll(t, h, "alice"), enroll(t, h, "bob")
	for _, table := range []string{"nonces", "messages", "blobs", "notify_pending"} {
		var n int
		if err := h.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("fixture %s: %d %v", table, n, err)
		}
	}
	sub := &subscriber{}
	ch := h.signals.subscribe(b.addr, sub)
	signedAt := time.Now() // s is signed (in whole ms) no earlier: its replay entry lasts until at least signedAt-1ms+SignalTTL
	s := hubSignal(t, a, b)
	if w := postSignal(t, h, a, s); w.Code != 202 {
		t.Fatalf("signal: %d %s", w.Code, w.Body)
	}
	select {
	case got := <-ch:
		if got.ID != s.ID {
			t.Fatal("wrong signal")
		}
	default:
		t.Fatal("not live forwarded")
	}
	if w := postSignal(t, h, a, s); w.Code != 429 && time.Since(signedAt) < protocol.SignalTTL-time.Millisecond {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	if w := postSignal(t, h, b, hubSignal(t, a, b)); w.Code != 400 {
		t.Fatalf("publisher mismatch: %d %s", w.Code, w.Body)
	}
	bad := hubSignal(t, a, b)
	bad.TS = time.Now().Add(-protocol.SignalTTL).UnixMilli()
	bad.Sig = ed25519.Sign(a.id.Sign, bad.Canonical())
	if w := postSignal(t, h, a, bad); w.Code != 400 {
		t.Fatal("expired signal accepted")
	}
	h.signals.unsubscribe(b.addr, sub)
	if w := postSignal(t, h, a, hubSignal(t, a, b)); w.Code != 202 {
		t.Fatal("offline signal should be dropped without custody")
	}
	ch = h.signals.subscribe(b.addr, sub)
	defer h.signals.unsubscribe(b.addr, sub)
	select {
	case <-ch:
		t.Fatal("offline signal replayed on reconnect")
	default:
	}
	for _, table := range []string{"nonces", "messages", "blobs", "notify_pending"} {
		var n int
		if err := h.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("signal wrote %s: %d %v", table, n, err)
		}
	}
}
func TestSignalsHubRateSizeAndCurrentEnrollment(t *testing.T) {
	h, _, _ := testHub(t)
	a, b := enroll(t, h, "alice"), enroll(t, h, "bob")
	// The pair bucket (burst 8) gets a token back 1/8s after the first post:
	// "capped" holds only while the clock is inside that, which a loaded
	// runner may pass during the burst.
	burst := time.Now()
	for i := 0; i < 8; i++ {
		if w := postSignal(t, h, a, hubSignal(t, a, b)); w.Code != 202 {
			t.Fatalf("burst: %d %s", w.Code, w.Body)
		}
	}
	if w := postSignal(t, h, a, hubSignal(t, a, b)); w.Code != 429 && time.Since(burst) < time.Second/8 {
		t.Fatalf("rate cap: %d %s", w.Code, w.Body)
	}
	bad := hubSignal(t, a, b)
	bad.CT = make([]byte, protocol.MaxSignalCiphertext+1)
	bad.Sig = ed25519.Sign(a.id.Sign, bad.Canonical())
	if w := postSignal(t, h, a, bad); w.Code != 400 {
		t.Fatal("oversize accepted")
	}
	if _, err := h.store.db.Exec(`UPDATE agents SET revoked_at=? WHERE address=?`, time.Now().Unix(), a.addr); err != nil {
		t.Fatal(err)
	}
	if w := postSignal(t, h, a, hubSignal(t, a, b)); w.Code != 403 {
		t.Fatalf("revoked sender: %d %s", w.Code, w.Body)
	}
}
