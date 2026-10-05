package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// runWith runs a's daemon with opts and waits until the Hub lists its session.
func runWith(t *testing.T, w *world, a *Agent, opts RunOptions) (stop func(), session string) {
	t.Helper()
	a.Logf = t.Logf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx, opts); close(done) }()
	stop = func() { cancel(); <-done }
	t.Cleanup(stop)
	eventually(t, a.Address+" session", func() bool {
		infos, err := w.alice.sessions(tctx(t), a.Address)
		for _, in := range infos {
			if in.Connected && (opts.Listen == "" || in.Ad.Endpoint != "") {
				session = in.Ad.Session
			}
		}
		return err == nil && session != ""
	})
	return stop, session
}

type hubCounters struct{ requests, messages, in, out int64 }

func counters(w *world) hubCounters {
	s := w.hub.Hub.Stats()
	return hubCounters{s.Requests.Load(), s.Messages.Load(), s.BlobBytesIn.Load(), s.BlobBytesOut.Load()}
}

func TestDirectMessageAndFileBypassHub(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0"})
	path, data := writeFile(t, t.TempDir(), "direct.bin", 10<<20)
	before := counters(w)
	res, err := w.alice.Send(tctx(t), w.bob.Address, "straight to you", "", path)
	if err != nil || res.Path != protocol.PathDirect || res.State != protocol.StateDelivered {
		t.Fatalf("send = %+v, %v", res, err)
	}
	after := counters(w)
	if after.messages != before.messages || after.in != before.in {
		t.Fatalf("payload went through the Hub: %+v -> %+v", before, after)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 0 {
		t.Fatal("spool kept after direct custody")
	}
	if r, err := w.alice.Status(tctx(t), res.ID, 0); err != nil || r.State != protocol.StateDelivered || r.Path != protocol.PathDirect {
		t.Fatalf("status = %+v, %v", r, err)
	}
	msgs, _ := w.bob.Inbox(false, false)
	if len(msgs) != 1 || msgs[0].Body != "straight to you" {
		t.Fatalf("inbox = %+v", msgs)
	}
	paths, err := w.bob.Download(tctx(t), msgs[0].ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("direct file differs")
	}
	if counters(w).out != before.out {
		t.Fatal("download fetched from the Hub")
	}
	if _, err := os.Stat(w.bob.downloadPath(msgs[0].Attachments[0].BlobID)); err != nil {
		t.Fatal("directly received ciphertext (the only copy) was discarded")
	}
}

func TestUnreachableEndpointFallsBackAndCools(t *testing.T) {
	w := newWorld(t, "")
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close()
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0", Advertise: "https://" + deadAddr})
	res, err := w.alice.Send(tctx(t), w.bob.Address, "via hub", "")
	if err != nil || res.Path != protocol.PathRelay {
		t.Fatalf("send = %+v, %v", res, err)
	}
	if cooling, _ := w.alice.store.routeCooling("https://"+deadAddr, time.Now()); !cooling {
		t.Fatal("failed route not cooled")
	}
	eventually(t, "relayed delivery", func() bool { return state(t, w.alice, res.ID) == protocol.StateDelivered })
}

func TestWrongPeerCertificateFallsBack(t *testing.T) {
	w := newWorld(t, "")
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	_, _ = runWith(t, w, carol, RunOptions{Listen: "127.0.0.1:0"})
	infos, _ := w.alice.sessions(tctx(t), carol.Address)
	carolEndpoint := infos[0].Ad.Endpoint
	// Bob's signed ad points at carol's listener, which cannot present bob's certificate.
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0", Advertise: carolEndpoint})
	res, err := w.alice.Send(tctx(t), w.bob.Address, "not for carol", "")
	if err != nil || res.Path != protocol.PathRelay {
		t.Fatalf("send = %+v, %v", res, err)
	}
	if msgs, _ := carol.Inbox(false, false); len(msgs) != 0 {
		t.Fatal("message reached the wrong peer")
	}
}

func TestInterruptedDirectTransferFallsBackWithoutDuplicates(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0"})
	path, data := writeFile(t, t.TempDir(), "half.bin", 3<<20)
	f := &faults{}
	f.addAfter("PUT", "/v1/direct/blobs/", 2, 1, false)
	wrapTransport = func(rt http.RoundTripper) http.RoundTripper { return faultRT{rt, f} }
	defer func() { wrapTransport = nil }()
	res, err := w.alice.Send(tctx(t), w.bob.Address, "half then hub", "", path)
	if err != nil || res.Path != protocol.PathRelay || res.State != protocol.StateCustody {
		t.Fatalf("send = %+v, %v", res, err)
	}
	eventually(t, "relayed delivery", func() bool { return state(t, w.alice, res.ID) == protocol.StateDelivered })
	msgs, _ := w.bob.Inbox(false, false)
	if len(msgs) != 1 {
		t.Fatalf("inbox has %d messages", len(msgs))
	}
	paths, err := w.bob.Download(tctx(t), msgs[0].ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("file differs")
	}
}

// The peer stored the message but its receipt was lost: the sender keeps its
// spool, relays through the Hub, and the recipient files it once.
func TestDirectReceiptLossThenRelay(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0"})
	path, data := writeFile(t, t.TempDir(), "lost.bin", 1<<20)
	f := &faults{}
	f.add("POST", "/v1/direct/messages", 1, true)
	wrapTransport = func(rt http.RoundTripper) http.RoundTripper { return faultRT{rt, f} }
	defer func() { wrapTransport = nil }()
	res, err := w.alice.Send(tctx(t), w.bob.Address, "receipt lost", "", path)
	if err != nil || res.Path != protocol.PathRelay {
		t.Fatalf("send = %+v, %v", res, err)
	}
	eventually(t, "Hub learns delivery", func() bool { return state(t, w.alice, res.ID) == protocol.StateDelivered })
	if msgs, _ := w.bob.Inbox(false, false); len(msgs) != 1 {
		t.Fatalf("inbox has %d messages", len(msgs))
	}
	paths, err := w.bob.Download(tctx(t), res.ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, data) {
		t.Fatal("file differs")
	}
}

func TestDirectReceiverChecksCallerAndMembership(t *testing.T) {
	defer func(d time.Duration) { memberTTL = d }(memberTTL)
	memberTTL = 300 * time.Millisecond
	w := newWorld(t, "")
	carol := mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0"})
	infos, _ := w.alice.sessions(tctx(t), w.bob.Address)
	ad := infos[0].Ad

	// Alice starts a direct upload; carol cannot touch it or claim her message.
	aliceConn, _ := w.alice.directConn(ad)
	id := protocol.NewID()
	aliceConn.do(tctx(t), "POST", "/v1/direct/blobs", protocol.BlobReserve{ID: id, Recipient: w.bob.Address, Size: 10, SHA256: digestHex(nil)}, nil)
	carolConn, _ := carol.directConn(ad)
	var he *HubError
	if err := carolConn.doBytes(tctx(t), "PUT", "/v1/direct/blobs/"+id+"?offset=0", []byte("x"), nil); !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("third party chunk: %v", err)
	}
	peer, _ := w.alice.sendKey(tctx(t), w.bob.Address)
	r, _ := peer.Recipient()
	env := sealFor(t, w.alice, w.bob.Address, r)
	if err := carolConn.do(tctx(t), "POST", "/v1/direct/messages", env, nil); !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("message posted by someone else: %v", err)
	}

	// Revocation reaches direct delivery within memberTTL.
	if err := carolConn.do(tctx(t), "GET", "/v1/direct/blobs/"+protocol.NewID(), nil, nil); !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("member request before revoke: %v", err)
	}
	if err := w.alice.Revoke(tctx(t), carol.Address); err != nil {
		t.Fatal(err)
	}
	time.Sleep(memberTTL + 50*time.Millisecond)
	if err := carolConn.do(tctx(t), "GET", "/v1/direct/blobs/"+protocol.NewID(), nil, nil); !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("revoked peer after TTL: %v", err)
	}

	// With the Hub down and the membership check expired, direct fails closed.
	w.hub.Stop()
	time.Sleep(memberTTL + 50*time.Millisecond)
	if err := aliceConn.do(tctx(t), "GET", "/v1/direct/blobs/"+id, nil, nil); !errors.As(err, &he) || he.Status != 503 {
		t.Fatalf("expired membership with Hub down: %v", err)
	}
}

