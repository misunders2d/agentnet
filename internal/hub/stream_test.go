package hub

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// An HTTP/2 push stream stays open while idle for longer than the per-write
// timeout: the deadline bounds each write, not the quiet time between them.
func TestIdleHTTP2StreamOutlivesWriteTimeout(t *testing.T) {
	old := streamWriteTimeout
	streamWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { streamWriteTimeout = old })
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	srv := httptest.NewUnstartedServer(h.routes())
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	sendMessage(t, h, alice, bob) // gives the stream a first write
	ad := protocol.SessionAd{Address: bob.addr, Session: protocol.NewID()}
	protocol.SignAd(&ad, bob.id.Sign)
	path := "/v1/stream?ad=" + ad.Encode()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	protocol.SignRequest(req, bob.addr, bob.id.Sign, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 {
		t.Fatalf("stream: %d over %s", resp.StatusCode, resp.Proto)
	}
	guard := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer guard.Stop()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), protocol.MaxBody)
	nextEvent := func() string {
		for sc.Scan() {
			if e, ok := strings.CutPrefix(sc.Text(), "event: "); ok && e != "release" && e != "members" && e != "teams" && e != "groups" {
				return e
			}
		}
		t.Fatalf("stream ended: %v", sc.Err())
		return ""
	}
	if e := nextEvent(); e != "message" {
		t.Fatalf("first event %q", e)
	}
	time.Sleep(4 * streamWriteTimeout) // idle, well past the last write's deadline
	sendMessage(t, h, alice, bob)
	if e := nextEvent(); e != "message" {
		t.Fatalf("second event %q", e)
	}
}

// pipeResponse is a push stream's response that moves only as fast as the
// test reads it: an io.Pipe holds nothing, so a slow reader makes each write
// wait, as a slow peer's full buffers make a real one wait.
type pipeResponse struct {
	header http.Header
	w      *io.PipeWriter
}

func (p *pipeResponse) Header() http.Header              { return p.header }
func (p *pipeResponse) WriteHeader(int)                  {}
func (p *pipeResponse) Write(b []byte) (int, error)      { return p.w.Write(b) }
func (p *pipeResponse) Flush()                           {}
func (p *pipeResponse) SetWriteDeadline(time.Time) error { return nil }

type pushEvent struct{ name, data string }

// pushStream opens m's stream and hands over its events one at a time, until
// it ends (the channel closes). It returns once the stream is registered.
func pushStream(t *testing.T, h *Hub, m member) <-chan pushEvent {
	t.Helper()
	pr, pw := io.Pipe()
	ad := protocol.SessionAd{Address: m.addr, Session: protocol.NewID()}
	protocol.SignAd(&ad, m.id.Sign)
	req := signed(t, m.id, m.addr, "GET", "/v1/stream?ad="+ad.Encode(), nil)
	served := make(chan struct{})
	go func() {
		defer close(served)
		h.routes().ServeHTTP(&pipeResponse{header: http.Header{}, w: pw}, req)
		pw.Close()
	}()
	events := make(chan pushEvent) // unbuffered: the consumer sets the pace
	go func() {
		defer close(events)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 4<<10), protocol.MaxBody)
		var name string
		for sc.Scan() {
			if e, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				name = e
			} else if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				events <- pushEvent{name, d}
			}
		}
	}()
	t.Cleanup(func() {
		pr.Close()
		go func() {
			for range events { // let the reader finish
			}
		}()
		<-served
	})
	if e, ok := <-events; !ok || e.name != "release" { // written first: the stream is registered
		t.Fatalf("stream did not start: %+v", e)
	}
	return events
}

func envelopeID(t *testing.T, data string) string {
	t.Helper()
	var head struct{ ID string }
	if err := json.Unmarshal([]byte(data), &head); err != nil || head.ID == "" {
		t.Fatalf("pushed message without id: %v", err)
	}
	return head.ID
}

