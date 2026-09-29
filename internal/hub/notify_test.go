package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// fakePush records the pushes a notifier makes and answers with status
// (and Retry-After); block, if set, holds each send until it is closed.
type fakePush struct {
	mu       sync.Mutex
	sent     []pushCall
	status   int
	retry    time.Duration
	block    chan struct{}
	started  chan struct{}
	endpoint string
}

type pushCall struct {
	endpoint, topic string
	payload         protocol.PushPayload
}

func (f *fakePush) send(ctx context.Context, sub protocol.PushSubscription, payload []byte, topic string) (int, time.Duration, error) {
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var p protocol.PushPayload
	json.Unmarshal(payload, &p)
	f.sent = append(f.sent, pushCall{sub.Endpoint, topic, p})
	return f.status, f.retry, nil
}

func (f *fakePush) calls() []pushCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushCall(nil), f.sent...)
}

type notifyWorld struct {
	h          *Hub
	bob, alice member // bob receives on his browser device; alice sends
	push       *fakePush
	now        time.Time
}

func newNotifyWorld(t *testing.T) *notifyWorld {
	t.Helper()
	h, _, _ := testHub(t)
	w := &notifyWorld{h: h, bob: enroll(t, h, "bob"), alice: enroll(t, h, "alice"), push: &fakePush{status: http.StatusCreated},
		now: time.Unix(1790000000, 0)}
	h.notifier.send = w.push.send
	h.notifier.now = func() time.Time { return w.now }
	return w
}

func pushKey(n int, first byte) string {
	b := make([]byte, n)
	b[0] = first
	return base64.RawURLEncoding.EncodeToString(b)
}

func (w *notifyWorld) subscribe(t *testing.T, m member, endpoint string) {
	t.Helper()
	if c, b := m.call(t, w.h, "PUT", "/v1/notify/subscription", protocol.PushSubscription{Endpoint: endpoint, P256DH: pushKey(65, 4), Auth: pushKey(16, 1)}); c != http.StatusNoContent {
		t.Fatalf("subscribe: %d %s", c, b)
	}
}

func (w *notifyWorld) prefs(t *testing.T, m member, p protocol.NotifyPrefs) {
	t.Helper()
	if c, b := m.call(t, w.h, "PUT", "/v1/notify/prefs", p); c != http.StatusNoContent {
		t.Fatalf("prefs: %d %s", c, b)
	}
}

// allowAlice turns bob's notifications on for alice's key, subscribed.
func (w *notifyWorld) allowAlice(t *testing.T, mutes ...string) {
	t.Helper()
	w.subscribe(t, w.bob, "https://fcm.googleapis.com/fcm/send/bob-1")
	w.prefs(t, w.bob, protocol.NotifyPrefs{Enabled: true, Mutes: mutes,
		Senders: []protocol.NotifySender{{Address: w.alice.addr, Fingerprint: w.alice.id.Public(w.alice.addr).Fingerprint()}}})
}

func conv(n byte) string { return strings.Repeat(string("0123456789abcdef"[n%16]), 64) }

func (w *notifyWorld) channel(c string) string {
	return protocol.NotifyChannel(c, w.bob.id.Public(w.bob.addr).Fingerprint())
}

// say posts a v2 message from alice to bob in conversation c, asking for
// attention on channel (unless attn is false), at the world's time.
func (w *notifyWorld) say(t *testing.T, c string, attn bool) string {
	t.Helper()
	r, _ := w.bob.id.Public(w.bob.addr).Recipient()
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.alice.addr, To: w.bob.addr, TS: w.now.Unix(), Kind: envelope.KindMessage,
		Body: "x", Conv: c, LID: protocol.NewID(), Root: json.RawMessage(`{"v":1}`), Origin: envelope.OriginUI}
	var env envelope.Envelope
	var err error
	if attn {
		env, err = envelope.SealAttention(in, w.alice.id.Sign, r, w.channel(c))
	} else {
		env, err = envelope.Seal(in, w.alice.id.Sign, r)
	}
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(env)
	if _, err := w.h.store.putMessage(env, canonical, w.alice.id.Public(w.alice.addr).Fingerprint(), w.now); err != nil {
		t.Fatal(err)
	}
	return env.ID
}

type pendingRow struct {
	channel, sender, last string
	gen, count, due       int64
}