func TestSessionAddressing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	w := &world{hub: testhub.StartConfig(t, hub.Config{DataDir: dir, SessionGrace: 500 * time.Millisecond}, "127.0.0.1:0")}
	w.alice = mustJoin(t, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "alice")
	code, _ := w.alice.Invite(tctx(t), "bob", time.Hour, false)
	w.bobHome = filepath.Join(t.TempDir(), "bob")
	w.bob = mustJoin(t, w.bobHome, code, "laptop")

	stop, s1 := runWith(t, w, w.bob, RunOptions{})
	res, err := w.alice.Send(tctx(t), w.bob.Address+"#"+s1, "to this session", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "session delivery", func() bool { return state(t, w.alice, res.ID) == protocol.StateDelivered })

	// Ended session, still in its grace period: accepted, then expires.
	// The grace runs from the Hub seeing the stop, so no earlier than
	// stopping; a loaded runner may spend all of it before the send lands,
	// and then the send is refused as expired, correctly.
	stopping := time.Now()
	stop()
	res, err = w.alice.Send(tctx(t), w.bob.Address+"#"+s1, "too late", "")
	inGrace := time.Since(stopping) < 500*time.Millisecond
	var he *HubError
	switch {
	case err == nil && res.State == protocol.StateCustody:
		eventually(t, "expired receipt", func() bool { return state(t, w.alice, res.ID) == protocol.StateExpired })
	case !inGrace && (errors.Is(err, ErrSessionExpired) || errors.As(err, &he) && he.Code == protocol.CodeSessionExpired):
		t.Logf("the send came after the grace (%v after stopping): refused as expired", time.Since(stopping))
	default:
		t.Fatalf("send during grace = %+v, %v", res, err)
	}

	// After the grace period: refused without fallback, inbox with fallback.
	if _, err := w.alice.Send(tctx(t), w.bob.Address+"#"+s1, "refused", ""); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("stale session: %v", err)
	}
	res, err = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address + "#" + s1, Body: "fallback", Fallback: true})
	if err != nil {
		t.Fatal(err)
	}
	_, s2 := runWith(t, w, w.bob, RunOptions{})
	if s2 == s1 {
		t.Fatal("new daemon reused the old session id")
	}
	eventually(t, "fallback delivery", func() bool { return state(t, w.alice, res.ID) == protocol.StateDelivered })
	msgs, _ := w.bob.Inbox(false, false)
	bodies := map[string]bool{}
	for _, m := range msgs {
		bodies[m.Body] = true
	}
	if !bodies["to this session"] || !bodies["fallback"] || bodies["too late"] || bodies["refused"] {
		t.Fatalf("inbox = %v", bodies)
	}
}

