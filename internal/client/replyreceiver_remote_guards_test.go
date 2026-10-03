package client

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func TestReceiverRemoteReadyRollbackDeclineCatalog(t *testing.T) {
	w, phone, _, _ := remoteReceiverWorld(t)
	sent, setup := remotePrepared(t, w, phone, envelope.KindMessage)
	b := groupReceiverBound(t, phone, sent.ID)
	full, e := replyReceiverIn(phone.store.db, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	route := full.remote.Route
	route.Op = "ready"
	for _, bad := range []string{"digest", "host-key", "reference", "signer"} {
		in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: w.alice.Address, To: phone.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: `{"v":1}`, ReplyTo: setup}
		copy := route
		in.ReceiverRoute = &copy
		key := w.alice.Self().Fingerprint()
		switch bad {
		case "digest":
			copy.RequestDigest = strings.Repeat("a", 64)
		case "host-key":
			copy.HostKey = w.bob.Self().Fingerprint()
		case "reference":
			in.ReplyTo = protocol.NewID()
		case "signer":
			key = w.bob.Self().Fingerprint()
		}
		if e = phone.acceptReceiverReady(in, key); e == nil {
			t.Fatalf("forged ready %s accepted", bad)
		}
	}
	var originalState string
	phone.store.db.QueryRow(`SELECT state FROM outbox WHERE id=?`, sent.ID).Scan(&originalState)
	if originalState != stateReceiverWaiting {
		t.Fatal("forged ready released original")
	}
	if _, e = w.alice.Decline(tctx(t), setup, "EXPLICIT HOST REFUSAL"); e != nil {
		t.Fatal(e)
	}
	eventually(t, "signed explicit refusal", func() bool {
		b, e := replyReceiverIn(phone.store.db, full.ID)
		return e == nil && b.remote.Refusal == "EXPLICIT HOST REFUSAL" && !b.remote.Ready
	})
	var seen int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, sent.ID).Scan(&seen)
	if seen != 0 {
		t.Fatal("decline exposed original")
	}
	var before, after int
	phone.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&before)
	if _, e = phone.store.db.Exec(`CREATE TRIGGER fail_remote_binding BEFORE INSERT ON reply_receivers BEGIN SELECT RAISE(ABORT,'injected remote binding rollback'); END`); e != nil {
		t.Fatal(e)
	}
	r := ReplyReceiver{Kind: "human", Host: &ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}}
	if _, e = phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "ROLLBACK ORIGINAL", ReplyReceiver: &r}); e == nil {
		t.Fatal("failed binding committed request")
	}
	phone.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&after)
	if before != after {
		t.Fatal("rollback left original or delegation outbox")
	}
	phone.store.db.Exec(`DROP TRIGGER fail_remote_binding`)
	owner, call := nativeReceiverFixture(t, w.alice, "pi")
	other, otherCall := nativeReceiverFixture(t, w.alice, "omp")
	if _, e = w.alice.RegisterReplySession(ReplySessionRegistration{Harness: "omp", SessionID: otherCall.SessionID, File: otherCall.File, Leaf: otherCall.Leaf, Handle: other.Handle, Label: "Consciously shared label"}); e != nil {
		t.Fatal(e)
	}
	host := *r.Host
	for range 2 {
		if _, e = phone.ReplyReceiverCatalog(tctx(t), host); e != nil {
			t.Fatal(e)
		}
	}
	var catalog ReplyReceiverCatalog
	eventually(t, "authenticated exact registration snapshot", func() bool {
		catalog, e = phone.ReplyReceiverCatalog(tctx(t), host)
		return e == nil && catalog.Status == "ready" && len(catalog.Sessions) == 2
	})
	if catalog.Local || catalog.HostKey != host.Fingerprint {
		t.Fatalf("wrong catalog %+v", catalog)
	}
	defaultFound, explicitFound := false, false
	for _, s := range catalog.Sessions {
		if s.Handle == owner.Handle && s.Label == "pi session" {
			defaultFound = true
		}
		if s.Handle == other.Handle && s.Label == "Consciously shared label" {
			explicitFound = true
		}
	}
	if !defaultFound || !explicitFound {
		t.Fatal("default private identity or explicit label changed")
	}
	raw, _ := json.Marshal(catalog)
	if strings.Contains(string(raw), owner.OwnerToken) || strings.Contains(string(raw), call.SessionID) || strings.Contains(string(raw), call.File) {
		t.Fatal("catalog leaked native identity/owner secrets")
	}
	var requests int
	phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND coalesce(reply_to,'')='' AND required_cap=? AND body=?`, host.Address, protocol.CapReplyReceiver, `{"v":1}`).Scan(&requests)
	if requests != 1 {
		t.Fatalf("repeated GET minted %d requests", requests)
	}
	if _, e = phone.ReplyReceiverCatalog(tctx(t), ReplyReceiverHost{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}); e == nil {
		t.Fatal("foreign-person catalog accepted")
	}
	if _, e = phone.ReplyReceiverCatalog(tctx(t), ReplyReceiverHost{Address: host.Address, Fingerprint: w.bob.Self().Fingerprint()}); e == nil {
		t.Fatal("changed own-host key accepted")
	}
}

func TestReceiverRouteMigrationAndQuietAttention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	routeStep := slices.Index(schema, receiverRouteSchema)
	if routeStep < 0 || routeStep+1 >= len(schema) || schema[routeStep+1] != humanScopeSchema || slices.Index(schema, convClearSchema) != routeStep+2 {
		t.Fatal("route, human scope and later schemas must append, never rewrite shipped steps")
	}
	old, e := sqlitedb.Open(path, schema[:routeStep])
	if e != nil {
		t.Fatal(e)
	}
	if _, e = old.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state) VALUES('legacy','bob/laptop',1,'message','legacy exact body',1,'')`); e != nil {
		t.Fatal(e)
	}
	old.Close()
	s, e := openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	var body string
	var route sql.NullString
	if e = s.db.QueryRow(`SELECT body,receiver_route FROM inbox WHERE id='legacy'`).Scan(&body, &route); e != nil || body != "legacy exact body" || route.Valid {
		t.Fatalf("migration changed legacy %q %v %v", body, route, e)
	}
	for _, op := range []string{"catalog", "delegate", "ready", "request"} {
		in := envelope.Inner{ID: protocol.NewID(), From: "bob/laptop", To: "alice/desk", TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "typed " + op}
		if e = s.addInbox(in, strings.Repeat("a", 64)); e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(envelope.ReceiverRoute{Op: op})
		if _, e = s.db.Exec(`UPDATE inbox SET receiver_route=? WHERE id=?`, string(raw), in.ID); e != nil {
			t.Fatal(e)
		}
	}
	items, e := s.arrivalsAfter(0, 20)
	if e != nil {
		t.Fatal(e)
	}
	if len(items) != 2 {
		t.Fatalf("generic attention leaked setup or hid legacy/original %d", len(items))
	}
	var reviews int
	s.db.QueryRow(`SELECT count(*) FROM inbox`).Scan(&reviews)
	if reviews != 5 {
		t.Fatal("quiet predicate removed human-visible data")
	}
	if _, e = s.db.Exec(`UPDATE inbox SET receiver_route='not JSON' WHERE id='legacy'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.arrivalsAfter(0, 20); e == nil {
		t.Fatal("malformed persisted route did not fail closed")
	}
}
