package hub

import (
	"bufio"
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