// With both daemons connected and nothing to send, the only traffic is the
// Hub's pings on the already-open streams: no requests reach the Hub.
func TestIdleDaemonsDoNotPoll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	// A ping interval well above one signed request's round trip on slow CI
	// disks (Windows): the Hub closes a stream whose ack is later than two
	// intervals, and a 250ms interval was once too short there (both
	// streams closed as silent and reconnected). Production pings every 90s.
	const beat = time.Second
	w := &world{hub: testhub.StartConfig(t, hub.Config{DataDir: dir, Heartbeat: beat}, "127.0.0.1:0")}
	w.alice = mustJoin(t, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "alice")
	code, _ := w.alice.Invite(tctx(t), "bob", time.Hour, false)
	w.bob = mustJoin(t, filepath.Join(t.TempDir(), "bob"), code, "laptop")
	w.alice.heartbeat, w.bob.heartbeat = beat, beat
	// Record every request each daemon makes to the Hub, by path.
	rec := &requestLog{}
	for _, a := range []*Agent{w.alice, w.bob} {
		a.hub.http.Transport = recordingRT{a.hub.http.Transport, rec}
	}
	runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0"})
	runWith(t, w, w.alice, RunOptions{})

	// Connecting starts a finite chain of event-driven upkeep: each daemon
	// publishes its capabilities, and the Hub's members event for that makes
	// each look once more (GET /v1/version), after runWith returns on slow
	// CI. Wait for it to end: a daemon that kept asking never goes quiet.
	for settle := time.Now().Add(30 * time.Second); ; {
		rec.reset()
		time.Sleep(4 * beat)
		if _, others := rec.snapshot(); len(others) == 0 {
			break
		} else if time.Now().After(settle) {
			t.Fatalf("daemons never went quiet after connecting: %v", others)
		}
	}
	rec.reset()
	const idle = 5 * beat // five ping intervals with both daemons connected and nothing to do
	time.Sleep(idle)
	acks, others := rec.snapshot()
	if len(others) != 0 {
		t.Fatalf("idle daemons made %d non-ping request(s): %v", len(others), others)
	}
	// Each daemon answers each ping; nothing else is asked.
	if acks == 0 || acks > 2*(int(idle/beat)+1) {
		t.Fatalf("%d ping acks in %s with a %s ping interval", acks, idle, beat)
	}
}