// A device busy storing a backlog answers its pings only after every message
// ahead of them, but acknowledges each message it stores: its stream stays
// open. Those acknowledgements keep only the device's newest connection: one
// it abandoned (half-open, as the Hub sees it) is still closed by the lease,
// and so is the newest once nothing at all is acknowledged.
func TestMessageAcksKeepBusyStreamAlive(t *testing.T) {
	const hb = 200 * time.Millisecond // the lease is two
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: hb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	abandoned := pushStream(t, h, bob)
	abandonedEnded := make(chan struct{})
	go func() { // read, never acknowledged: the Hub cannot tell it from a live one
		for range abandoned {
		}
		close(abandonedEnded)
	}()
	current := pushStream(t, h, bob)

	// Busy for at least four leases, and until the abandoned connection ended
	// while the newest one's acknowledgements went on.
	start, abandonedClosed := time.Now(), false
	for ; !abandonedClosed || time.Since(start) < 4*2*hb; time.Sleep(hb / 4) {
		if time.Since(start) > 10*time.Second {
			t.Fatal("the abandoned connection was kept alive by the newest one's acknowledgements")
		}
		select {
		case <-abandonedEnded:
			abandonedClosed = true
		default:
		}
		id := sendMessage(t, h, alice, bob)
		for got := ""; got != id; {
			e, ok := <-current
			if !ok {
				t.Fatal("the busy stream was closed although its device acknowledged every message")
			}
			if e.name == "message" { // pings stay unanswered: queued behind the backlog
				got = envelopeID(t, e.data)
			}
		}
		if c, b := bob.call(t, h, "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: protocol.StateDelivered}); c != http.StatusOK {
			t.Fatalf("ack: %d %s", c, b)
		}
	}
	idle := time.After(5 * time.Second) // nothing acknowledged any more: the lease ends it
	for {
		select {
		case _, ok := <-current:
			if !ok {
				return
			}
		case <-idle:
			t.Fatal("a stream that acknowledges nothing stayed open")
		}
	}
}

// A long backlog push to a slow reader keeps the ping interval: a due ping
// goes between frames, the messages keep their order, and a peer that
// answers the pings (and no message) keeps its stream. Before, the push sent
// no ping at all, and the first tick after it closed the stream as silent.
func TestPingsInterleaveLongBacklogPush(t *testing.T) {
	const hb = 200 * time.Millisecond
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: hb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	var sent []string
	for range 250 { // two full batches and part of a third
		sent = append(sent, sendMessage(t, h, alice, bob))
	}
	events := pushStream(t, h, bob)
	var got []string
	pings := 0 // seen before the last message
	timeout := time.After(30 * time.Second)
	for len(got) < len(sent) {
		var e pushEvent
		ok := true
		select {
		case e, ok = <-events:
		case <-timeout:
			t.Fatalf("backlog not pushed: %d of %d", len(got), len(sent))
		}
		if !ok {
			t.Fatalf("stream closed after %d of %d messages and %d pings", len(got), len(sent), pings)
		}
		switch e.name {
		case "message":
			got = append(got, envelopeID(t, e.data))
			time.Sleep(6 * time.Millisecond) // a slow store: the Hub's next write waits
		case "ping":
			pings++
			var p protocol.PingAck
			if json.Unmarshal([]byte(e.data), &p) != nil || p.Conn == "" {
				t.Fatalf("ping %q", e.data)
			}
			if c, b := bob.call(t, h, "POST", "/v1/stream/ack", p); c != http.StatusNoContent {
				t.Fatalf("ping ack: %d %s", c, b)
			}
		}
	}
	if pings == 0 {
		t.Fatal("no ping during a backlog push of over a second")
	}
	t.Logf("%d pings during the push", pings)
	if strings.Join(got, ",") != strings.Join(sent, ",") {
		t.Fatal("the backlog was not pushed in order")
	}
	for after := time.After(5 * time.Second); ; { // and the stream stays open: the next ping arrives
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the stream closed after the backlog")
			}
			if e.name == "ping" {
				return
			}
		case <-after:
			t.Fatal("no ping after the backlog")
		}
	}
}
