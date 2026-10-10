package hub

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func laneTestEnvelope(t *testing.T, from, to member, attachments ...envelope.Attachment) envelope.Envelope {
	t.Helper()
	recipient, _ := to.id.Public(to.addr).Recipient()
	e, err := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: from.addr, To: to.addr, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "transport fixture", Attachments: attachments}, from.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func lanePost(t *testing.T, h *Hub, from member, e envelope.Envelope, lane string) {
	t.Helper()
	path := "/v1/messages"
	if lane != "" {
		path += "?lane=" + lane
	}
	if c, b := from.call(t, h, "POST", path, e); c != http.StatusAccepted {
		t.Fatalf("post lane %q: %d %s", lane, c, b)
	}
}

func laneValue(t *testing.T, h *Hub, id string) string {
	t.Helper()
	var lane string
	if err := h.store.db.QueryRow(`SELECT lane FROM messages WHERE id=?`, id).Scan(&lane); err != nil {
		t.Fatal(err)
	}
	return lane
}

func laneAck(t *testing.T, h *Hub, to member, id, state string) {
	t.Helper()
	if c, b := to.call(t, h, "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: state}); c != http.StatusOK {
		t.Fatalf("ack: %d %s", c, b)
	}
}

func nextLaneMessage(t *testing.T, events <-chan pushEvent) string {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("stream ended")
			}
			if e.name == "message" {
				return envelopeID(t, e.data)
			}
		case <-timer.C:
			t.Fatal("stream did not send the next message")
		}
	}
}

func TestMessageLaneOwnedCustodyDemotion(t *testing.T) {
	h, alice, bob, carol := blobHub(t, 1<<30)
	ct := []byte("unchanged encrypted attachment")
	blobID := alice.upload(t, h, bob.addr, ct)
	e := laneTestEnvelope(t, alice, bob, envelope.Attachment{Name: "fixture", Size: 10, SHA256: digest([]byte("fixture")), Blob: envelope.Blob{ID: blobID, Size: int64(len(ct)), SHA256: digest(ct)}})
	lanePost(t, h, alice, e, "")
	canonical, _ := json.Marshal(e)
	if laneValue(t, h, e.ID) != protocol.MessageLaneLive {
		t.Fatal("legacy request did not default live")
	}
	// Authentication covers the routing query as well as the unchanged body.
	r := signed(t, alice.id, alice.addr, "POST", "/v1/messages", canonical)
	r.URL.RawQuery = "lane=sync"
	r.RequestURI = ""
	if w := serve(h, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned query change: %d", w.Code)
	}
	if c, _ := carol.call(t, h, "POST", "/v1/messages?lane=sync", e); c != http.StatusForbidden {
		t.Fatalf("another owner reclassified message: %d", c)
	}
	bad := e
	bad.Sig = make([]byte, ed25519.SignatureSize)
	if c, _ := alice.call(t, h, "POST", "/v1/messages?lane=sync", bad); c != http.StatusBadRequest {
		t.Fatalf("bad envelope signature: %d", c)
	}
	// Valid alternate content under the same ID must still conflict.
	other := laneTestEnvelope(t, alice, bob)
	other.ID = e.ID
	// A different signed envelope needs its encrypted header resealed too.
	recipient, _ := bob.id.Public(bob.addr).Recipient()
	other, _ = envelope.Seal(envelope.Inner{ID: e.ID, From: alice.addr, To: bob.addr, TS: e.TS, Kind: envelope.KindMessage, Body: "different"}, alice.id.Sign, recipient)
	if c, _ := alice.call(t, h, "POST", "/v1/messages?lane=sync", other); c != http.StatusConflict {
		t.Fatalf("changed content reclassified message: %d", c)
	}
	if laneValue(t, h, e.ID) != protocol.MessageLaneLive {
		t.Fatal("failed retry changed lane")
	}
	for range 2 {
		lanePost(t, h, alice, e, protocol.MessageLaneSync)
	}
	for _, lane := range []string{"", protocol.MessageLaneLive} {
		lanePost(t, h, alice, e, lane)
		if laneValue(t, h, e.ID) != protocol.MessageLaneSync {
			t.Fatal("retry promoted sync to live")
		}
	}
	var stored []byte
	var state, attached string
	var receipts int
	if err := h.store.db.QueryRow(`SELECT envelope,state FROM messages WHERE id=?`, e.ID).Scan(&stored, &state); err != nil {
		t.Fatal(err)
	}
	if err := h.store.db.QueryRow(`SELECT message_id FROM blobs WHERE id=?`, blobID).Scan(&attached); err != nil {
		t.Fatal(err)
	}
	if err := h.store.db.QueryRow(`SELECT count(*) FROM receipts WHERE id=?`, e.ID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, canonical) || state != protocol.StateCustody || attached != e.ID || receipts != 0 {
		t.Fatal("demotion changed envelope, attachment or receipt")
	}
	terminal := laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, terminal, "")
	laneAck(t, h, bob, terminal.ID, protocol.StateDelivered)
	lanePost(t, h, alice, terminal, protocol.MessageLaneSync)
	if laneValue(t, h, terminal.ID) != protocol.MessageLaneLive {
		t.Fatal("terminal copy reclassified")
	}
	for _, query := range []string{"lane=", "lane=invalid", "lane=sync&lane=live"} {
		if c, _ := alice.call(t, h, "POST", "/v1/messages?"+query, e); c != http.StatusBadRequest {
			t.Fatalf("invalid lane %q: %d", query, c)
		}
	}
}