// The Hub, not the admin's connection pin, decides whether its advertised
// endpoint can issue browser invitations. A pinned-only Hub still refuses.
func TestBrowserInvitePinnedOnlyHubRefuses(t *testing.T) {
	w := newWorld(t, "")
	rec := &requestLog{}
	w.alice.hub.http.Transport = recordingRT{w.alice.hub.http.Transport, rec}
	inv, err := w.alice.CreateInvite(tctx(t), InviteOptions{Label: "carol", TTL: time.Hour})
	var he *HubError
	if !errors.As(err, &he) || he.Status != http.StatusConflict || inv.Code != "" {
		t.Fatalf("browser invite from a pinned Hub: %q %v", inv.Code, err)
	}
	if acks, others := rec.snapshot(); acks != 0 || len(others) != 1 || others[0] != "POST /v1/admin/invites" {
		t.Fatalf("browser capability request: %v", others)
	}
}

// requestLog counts ping acks and records any other request path.
type requestLog struct {
	mu     sync.Mutex
	acks   int
	others []string
}

func (l *requestLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acks, l.others = 0, nil
}

func (l *requestLog) snapshot() (int, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acks, append([]string(nil), l.others...)
}

type recordingRT struct {
	base http.RoundTripper
	log  *requestLog
}

func (r recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.log.mu.Lock()
	if req.Method == "POST" && req.URL.Path == "/v1/stream/ack" {
		r.log.acks++
	} else {
		r.log.others = append(r.log.others, req.Method+" "+req.URL.Path)
	}
	r.log.mu.Unlock()
	return r.base.RoundTrip(req)
}