func (w *notifyWorld) pending(t *testing.T) []pendingRow {
	t.Helper()
	rows, err := w.h.store.db.Query(`SELECT channel, sender, last_msg, gen, count, due_ms FROM notify_pending ORDER BY channel, sender`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []pendingRow
	for rows.Next() {
		var r pendingRow
		rows.Scan(&r.channel, &r.sender, &r.last, &r.gen, &r.count, &r.due)
		out = append(out, r)
	}
	return out
}

func (w *notifyWorld) round(t *testing.T) {
	t.Helper()
	if _, err := w.h.notifier.round(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Off by default: nothing is queued until the device turns notifications
// on, holds a subscription and allows that exact sender key; a message
// without the hint, a muted channel or another key queue nothing.
func TestNotifyQueuesOnlyWhatTheDeviceAllows(t *testing.T) {
	w := newNotifyWorld(t)
	c1, c2 := conv(1), conv(2)
	w.say(t, c1, true)
	if len(w.pending(t)) != 0 {
		t.Fatal("queued while notifications are off")
	}
	w.subscribe(t, w.bob, "https://fcm.googleapis.com/fcm/send/bob-1")
	w.prefs(t, w.bob, protocol.NotifyPrefs{Enabled: true, Senders: []protocol.NotifySender{{Address: w.alice.addr, Fingerprint: "0000beef-0000beef-0000beef-0000beef"}}})
	w.say(t, c1, true)
	if len(w.pending(t)) != 0 {
		t.Fatal("queued for a sender allowed under another key")
	}
	w.allowAlice(t, w.channel(c2)) // the second DM with alice is muted
	w.say(t, c1, false)
	w.say(t, c2, true)
	if len(w.pending(t)) != 0 {
		t.Fatal("queued without the hint, or on a muted channel")
	}
	id := w.say(t, c1, true)
	if p := w.pending(t); len(p) != 1 || p[0].channel != w.channel(c1) || p[0].last != id || p[0].due != w.now.Add(notifyGrace).UnixMilli() {
		t.Fatalf("pending: %+v", p)
	}
	var st protocol.NotifyState
	_, body := w.bob.call(t, w.h, "GET", "/v1/notify/prefs", nil)
	json.Unmarshal(body, &st)
	if !st.Subscribed || !st.Prefs.Enabled || len(st.Prefs.Mutes) != 1 || strings.Contains(string(body), "fcm.googleapis") {
		t.Fatalf("state: %s", body)
	}
	for _, bad := range []protocol.PushSubscription{
		{Endpoint: "https://evil.example/x", P256DH: pushKey(65, 4), Auth: pushKey(16, 1)},
		{Endpoint: "https://169.254.169.254/x", P256DH: pushKey(65, 4), Auth: pushKey(16, 1)},
	} {
		if c, b := w.bob.call(t, w.h, "PUT", "/v1/notify/subscription", bad); c != http.StatusBadRequest || strings.Contains(string(b), bad.Endpoint) {
			t.Fatalf("bad subscription: %d %s", c, b)
		}
	}
}

// The first message sets the deadline; later ones count, never extend it.
// The device's presentation of the newest message cancels it; of an older
// one does not. Stream and delivery acks never cancel anything.
func TestNotifyGraceAndPresentation(t *testing.T) {
	w := newNotifyWorld(t)
	w.allowAlice(t)
	c := conv(1)
	first := w.say(t, c, true)
	w.now = w.now.Add(3 * time.Second)
	second := w.say(t, c, true)
	p := w.pending(t)
	if len(p) != 1 || p[0].count != 2 || p[0].last != second || p[0].due != time.Unix(1790000000, 0).Add(notifyGrace).UnixMilli() {
		t.Fatalf("pending: %+v", p)
	}
	if c, b := w.bob.call(t, w.h, "POST", "/v1/messages/"+first+"/ack", protocol.AckRequest{State: protocol.StateDelivered}); c != http.StatusOK {
		t.Fatalf("ack: %d %s", c, b)
	}
	seen := func(ids ...string) {
		if c, b := w.bob.call(t, w.h, "POST", "/v1/notify/seen", protocol.NotifySeen{Channel: w.channel(c), IDs: ids}); c != http.StatusNoContent {
			t.Fatalf("seen: %d %s", c, b)
		}
	}
	seen(first)
	if len(w.pending(t)) != 1 {
		t.Fatal("a delivery ack or an older message's presentation cancelled the alert")
	}
	// Alice cannot cancel bob's alert: presentation is the device's own.
	w.alice.call(t, w.h, "POST", "/v1/notify/seen", protocol.NotifySeen{Channel: w.channel(c), IDs: []string{second}})
	if len(w.pending(t)) != 1 {
		t.Fatal("another device cancelled the alert")
	}
	seen(first, second)
	if len(w.pending(t)) != 0 {
		t.Fatal("presenting the newest message did not cancel the alert")
	}
	w.round(t)
	if len(w.push.calls()) != 0 {
		t.Fatal("pushed after presentation")
	}
}

// At its deadline one content-free push goes out, naming the one
// conversation, or a summary for several; limited pushes keep a finite
// deadline; two DMs with one person are separate channels.
func TestNotifyDispatch(t *testing.T) {
	w := newNotifyWorld(t)
	w.allowAlice(t)
	c1, c2 := conv(1), conv(2)
	w.say(t, c1, true)
	w.round(t)
	if len(w.push.calls()) != 0 {
		t.Fatal("pushed before the grace ended")
	}
	w.now = w.now.Add(notifyGrace)
	w.round(t)
	calls := w.push.calls()
	if len(calls) != 1 || calls[0].payload.Channel != w.channel(c1) || calls[0].topic != w.channel(c1) || calls[0].payload.V != 1 || len(w.pending(t)) != 0 {
		t.Fatalf("push: %+v, pending %+v", calls, w.pending(t))
	}
	w.say(t, c1, true)
	w.say(t, c2, true)
	w.now = w.now.Add(notifyGrace)
	w.round(t)
	if calls = w.push.calls(); len(calls) != 2 || calls[1].payload.Channel != "" || calls[1].topic != "summary" {
		t.Fatalf("summary push: %+v", calls)
	}
	// With the device's bucket empty, the next alert waits, finitely.
	w.say(t, c1, true)
	w.now = w.now.Add(notifyGrace)
	w.h.notifier.buckets[w.bob.addr] = &bucket{tokens: 0, at: w.now}
	w.round(t)
	p := w.pending(t)
	if len(p) != 1 || p[0].due <= w.now.UnixMilli() || p[0].due > w.now.Add(pushRefill).UnixMilli() {
		t.Fatalf("rate-limited alert: %+v at %d", p, w.now.UnixMilli())
	}
	w.now = time.UnixMilli(p[0].due)
	w.round(t)
	if len(w.pending(t)) != 0 || len(w.push.calls()) != 3 {
		t.Fatalf("after the wait: %d pushes, pending %+v", len(w.push.calls()), w.pending(t))
	}
}

// Everything is checked again at the deadline: a channel muted, a sender
// removed, notifications turned off or the subscription deleted meanwhile
// send nothing; a revoke drops the device's state.
func TestNotifyRecheckedWhenDue(t *testing.T) {
	for _, change := range []string{"mute", "remove sender", "sender key changed", "sender revoked", "off", "unsubscribe", "revoke"} {
		t.Run(change, func(t *testing.T) {
			w := newNotifyWorld(t)
			w.allowAlice(t)
			c := conv(1)
			w.say(t, c, true)
			switch change {
			case "mute":
				w.allowAlice(t, w.channel(c))
			case "remove sender":
				w.prefs(t, w.bob, protocol.NotifyPrefs{Enabled: true})
			case "sender key changed": // the device now allows another key for alice
				w.prefs(t, w.bob, protocol.NotifyPrefs{Enabled: true,
					Senders: []protocol.NotifySender{{Address: w.alice.addr, Fingerprint: "0000beef-0000beef-0000beef-0000beef"}}})
			case "sender revoked":
				if err := w.h.store.revoke(w.alice.addr); err != nil {
					t.Fatal(err)
				}
			case "off":
				w.prefs(t, w.bob, protocol.NotifyPrefs{})
			case "unsubscribe":
				w.bob.call(t, w.h, "DELETE", "/v1/notify/subscription", nil)
			case "revoke":
				if err := w.h.store.revoke(w.bob.addr); err != nil {
					t.Fatal(err)
				}
			}
			w.now = w.now.Add(notifyGrace)
			w.round(t)
			if len(w.push.calls()) != 0 || len(w.pending(t)) != 0 {
				t.Fatalf("after %s: %d pushes, pending %+v", change, len(w.push.calls()), w.pending(t))
			}
		})
	}
}

// A sender spraying channels gets at most notifyChannelsPerSender exact
// channels and one summary; the summary keeps its sender, so removing the
// sender stops it too.
func TestNotifyFloodBounded(t *testing.T) {
	w := newNotifyWorld(t)
	w.allowAlice(t)
	for i := 0; i < notifyChannelsPerSender+10; i++ {
		w.say(t, conv(byte(i%16))+"", true)
		w.say(t, strings.Repeat("f", 63)+string("0123456789abcdef"[i%16]), true)
		if i > 16 {
			w.say(t, strings.Repeat("e", 62)+string("0123456789abcdef"[i%16])+"0", true)
		}
	}
	p := w.pending(t)
	var summaries int
	for _, r := range p {
		if r.channel == "" {
			summaries++
		}
	}
	if len(p) != notifyChannelsPerSender+1 || summaries != 1 {
		t.Fatalf("%d rows, %d summaries", len(p), summaries)
	}
	w.prefs(t, w.bob, protocol.NotifyPrefs{Enabled: true})
	w.now = w.now.Add(notifyGrace)
	w.round(t)
	if len(w.push.calls()) != 0 || len(w.pending(t)) != 0 {
		t.Fatal("a removed sender's summary was pushed")
	}
}

// Completion is generation-safe: a push in flight completes only what it
// covered. Activity added meanwhile stays pending (due after the grace),
// and a 410 for the old subscription never removes its replacement.
func TestNotifyCompletionIsGenerationSafe(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			w := newNotifyWorld(t)
			w.allowAlice(t)
			c := conv(1)
			w.say(t, c, true)
			w.now = w.now.Add(notifyGrace)
			w.push.status = status
			w.push.block, w.push.started = make(chan struct{}), make(chan struct{}, 1)
			done := make(chan struct{})
			go func() { defer close(done); w.round(t) }()
			<-w.push.started
			newer := w.say(t, c, true) // new activity while the push is out
			w.subscribe(t, w.bob, "https://fcm.googleapis.com/fcm/send/bob-2")
			close(w.push.block)
			<-done
			p := w.pending(t)
			if len(p) != 1 || p[0].last != newer || p[0].due < w.now.Add(notifyGrace).UnixMilli() && status == http.StatusCreated {
				t.Fatalf("after the push: %+v", p)
			}
			var st protocol.NotifyState
			_, body := w.bob.call(t, w.h, "GET", "/v1/notify/prefs", nil)
			json.Unmarshal(body, &st)
			if !st.Subscribed {
				t.Fatal("the old subscription's outcome removed its replacement")
			}
			w.push.block, w.push.started = nil, nil
			w.push.status = http.StatusCreated
			w.now = w.now.Add(2 * notifyGrace)
			w.round(t)
			calls := w.push.calls()
			if last := calls[len(calls)-1]; last.endpoint != "https://fcm.googleapis.com/fcm/send/bob-2" || len(w.pending(t)) != 0 {
				t.Fatalf("the newer activity: %+v, pending %+v", calls, w.pending(t))
			}
		})
	}
}

