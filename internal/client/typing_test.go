package client

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func typingFixture(t *testing.T) (*world, protocol.TypingScope) {
	t.Helper()
	w := teamWorld(t)
	for _, pair := range [][2]*Agent{{w.alice, w.bob}, {w.bob, w.alice}} {
		a, b := pair[0], pair[1]
		if err := a.store.pin(b.Self()); err != nil {
			t.Fatal(err)
		}
		p, _, _ := b.Person()
		if _, err := a.refreshPerson(tctx(t), p.Person, false); err != nil {
			t.Fatal(err)
		}
	}
	sent, err := w.alice.Send(tctx(t), w.bob.Address, "synthetic existing thread", "")
	if err != nil {
		t.Fatal(err)
	}
	env, err := w.alice.store.outboxEnvelope(sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.bob.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		a.session = protocol.NewID()
		h := make(http.Header)
		h.Set(protocol.SignalsHeader, "1")
		a.typingConnected(h)
		m, err := a.Members(tctx(t))
		if err != nil {
			t.Fatal(err)
		}
		for i := range m.Members {
			m.Members[i].Presence = protocol.PresenceConnected
		}
		raw, _ := json.Marshal(m)
		a.onMembers(raw)
		t.Cleanup(a.typingDisconnected)
	}
	return w, protocol.TypingScope{Peer: w.alice.Address, Thread: sent.ID}
}
func typingWire(t *testing.T, from, to *Agent, scope protocol.TypingScope, active bool, ts int64) protocol.Signal {
	t.Helper()
	realm, _ := from.RealmID()
	in := protocol.TypingPlain{V: 1, ID: protocol.NewID(), From: from.Address, To: to.Address, Session: to.session, TS: ts, Realm: realm, Conv: scope.Conv, Thread: scope.Thread, Origin: "human", Active: active}
	v, err := protocol.SealTyping(in, from.id.Sign, to.Self())
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func applyTyping(a *Agent, s protocol.Signal) { raw, _ := json.Marshal(s); a.onSignal(raw) }
func typingCount(t *testing.T, a *Agent, scope protocol.TypingScope) int {
	t.Helper()
	v, err := a.Typing(scope)
	if err != nil {
		t.Fatal(err)
	}
	return len(v.Entries)
}
func TestTypingLegacyStopWinsReorderingReplayAndZeroRows(t *testing.T) {
	w, scope := typingFixture(t)
	before := realmRows(t, w.bob.store.db)
	ts := time.Now().UnixMilli()
	start := typingWire(t, w.alice, w.bob, scope, true, ts)
	applyTyping(w.bob, start)
	if typingCount(t, w.bob, scope) != 1 {
		t.Fatal("active known composer not shown")
	}
	applyTyping(w.bob, typingWire(t, w.alice, w.bob, scope, false, ts))
	applyTyping(w.bob, start)
	applyTyping(w.bob, typingWire(t, w.alice, w.bob, scope, true, ts))
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("replayed/tied start overrode stop")
	}
	applyTyping(w.bob, typingWire(t, w.alice, w.bob, scope, true, ts+1))
	if typingCount(t, w.bob, scope) != 1 {
		t.Fatal("newer start not accepted")
	}
	wrong := scope
	wrong.Thread = protocol.NewID()
	applyTyping(w.bob, typingWire(t, w.alice, w.bob, wrong, true, ts+2))
	if typingCount(t, w.bob, wrong) != 0 {
		t.Fatal("phantom thread created")
	}
	if after := realmRows(t, w.bob.store.db); !reflect.DeepEqual(before, after) {
		t.Fatal("signal mutated config/pins/grants/inbox/outbox")
	}
	for _, table := range []string{"conversations", "history_jobs", "participation_events", "operators", "reported"} {
		var n int
		if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("typing created %s: %d %v", table, n, err)
		}
	}
}
func TestTypingExpiryDisconnectSessionRealmAndPrivacy(t *testing.T) {
	w, scope := typingFixture(t)
	applyTyping(w.bob, typingWire(t, w.alice, w.bob, scope, true, time.Now().Add(-4500*time.Millisecond).UnixMilli()))
	if typingCount(t, w.bob, scope) != 1 {
		t.Fatal("fresh delayed signal missing")
	}
	_, changed := w.bob.Changed()
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("expiry timer did not refresh UI")
	}
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("typing outlived signed TTL")
	}
	v := typingWire(t, w.alice, w.bob, scope, true, time.Now().UnixMilli())
	applyTyping(w.bob, v)
	w.bob.typingDisconnected()
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("disconnect retained indicator")
	}
	h := make(http.Header)
	h.Set(protocol.SignalsHeader, "1")
	w.bob.session = protocol.NewID()
	w.bob.typingConnected(h)
	applyTyping(w.bob, v)
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("old recipient-run signal resurrected after restart")
	}
	realm, _ := w.alice.RealmID()
	in := protocol.TypingPlain{V: 1, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, Session: w.bob.session, TS: time.Now().UnixMilli(), Realm: protocol.NewID(), Thread: scope.Thread, Origin: "human", Active: true}
	foreign, _ := protocol.SealTyping(in, w.alice.id.Sign, w.bob.Self())
	applyTyping(w.bob, foreign)
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("cross-workspace indicator")
	}
	_ = realm
	if err := w.bob.SetTypingPreferences(TypingPreferences{Send: true, Show: false}); err != nil {
		t.Fatal(err)
	}
	applyTyping(w.bob, typingWire(t, w.alice, w.bob, scope, true, time.Now().UnixMilli()))
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("show opt-out ignored")
	}
}
func TestTypingChangedKeyUnknownContactAndCurrentMembership(t *testing.T) {
	w, scope := typingFixture(t)
	v := typingWire(t, w.alice, w.bob, scope, true, time.Now().UnixMilli())
	bad := v
	bad.Sig = ed25519.Sign(w.bob.id.Sign, bad.Canonical())
	applyTyping(w.bob, bad)
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("wrong signing key shown")
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.setPending(id.Public(w.alice.Address)); err != nil {
		t.Fatal(err)
	}
	applyTyping(w.bob, v)
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("changed key bypassed trust gate")
	}
	if _, err := w.bob.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address); err != nil {
		t.Fatal(err)
	}
	w.bob.members.mu.Lock()
	w.bob.members.view.Members.Members = nil
	w.bob.members.mu.Unlock()
	applyTyping(w.bob, v)
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("nonmember shown")
	}
	before := realmRows(t, w.bob.store.db)
	unknown := protocol.TypingPlain{V: 1, ID: protocol.NewID(), From: "unknown/desk", To: w.bob.Address, Session: w.bob.session, TS: time.Now().UnixMilli(), Thread: scope.Thread, Origin: "human"}
	unknown.Realm, _ = w.bob.RealmID()
	s, _ := protocol.SealTyping(unknown, id.Sign, w.bob.Self())
	applyTyping(w.bob, s)
	if !reflect.DeepEqual(before, realmRows(t, w.bob.store.db)) {
		t.Fatal("unknown signal pinned contact or created rows")
	}
}
func TestTypingGroupRequiresEffectiveMembership(t *testing.T) {
	w, _ := typingFixture(t)
	a, _, _ := w.alice.Person()
	b, _, _ := w.bob.Person()
	root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Members: []protocol.ConvMember{{Person: a.Person, Roster: a.Roster}, {Person: b.Person, Roster: b.Roster}}, Nonce: protocol.NewID()}
	raw, _ := json.Marshal(root)
	if err := w.bob.store.addConversation(root, raw, a.Person); err != nil {
		t.Fatal(err)
	}
	scope := protocol.TypingScope{Conv: root.ID()}
	v := typingWire(t, w.alice, w.bob, scope, true, time.Now().UnixMilli())
	w.bob.typing.mu.Lock()
	w.bob.typing.groupMembers = nil
	w.bob.typing.mu.Unlock()
	applyTyping(w.bob, v)
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("group inferred frozen-root membership")
	}
	w.bob.typing.mu.Lock()
	w.bob.typing.groupMembers = func(string) ([]protocol.ConvMember, error) { return root.Members, nil }
	w.bob.typing.mu.Unlock()
	applyTyping(w.bob, v)
	if typingCount(t, w.bob, scope) != 1 {
		t.Fatal("verified effective member not shown")
	}
	w.bob.typing.mu.Lock()
	w.bob.typing.groupMembers = func(string) ([]protocol.ConvMember, error) { return root.Members[1:], nil }
	w.bob.typing.mu.Unlock()
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("withdrawn sender still shown from frozen root")
	}
	w.bob.typing.mu.Lock()
	w.bob.typing.groupMembers = func(string) ([]protocol.ConvMember, error) { return nil, errors.New("verified conflict") }
	w.bob.typing.mu.Unlock()
	if typingCount(t, w.bob, scope) != 0 {
		t.Fatal("conflicting group shown")
	}
}