// A peer that keeps its TCP connection open but stops answering pings (a
// sleeping laptop, a half-open link) loses its session within the lease.
func TestSilentPeerSessionExpires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	// A lease well above one send's time on slow CI disks (Windows), so the
	// message reaches the Hub while the session is still live.
	const beat, grace = 250 * time.Millisecond, 500 * time.Millisecond
	w := &world{hub: testhub.StartConfig(t, hub.Config{DataDir: dir, Heartbeat: beat, SessionGrace: grace}, "127.0.0.1:0")}
	w.alice = mustJoin(t, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "alice")
	code, _ := w.alice.Invite(tctx(t), "bob", time.Hour, false)
	w.bob = mustJoin(t, filepath.Join(t.TempDir(), "bob"), code, "laptop")

	// Bob's stream stays open and is read, but no ping is ever acknowledged.
	ad := protocol.SessionAd{Address: w.bob.Address, Session: protocol.NewID()}
	protocol.SignAd(&ad, w.bob.id.Sign)
	req, _ := w.bob.hub.request(context.Background(), "GET", "/v1/stream?ad="+ad.Encode(), nil)
	opened := time.Now() // the stream's lease (2 beats unacknowledged, then the grace) runs from no earlier
	resp, err := w.bob.hub.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go io.Copy(io.Discard, resp.Body)
	start := time.Now()
	res, err := w.alice.Send(tctx(t), w.bob.Address+"#"+ad.Session, "for the sleeping session", "")
	// "Still registered" holds only while the clock is inside the lease: a
	// loaded runner may spend it before the send lands, and then the send
	// is refused as expired, correctly; what follows is about its message.
	if inLease := time.Since(opened) < 2*beat+grace; err != nil || res.State != protocol.StateCustody {
		var he *HubError
		if !inLease && (errors.Is(err, ErrSessionExpired) || errors.As(err, &he) && he.Code == protocol.CodeSessionExpired) {
			t.Logf("the send came after the lease (%v after the stream opened): refused as expired; the queued message's expiry is not observable this run", time.Since(opened))
			return
		}
		t.Fatalf("send while registered = %+v, %v", res, err)
	}
	eventually(t, "silent session to end", func() bool {
		infos, _ := w.alice.sessions(tctx(t), w.bob.Address)
		return len(infos) == 0
	})
	if bound := 3*beat + grace + 200*time.Millisecond; time.Since(start) > bound {
		t.Fatalf("silent session lasted %s, bound %s", time.Since(start), bound)
	}
	if s := state(t, w.alice, res.ID); s != protocol.StateExpired {
		t.Fatalf("queued session message state %s, want expired", s)
	}
}

func digestHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func sealFor(t *testing.T, from *Agent, to string, r *age.X25519Recipient) envelope.Envelope {
	t.Helper()
	env, err := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: from.Address, To: to, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "x"}, from.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// The receipt is stored before the sender's spool goes: if recording it