func TestMessageLaneLegacyCatchupLiveBypassAndReconnect(t *testing.T) {
	h, alice, bob, _ := blobHub(t, 1<<30)
	const backlog = 110
	old := make([]envelope.Envelope, backlog)
	for i := range old {
		old[i] = laneTestEnvelope(t, alice, bob)
		lanePost(t, h, alice, old[i], "") // existing custody defaults live
	}
	for _, e := range old {
		lanePost(t, h, alice, e, protocol.MessageLaneSync)
	}
	fresh := laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, fresh, "")
	events := pushStream(t, h, bob)
	defer h.streams.disconnect(bob.addr) // close an idle handler before pipe cleanup
	if got := nextLaneMessage(t, events); got != fresh.ID {
		t.Fatalf("initial live %s, got %s", fresh.ID, got)
	}
	laneAck(t, h, bob, fresh.ID, protocol.StateDelivered)
	if got := nextLaneMessage(t, events); got != old[0].ID {
		t.Fatalf("first sync %s, got %s", old[0].ID, got)
	}
	late := laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, late, "")
	if got := nextLaneMessage(t, events); got != late.ID {
		t.Fatalf("late live waited for sync ack: %s", got)
	}
	laneAck(t, h, bob, late.ID, protocol.StateDelivered)
	// An unrelated terminal receipt wakes the stream but cannot free sync.
	marker := laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, marker, "")
	if got := nextLaneMessage(t, events); got != marker.ID {
		t.Fatalf("unrelated receipt released sync: %s", got)
	}
	laneAck(t, h, bob, marker.ID, protocol.StateDelivered)
	laneAck(t, h, bob, old[0].ID, protocol.StateQuarantined)
	if got := nextLaneMessage(t, events); got != old[1].ID {
		t.Fatalf("quarantine did not resume sync: %s", got)
	}
	h.streams.disconnect(bob.addr)
	// Reconnect resets live delivery but keeps the unacknowledged sync copy.
	fresh = laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, fresh, "")
	events = pushStream(t, h, bob)
	if got := nextLaneMessage(t, events); got != fresh.ID {
		t.Fatalf("reconnect live was not first: %s", got)
	}
	laneAck(t, h, bob, fresh.ID, protocol.StateDelivered)
	for i := 1; i < len(old); i++ {
		if got := nextLaneMessage(t, events); got != old[i].ID {
			t.Fatalf("sync %d: got %s want %s", i, got, old[i].ID)
		}
		laneAck(t, h, bob, old[i].ID, protocol.StateDelivered)
	}
	var remaining int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM messages WHERE recipient=? AND lane=? AND state=?`, bob.addr, protocol.MessageLaneSync, protocol.StateCustody).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("sync custody remaining %d: %v", remaining, err)
	}
}

func TestMessageLaneLateOlderDemotionAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	config := Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Heartbeat: time.Minute, Logf: t.Logf}
	h, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	older := laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, older, "")
	newer := laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, newer, protocol.MessageLaneSync)
	events := pushStream(t, h, bob)
	defer func() { h.streams.disconnect(bob.addr) }() // h changes on the real reopen below
	if got := nextLaneMessage(t, events); got != older.ID {
		t.Fatalf("older live: %s", got)
	}
	if got := nextLaneMessage(t, events); got != newer.ID {
		t.Fatalf("newer sync: %s", got)
	}
	// This sequence is behind the sync turn already sent. A sync cursor
	// would skip it; querying oldest custody after the ack must recover it.
	lanePost(t, h, alice, older, protocol.MessageLaneSync)
	laneAck(t, h, bob, newer.ID, protocol.StateDelivered)
	if got := nextLaneMessage(t, events); got != older.ID {
		t.Fatalf("late demoted older copy skipped: %s", got)
	}
	h.streams.disconnect(bob.addr)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	if laneValue(t, h, older.ID) != protocol.MessageLaneSync {
		t.Fatal("demotion did not survive restart")
	}
	fresh := laneTestEnvelope(t, alice, bob)
	lanePost(t, h, alice, fresh, "")
	events = pushStream(t, h, bob)
	if got := nextLaneMessage(t, events); got != fresh.ID {
		t.Fatalf("restart live was not first: %s", got)
	}
	if got := nextLaneMessage(t, events); got != older.ID {
		t.Fatalf("restart lost unacknowledged sync: %s", got)
	}
	laneAck(t, h, bob, older.ID, protocol.StateDelivered)
}
