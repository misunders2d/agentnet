package hub

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func signalHookPost(t *testing.T, h *Hub, who member, signal protocol.Signal) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	return serve(h, signed(t, who.id, who.addr, http.MethodPost, "/v1/signal", raw))
}

func signalHookRows(t *testing.T, h *Hub) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"nonces", "messages", "blobs", "notify_pending"} {
		var n int
		if err := h.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return counts
}

func TestTypingHooksHubRouteAuthenticationWithoutStorage(t *testing.T) {
	h, _, _ := testHub(t)
	a, b := enroll(t, h, "alice"), enroll(t, h, "bob")
	before := signalHookRows(t, h)
	signedAt := time.Now() // s is signed (in whole ms) no earlier: its replay entry lasts until at least signedAt-1ms+SignalTTL
	s := hubSignal(t, a, b)
	if w := signalHookPost(t, h, a, s); w.Code != http.StatusAccepted {
		t.Fatalf("registered route: %d %s", w.Code, w.Body)
	}
	if w := signalHookPost(t, h, a, s); w.Code != http.StatusTooManyRequests && time.Since(signedAt) < protocol.SignalTTL-time.Millisecond {
		t.Fatalf("route replay: %d %s", w.Code, w.Body)
	}
	raw, _ := json.Marshal(hubSignal(t, a, b))
	r := signed(t, a.id, a.addr, http.MethodPost, "/v1/signal", raw)
	r.Body = io.NopCloser(strings.NewReader(`{}`))
	if w := serve(h, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("forged HTTP body: %d %s", w.Code, w.Body)
	}
	bad := hubSignal(t, a, b)
	bad.Sig = ed25519.Sign(b.id.Sign, bad.Canonical())
	if w := signalHookPost(t, h, a, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("forged signal: %d %s", w.Code, w.Body)
	}
	bad = hubSignal(t, a, b)
	bad.TS = time.Now().Add(-protocol.SignalTTL).UnixMilli()
	bad.Sig = ed25519.Sign(a.id.Sign, bad.Canonical())
	if w := signalHookPost(t, h, a, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("expired signal: %d %s", w.Code, w.Body)
	}
	if w := signalHookPost(t, h, b, hubSignal(t, a, b)); w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP sender does not match signal: %d %s", w.Code, w.Body)
	}
	if after := signalHookRows(t, h); !reflect.DeepEqual(before, after) {
		t.Fatalf("registered live route wrote durable rows: %v -> %v", before, after)
	}
}

func signalHookStream(t *testing.T, server *httptest.Server, who member, session string) (*http.Response, *bufio.Scanner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ad := protocol.SessionAd{Address: who.addr, Session: session}
	protocol.SignAd(&ad, who.id.Sign)
	r := signed(t, who.id, who.addr, http.MethodGet, "/v1/stream?ad="+ad.Encode(), nil).WithContext(ctx)
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.URL.Scheme, r.URL.Host, r.Host, r.RequestURI = origin.Scheme, origin.Host, origin.Host, ""
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed stream: %d", resp.StatusCode)
	}
	return resp, bufio.NewScanner(resp.Body), cancel
}

func signalHookFrame(t *testing.T, scanner *bufio.Scanner) (event, data string) {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		} else if line == "" && event != "" {
			return event, data
		}
	}
	t.Fatalf("stream ended before frame: %v", scanner.Err())
	return "", ""
}

func signalHookReady(t *testing.T, scanner *bufio.Scanner) {
	t.Helper()
	for {
		if event, _ := signalHookFrame(t, scanner); event == "groups" {
			return // initial frame comes after the live signal subscription
		}
	}
}

func TestTypingHooksHubStreamSessionExpiryAndReconnect(t *testing.T) {
	h, _, _ := testHub(t)
	a, b := enroll(t, h, "alice"), enroll(t, h, "bob")
	server := httptest.NewServer(h.routes())
	t.Cleanup(server.Close)
	session := protocol.NewID()
	resp, scanner, cancel := signalHookStream(t, server, b, session)
	if resp.Header.Get(protocol.SignalsHeader) != "1" {
		t.Fatal("admitted stream did not advertise live signals")
	}
	signalHookReady(t, scanner)
	wrong := hubSignal(t, a, b)
	stale := hubSignal(t, a, b)
	stale.Session, stale.TS = session, time.Now().Add(-protocol.SignalTTL).UnixMilli()
	// Model signals whose queue residency exhausted their TTL; route
	// authentication/freshness is checked independently above.
	h.signals.mu.Lock()
	for _, ch := range h.signals.queues[b.addr] {
		ch <- wrong
		ch <- stale
	}
	h.signals.mu.Unlock()
	post := func() protocol.Signal {
		s := hubSignal(t, a, b)
		s.Session = session
		s.Sig = ed25519.Sign(a.id.Sign, s.Canonical())
		if w := signalHookPost(t, h, a, s); w.Code != http.StatusAccepted {
			t.Fatalf("live route: %d %s", w.Code, w.Body)
		}
		return s
	}
	read := func(want protocol.Signal) {
		event, data := signalHookFrame(t, scanner)
		var got protocol.Signal
		if err := json.Unmarshal([]byte(data), &got); event != "signal" || err != nil || got.ID != want.ID {
			t.Fatalf("wrong queued signal frame: %q %+v %v", event, got, err)
		}
	}
	read(post())
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.signals.mu.Lock()
		n := len(h.signals.queues[b.addr])
		h.signals.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected stream retained a signal queue")
		}
		time.Sleep(time.Millisecond)
	}
	post() // accepted while offline, without a connection queue
	resp, scanner, _ = signalHookStream(t, server, b, session)
	signalHookReady(t, scanner)
	read(post()) // same session, new stream: no offline signal was replayed
}

func TestTypingHooksPendingStreamAndSignalAdmission(t *testing.T) {
	h, _, _ := testHub(t)
	a, b := enroll(t, h, "alice"), enroll(t, h, "bob")
	if _, err := h.store.db.Exec(`UPDATE agents SET pending_person=?, pending_until=? WHERE address=?`, protocol.NewID(), time.Now().Add(time.Minute).Unix(), b.addr); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.routes())
	t.Cleanup(server.Close)
	resp, _, cancel := signalHookStream(t, server, b, protocol.NewID())
	if resp.Header.Get(protocol.SignalsHeader) != "" {
		t.Fatal("pending device was offered live signals")
	}
	cancel()
	resp.Body.Close()
	if w := signalHookPost(t, h, a, hubSignal(t, a, b)); w.Code != http.StatusForbidden {
		t.Fatalf("pending recipient: %d %s", w.Code, w.Body)
	}
	if w := signalHookPost(t, h, b, hubSignal(t, b, a)); w.Code != http.StatusForbidden {
		t.Fatalf("pending sender: %d %s", w.Code, w.Body)
	}
	if _, err := h.store.db.Exec(`UPDATE agents SET pending_person=NULL, revoked_at=? WHERE address=?`, time.Now().Unix(), b.addr); err != nil {
		t.Fatal(err)
	}
	if w := signalHookPost(t, h, a, hubSignal(t, a, b)); w.Code != http.StatusForbidden {
		t.Fatalf("revoked recipient: %d %s", w.Code, w.Body)
	}
}