// fails, the spooled ciphertext is still there.
func TestSpoolKeptUntilReceiptRecorded(t *testing.T) {
	w := newWorld(t, "")
	path, _ := writeFile(t, t.TempDir(), "k.bin", 1000)
	if _, err := w.alice.store.db.Exec(`CREATE TRIGGER no_receipt BEFORE UPDATE OF state ON outbox
		BEGIN SELECT RAISE(FAIL, 'injected receipt write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "keep my spool", "", path); err == nil {
		t.Fatal("send succeeded without recording its receipt")
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 1 {
		t.Fatalf("spool has %d files after a failed receipt write", len(entries))
	}
}

// slowPuts delays chunk uploads to make a long transfer.
type slowPuts struct{ base http.RoundTripper }

func (s slowPuts) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/blobs/") {
		time.Sleep(200 * time.Millisecond)
	}
	return s.base.RoundTrip(r)
}

// A long queued upload runs beside the stream: pings are still answered and
// incoming messages still arrive while it is in progress.
func TestQueuedUploadDoesNotBlockStream(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	const beat = time.Second // allow signed ACK persistence on slow CI disks
	w := &world{hub: testhub.StartConfig(t, hub.Config{DataDir: dir, Heartbeat: beat}, "127.0.0.1:0")}
	w.alice = mustJoin(t, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "alice")
	code, _ := w.alice.Invite(tctx(t), "bob", time.Hour, false)
	w.bob = mustJoin(t, filepath.Join(t.TempDir(), "bob"), code, "laptop")
	w.alice.heartbeat = beat

	path, _ := writeFile(t, t.TempDir(), "long.bin", 5<<20) // ~11 chunks × 200 ms
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "pin keys", ""); err != nil {
		t.Fatal(err)
	}
	f := injectFaults(w.alice)
	f.add("POST", "/v1/blobs", 1, false) // first attempt fails, so it is queued
	res, err := w.alice.Send(tctx(t), w.bob.Address, "long upload", "", path)
	if err != nil || res.State != stateQueued {
		t.Fatalf("queue = %+v, %v", res, err)
	}
	w.alice.hub.http.Transport = slowPuts{w.alice.hub.http.Transport}

	runWith(t, w, w.alice, RunOptions{})
	acks := w.hub.Hub.Stats().Acks.Load()
	if _, err := w.bob.Send(tctx(t), w.alice.Address, "while you upload", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "incoming message during upload", func() bool { return hasInbox(w.alice, "while you upload") })
	if s, _, _, _ := w.alice.store.outboxState(res.ID); s != stateQueued {
		t.Fatalf("upload finished before the check (state %s); test is not measuring concurrency", s)
	}
	eventually(t, "ping acknowledged during upload", func() bool {
		if s, _, _, _ := w.alice.store.outboxState(res.ID); s != stateQueued {
			t.Fatalf("upload finished before ping acknowledgement (state %s); test is not measuring concurrency", s)
		}
		return w.hub.Hub.Stats().Acks.Load() > acks
	})
	eventually(t, "upload to finish", func() bool { return state(t, w.alice, res.ID) == protocol.StateCustody })
}

func hasInbox(a *Agent, body string) bool {
	msgs, _ := a.Inbox(false, false)
	for _, m := range msgs {
		if m.Body == body {
			return true
		}
	}
	return false
}

// A relayed send waits for the recipient's receipt with one request that
// the receipt answers; with the recipient offline it returns custody after
// the wait, without retrying.
func TestSendWaitsForReceipt(t *testing.T) {
	w := newWorld(t, "")
	rec := &requestLog{}
	w.alice.hub.http.Transport = recordingRT{w.alice.hub.http.Transport, rec}
	stopBob, _ := runWith(t, w, w.bob, RunOptions{})

	rec.reset()
	res, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "are you there?", Wait: 5 * time.Second})
	if err != nil || res.State != protocol.StateDelivered || res.Path != protocol.PathRelay {
		t.Fatalf("online send = %+v, %v", res, err)
	}
	if st, _, _, _ := w.alice.store.outboxState(res.ID); st != protocol.StateDelivered {
		t.Fatalf("outbox state %s", st)
	}
	if n := countPaths(rec, "/wait"); n != 1 {
		t.Fatalf("%d receipt-wait requests, want 1", n)
	}

	stopBob()
	rec.reset()
	start := time.Now()
	res, err = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "later", Wait: 300 * time.Millisecond})
	if err != nil || res.State != protocol.StateCustody || time.Since(start) > 5*time.Second {
		t.Fatalf("offline send = %+v, %v after %s", res, err, time.Since(start))
	}
	if n := countPaths(rec, "/wait"); n != 1 {
		t.Fatalf("%d receipt-wait requests, want 1", n)
	}
	if n := countPaths(rec, "POST /v1/messages"); n != 1 {
		t.Fatalf("message posted %d times", n)
	}
	if r, err := w.alice.Status(tctx(t), res.ID, 200*time.Millisecond); err != nil || r.State != protocol.StateCustody {
		t.Fatalf("status --wait while offline: %+v %v", r, err)
	}
}

func countPaths(l *requestLog, part string) int {
	_, others := l.snapshot()
	n := 0
	for _, o := range others {
		if strings.Contains(o, part) {
			n++
		}
	}
	return n
}

// The receipt wait may be longer than the transport's response-header
// timeout: the Hub sends headers at once.
func TestReceiptWaitOutlivesHeaderTimeout(t *testing.T) {
	// Setup keeps the ordinary request budget (slow CI disks, Windows).
	w := newWorld(t, "")
	res, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "x"}) // bob offline
	if err != nil || res.State != protocol.StateCustody {
		t.Fatalf("send = %+v, %v", res, err)
	}
	// Only the measured Status waits at most 300ms for response headers: a
	// clone of Alice's own Hub transport (the same TLS trust, its own fresh
	// connections), so the 1s wait must stream its headers before its answer.
	tr, ok := w.alice.hub.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("hub transport %T", w.alice.hub.http.Transport)
	}
	measured := tr.Clone()
	measured.ResponseHeaderTimeout = 300 * time.Millisecond
	w.alice.hub.http = &http.Client{Transport: measured}
	start := time.Now()
	r, err := w.alice.Status(tctx(t), res.ID, time.Second)
	if err != nil || r.State != protocol.StateCustody || time.Since(start) < time.Second {
		t.Fatalf("1s wait with 300ms header timeout: %+v, %v after %s", r, err, time.Since(start))
	}
}