type typingTransport struct {
	base  http.RoundTripper
	who   *Agent
	old   bool
	mu    sync.Mutex
	posts []protocol.Signal
}

func (r *typingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/profile") {
		caps := protocol.CapsRecord{Address: r.who.Address, Session: r.who.session, Caps: []string{protocol.CapTyping}, TS: time.Now().Unix()}
		if r.old {
			caps.Caps = nil
		}
		caps.Sign(r.who.id.Sign)
		raw, _ := json.Marshal(caps)
		p := protocol.Profile{Live: true, Sessions: []string{r.who.session}, Caps: []json.RawMessage{raw}}
		body, _ := json.Marshal(p)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
	}
	if req.URL.Path == "/v1/signal" {
		var s protocol.Signal
		if err := json.NewDecoder(req.Body).Decode(&s); err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.posts = append(r.posts, s)
		r.mu.Unlock()
		return &http.Response{StatusCode: 202, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	return r.base.RoundTrip(req)
}
func TestTypingComposerThrottleOldPeerAndPreferences(t *testing.T) {
	w, scope := typingFixture(t)
	scope.Peer = w.bob.Address
	r := &typingTransport{base: w.alice.hub.http.Transport, who: w.bob}
	w.alice.hub.http.Transport = r
	res, err := w.alice.SendTyping(tctx(t), scope, true)
	if err != nil || res.Submitted != 1 {
		t.Fatalf("send: %+v %v", res, err)
	}
	res, err = w.alice.SendTyping(tctx(t), scope, true)
	if err != nil || !res.Throttled || res.Submitted != 0 {
		t.Fatalf("composer throttle: %+v %v", res, err)
	}
	res, err = w.alice.SendTyping(tctx(t), scope, false)
	if err != nil || res.Submitted != 1 {
		t.Fatalf("clear was throttled: %+v %v", res, err)
	}
	r.old = true
	res, err = w.alice.SendTyping(tctx(t), scope, true)
	if err != nil || res.Submitted != 0 || res.Skipped != 1 {
		t.Fatalf("old peer: %+v %v", res, err)
	}
	if err := w.alice.SetTypingPreferences(TypingPreferences{Send: false, Show: true}); err != nil {
		t.Fatal(err)
	}
	res, err = w.alice.SendTyping(context.Background(), scope, true)
	if err != nil || res.Submitted != 0 {
		t.Fatal("send opt-out ignored")
	}
	u := newWorld(t, "")
	for _, a := range []*Agent{u.alice, u.bob} {
		p, err := a.TypingPreferences()
		if err != nil || p.Send || p.Show {
			t.Fatalf("unset inferred human: %+v %v", p, err)
		}
	}
	if err := u.bob.SetService(); err != nil {
		t.Fatal(err)
	}
	if p, _ := u.bob.TypingPreferences(); p.Send || p.Show {
		t.Fatal("service defaults enabled")
	}
	if err := u.bob.SetTypingPreferences(TypingPreferences{Send: true, Show: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := u.bob.TypingPreferences(); !p.Send || !p.Show {
		t.Fatal("explicit local opt-in ignored")
	}
}
