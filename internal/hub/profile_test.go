package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// firstRoster is m's person's first roster.
func firstRoster(m member, label string) protocol.PersonRoster {
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: label, Devices: []identity.Public{m.id.Public(m.addr)}}
	r.Sign(m.id.Sign)
	return r
}

func rosterFor(m member, label string) []byte {
	data, _ := json.Marshal(firstRoster(m, label))
	return data
}

func capsFor(m member, session string, ts int64, caps ...string) []byte {
	k := protocol.CapsRecord{Address: m.addr, Session: session, Caps: caps, TS: ts}
	k.Sign(m.id.Sign)
	data, _ := json.Marshal(k)
	return data
}

func profileOf(t *testing.T, h *Hub, asker, of member) protocol.Profile {
	t.Helper()
	label, name, _ := protocol.SplitAddress(of.addr)
	c, body := asker.call(t, h, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil)
	if c != http.StatusOK {
		t.Fatalf("profile: %d %s", c, body)
	}
	var p protocol.Profile
	json.Unmarshal(body, &p)
	return p
}

func supportsEnv2(p protocol.Profile, m member) bool {
	return p.Supports(m.addr, m.id.Public(m.addr).SignKey, protocol.CapEnv2)
}

// A device starts its person with a first roster naming only itself, signed
// by its own key; the Hub serves the newest step in profiles, a reference
// to it in member lists, and the chain on request.
func TestPersonPublished(t *testing.T) {
	h, _, _ := testHub(t)
	bob, vit := joinMember(t, h, "bob"), joinMember(t, h, "vitalii")
	first := firstRoster(vit, "Vitalii")
	own, _ := json.Marshal(first)
	if c, b := vit.call(t, h, "PUT", "/v1/person", own); c != http.StatusNoContent {
		t.Fatalf("publish: %d %s", c, b)
	}
	if c, b := vit.call(t, h, "PUT", "/v1/person", own); c != http.StatusNoContent {
		t.Fatalf("the same step again: %d %s", c, b)
	}
	if p := profileOf(t, h, bob, vit); string(p.Person) != string(own) {
		t.Fatalf("profile person %s", p.Person)
	}
	var listed *protocol.PersonRef
	for _, m := range listMembers(t, h, bob).Members {
		if m.Address == vit.addr {
			listed = m.Person
		}
	}
	if listed == nil || *listed != (protocol.PersonRef{ID: first.Person, Seq: 0, Hash: first.Hash()}) {
		t.Fatalf("member list person %+v", listed)
	}
	if page := chainOf(t, h, bob, first.Person, -1); len(page.Records) != 1 || string(page.Records[0]) != string(own) || page.More {
		t.Fatalf("chain %+v", page)
	}
	// Someone else's device, someone else's key, or a bad record: refused.
	other, _ := identity.Generate()
	forged := protocol.PersonRoster{Person: protocol.NewID(), Label: "Vitalii", Devices: []identity.Public{vit.id.Public(vit.addr)}}
	forged.Sign(other.Sign)
	forgedData, _ := json.Marshal(forged)
	for what, body := range map[string][]byte{
		"a record for another device": rosterFor(bob, "Bob"),
		"signed by another key":       forgedData,
		"malformed":                   []byte(`{"person":"x"}`),
		"oversized":                   []byte(`"` + strings.Repeat("x", protocol.MaxPersonRecord) + `"`),
	} {
		if c, _ := vit.call(t, h, "PUT", "/v1/person", body); c != http.StatusBadRequest {
			t.Errorf("%s: %d", what, c)
		}
	}
	if c, _ := vit.call(t, h, "PUT", "/v1/person", rosterFor(vit, "Second")); c != http.StatusConflict {
		t.Errorf("a second person for one device: %d", c)
	}
	if p := profileOf(t, h, bob, vit); string(p.Person) != string(own) {
		t.Fatal("a refused record replaced the published one")
	}
}

func chainOf(t *testing.T, h *Hub, asker member, person string, after int64) protocol.PersonChain {
	t.Helper()
	c, body := asker.call(t, h, "GET", fmt.Sprintf("/v1/persons/%s/chain?after=%d", person, after), nil)
	if c != http.StatusOK {
		t.Fatalf("chain: %d %s", c, body)
	}
	var page protocol.PersonChain
	json.Unmarshal(body, &page)
	return page
}