// A 410 for the current subscription removes it and its alerts; a
// transient failure retries after Retry-After and gives up after its
// attempts; pending alerts survive a Hub restart.
func TestNotifyOutcomesAndRestart(t *testing.T) {
	w := newNotifyWorld(t)
	w.allowAlice(t)
	w.say(t, conv(1), true)
	w.now = w.now.Add(notifyGrace)
	w.push.status = http.StatusGone
	w.round(t)
	var st protocol.NotifyState
	_, body := w.bob.call(t, w.h, "GET", "/v1/notify/prefs", nil)
	json.Unmarshal(body, &st)
	if st.Subscribed || len(w.pending(t)) != 0 {
		t.Fatalf("after 410: %s, pending %+v", body, w.pending(t))
	}
	w.allowAlice(t)
	w.say(t, conv(1), true)
	w.push.status, w.push.retry = http.StatusTooManyRequests, 90*time.Second
	for i := 0; i < pushMaxAttempts; i++ {
		w.now = w.now.Add(pushMaxBackoff)
		w.round(t)
		if p := w.pending(t); i < pushMaxAttempts-1 && (len(p) != 1 || p[0].due != w.now.Add(90*time.Second).UnixMilli()) {
			t.Fatalf("attempt %d: %+v", i, p)
		}
	}
	if len(w.pending(t)) != 0 {
		t.Fatal("still pending after its attempts")
	}

	// Restart: the pending alert is in the database, found again.
	w.push.status, w.push.retry = http.StatusCreated, 0
	w.say(t, conv(2), true)
	dir := w.h.cfg.DataDir
	w.h.Close()
	h, err := Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	w.h = h
	h.notifier.send = w.push.send
	h.notifier.now = func() time.Time { return w.now }
	w.now = w.now.Add(notifyGrace)
	w.round(t)
	if calls := w.push.calls(); len(calls) != 1+pushMaxAttempts+1 || calls[len(calls)-1].payload.Channel != w.channel(conv(2)) {
		t.Fatalf("after restart: %+v", calls)
	}
	if h.push.publicKey == "" || filepath.Base(dir) == "" {
		t.Fatal("no push key")
	}
}
