package hub

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Publishing the same capability record again, or an older one, changes
// nothing, so it moves the member list on for nobody; a new one does (HP-3).
func TestUnchangedCapsKeepMembers(t *testing.T) {
	h, _, _ := testHub(t)
	bob := enroll(t, h, "bob")
	session := protocol.NewID()
	put := func(rec []byte) int64 {
		t.Helper()
		before := h.membersGen.Load()
		if c, b := bob.call(t, h, "PUT", "/v1/caps", rec); c != http.StatusNoContent {
			t.Fatalf("caps: %d %s", c, b)
		}
		return h.membersGen.Load() - before
	}
	rec := capsFor(bob, session, 100, protocol.CapEnv2)
	for _, c := range []struct {
		what string
		rec  []byte
		want int64
	}{
		{"first", rec, 1},
		{"the same again", rec, 0},
		{"an older one", capsFor(bob, session, 99, protocol.CapEnv2), 0},
		{"a newer one", capsFor(bob, session, 101, protocol.CapEnv2), 1},
		{"another session's", capsFor(bob, protocol.NewID(), 50, protocol.CapEnv2), 1},
	} {
		if d := put(c.rec); d != c.want {
			t.Errorf("%s moved the member list on by %d, want %d", c.what, d, c.want)
		}
	}
}

// A stream sends the member list and team directory only when they differ
// from what it sent last, however often their generation moves (HP-3);
// presence changes still arrive, and a newly published capability record
// sends the list again even unchanged, so senders waiting for it look again.
func TestStreamSkipsUnchangedMemberList(t *testing.T) {
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: 300 * time.Millisecond, SessionGrace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	events := pushStream(t, h, bob)
	next := func() pushEvent {
		t.Helper()
		return nextEvent(t, h, bob, "", events)
	}
	// after reads up to message id; it must come next when nothing is
	// expected before it.
	after := func(what string, expect ...string) {
		t.Helper()
		id := sendMessage(t, h, alice, bob)
		for _, name := range expect {
			if e := next(); e.name != name {
				t.Fatalf("%s: %s %s, want %s", what, e.name, e.data, name)
			}
		}
		if e := next(); e.name != "message" || envelopeID(t, e.data) != id {
			t.Fatalf("%s: %s %s, want the message", what, e.name, e.data)
		}
	}
	for e := next(); e.name != "groups"; e = next() { // the lists sent on connect
	}
	after("connected")
	for range 3 {
		h.membersChanged() // nothing changed
	}
	after("unchanged lists")
	h.presence.connect(alice.addr, testAd(alice))
	if e := next(); e.name != "members" || memberOfData(t, e.data, alice.addr).Presence != protocol.PresenceConnected {
		t.Fatalf("presence change: %s %s", e.name, e.data)
	}
	after("after the presence change") // the team directory, unchanged, was not sent again
	// What receivers look again for on each list, which the list does not
	// show, sends both again unchanged.
	if c, b := alice.call(t, h, "PUT", "/v1/caps", capsFor(alice, protocol.NewID(), 1, protocol.CapEnv2)); c != http.StatusNoContent {
		t.Fatalf("caps: %d %s", c, b)
	}
	after("after new capabilities", "members", "teams")
	if c, b := alice.call(t, h, "PUT", "/v1/agent-catalog", []byte("[]")); c != http.StatusNoContent {
		t.Fatalf("agent catalog: %d %s", c, b)
	}
	after("after an agent catalog", "members", "teams")
	after("nothing changed since")
}

// A device's session that starts or ends while the device stays connected
// leaves its member list entry the same, but what it supports changes
// (every live session's capabilities count): an updated program's session
// outlives the older one, whose end lets what waited for the update go.
// Every stream sends the list again then, so senders look again.
func TestSessionStartAndEndSendMembersAgain(t *testing.T) {
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: 300 * time.Millisecond, SessionGrace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	events := pushStream(t, h, bob)
	next := func() pushEvent {
		t.Helper()
		return nextEvent(t, h, bob, "", events)
	}
	after := func(what string, expect ...string) {
		t.Helper()
		id := sendMessage(t, h, alice, bob)
		for _, name := range expect {
			if e := next(); e.name != name {
				t.Fatalf("%s: %s %s, want %s", what, e.name, e.data, name)
			}
		}
		if e := next(); e.name != "message" || envelopeID(t, e.data) != id {
			t.Fatalf("%s: %s %s, want the message", what, e.name, e.data)
		}
	}
	for e := next(); e.name != "groups"; e = next() { // the lists sent on connect
	}
	old, updated := testAd(alice), testAd(alice)
	h.presence.connect(alice.addr, old)
	if e := next(); e.name != "members" || memberOfData(t, e.data, alice.addr).Presence != protocol.PresenceConnected {
		t.Fatalf("presence change: %s %s", e.name, e.data)
	}
	after("connected")
	h.presence.connect(alice.addr, updated) // the updated program, while the old one still runs
	after("a second session started", "members", "teams")
	h.presence.disconnect(alice.addr, old.Session)
	after("the old session disconnected, within its grace") // still connected: nothing changed
	endGrace(t, h, alice.addr, old.Session)
	after("the old session ended", "members", "teams")
	if ids := h.presence.sessionIDs(alice.addr); len(ids) != 1 || ids[0] != updated.Session {
		t.Fatalf("live sessions %v", ids)
	}
	after("nothing changed since")
}

// nextEvent is the next event of m's stream other than a ping, which it
// acknowledges as m's client of version would.
func nextEvent(t *testing.T, h *Hub, m member, version string, events <-chan pushEvent) pushEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("the stream of %s ended", m.addr)
			}
			if e.name != "ping" {
				return e
			}
			var p protocol.PingAck
			json.Unmarshal([]byte(e.data), &p)
			if c, b := m.callAs(t, h, version, "POST", "/v1/stream/ack", p); c != http.StatusNoContent {
				t.Fatalf("ping acknowledgement: %d %s", c, b)
			}
		case <-deadline:
			t.Fatalf("no event but pings on the stream of %s", m.addr)
		}
	}
}

func memberOfData(t *testing.T, data, addr string) protocol.Member {
	t.Helper()
	var ms protocol.Members
	if err := json.Unmarshal([]byte(data), &ms); err != nil {
		t.Fatal(err)
	}
	return memberOf(t, ms, addr)
}