// What a device can read is decided by its live sessions (all must support
// it), or when none is live by the session that connected last, which
// survives a Hub restart. An older record never replaces a newer one for a
// session, and old sessions' records are pruned.
func TestCapabilitiesBySession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	open := func() *Hub {
		h, err := Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf, SessionGrace: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := open()
	bob, vit := enroll(t, h, "bob"), enroll(t, h, "vitalii")
	connect := func(session string) {
		h.store.setLastSession(vit.addr, session)
		h.presence.connect(vit.addr, protocol.SessionAd{Address: vit.addr, Session: session})
	}
	put := func(body []byte) {
		t.Helper()
		if c, b := vit.call(t, h, "PUT", "/v1/caps", body); c != http.StatusNoContent {
			t.Fatalf("caps: %d %s", c, b)
		}
	}
	oldS, newS := strings.Repeat("a", 32), strings.Repeat("b", 32)

	if p := profileOf(t, h, bob, vit); len(p.Sessions) != 0 || supportsEnv2(p, vit) {
		t.Fatalf("never connected: %+v", p)
	}
	connect(newS)
	put(capsFor(vit, newS, 10, protocol.CapEnv2))
	if p := profileOf(t, h, bob, vit); !p.Live || !supportsEnv2(p, vit) {
		t.Fatalf("one new session: %+v", p)
	}
	connect(oldS) // an older program connects too, publishing nothing
	if p := profileOf(t, h, bob, vit); len(p.Sessions) != 2 || supportsEnv2(p, vit) {
		t.Fatalf("old and new sessions: %+v", p)
	}
	h.presence.disconnect(vit.addr, oldS)
	h.presence.disconnect(vit.addr, newS) // the new session's connection drops,
	connect(newS)                         // and the same session reconnects within grace
	time.Sleep(80 * time.Millisecond)     // the old one is past grace: only the new one is live
	if p := profileOf(t, h, bob, vit); !supportsEnv2(p, vit) {
		t.Fatalf("after the old session ended: %+v", p)
	}
	put(capsFor(vit, newS, 5)) // a stale, older record for that session
	if p := profileOf(t, h, bob, vit); !supportsEnv2(p, vit) {
		t.Fatal("an older record replaced a newer one")
	}

	h.presence.disconnect(vit.addr, newS)
	time.Sleep(80 * time.Millisecond)
	if p := profileOf(t, h, bob, vit); p.Live || len(p.Sessions) != 1 || !supportsEnv2(p, vit) {
		t.Fatalf("offline keeps the last session's: %+v", p)
	}
	h.Close()
	h = open()
	t.Cleanup(func() { h.Close() })
	if p := profileOf(t, h, bob, vit); p.Live || !supportsEnv2(p, vit) {
		t.Fatalf("after a Hub restart, offline: %+v", p)
	}
	// The last session to connect was an older program: offline, it decides.
	h.store.setLastSession(vit.addr, oldS)
	if p := profileOf(t, h, bob, vit); supportsEnv2(p, vit) {
		t.Fatal("offline after an older program: still said to support conversations")
	}

	for i := range 12 {
		put(capsFor(vit, fmt.Sprintf("%032x", 1000+i), int64(100+i), protocol.CapEnv2))
	}
	var n int
	h.store.db.QueryRow(`SELECT count(*) FROM caps WHERE address = ?`, vit.addr).Scan(&n)
	if n > maxCapsSessions+1 {
		t.Fatalf("%d capability records kept", n)
	}
	if c, _ := vit.call(t, h, "PUT", "/v1/caps", capsFor(bob, newS, 200, protocol.CapEnv2)); c != http.StatusBadRequest {
		t.Fatalf("a record for another device: %d", c)
	}
}

// The Hub lists what it supports, and relays version 2 envelopes.
func TestFeaturesAndVersion2(t *testing.T) {
	h, _, _ := testHub(t)
	w := serve(h, httptestGet("/v1/version"))
	var v protocol.VersionInfo
	json.Unmarshal(w.Body.Bytes(), &v)
	for _, f := range []string{protocol.FeatureMembers, protocol.FeatureEnv2, protocol.FeaturePerson, protocol.FeatureCaps} {
		if !strings.Contains(strings.Join(v.Features, ","), f) {
			t.Errorf("feature %s not listed: %v", f, v.Features)
		}
	}
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	r, _ := bob.id.Public(bob.addr).Recipient()
	env, err := envelope.Seal(envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: alice.addr, To: bob.addr, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: "hi", Conv: strings.Repeat("c", 64), LID: protocol.NewID(), Root: json.RawMessage(`{}`)}, alice.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if c, b := alice.call(t, h, "POST", "/v1/messages", env); c != http.StatusAccepted {
		t.Fatalf("version 2 post: %d %s", c, b)
	}
}

func httptestGet(path string) *http.Request { return httptest.NewRequest("GET", path, nil) }
