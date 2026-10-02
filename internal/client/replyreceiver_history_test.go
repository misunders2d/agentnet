package client

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestReceiverHistoryOfflineRetryAndRetraction(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	_, raw, _, err := w.alice.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	ref := ControlRef{Conv: conv, ID: protocol.NewID(), Fingerprint: w.alice.Self().Fingerprint()}
	route := &envelope.ReceiverRoute{Op: "request", Host: w.alice.Address, HostKey: ref.Fingerprint, RequestRef: ref.ID, RequestDigest: strings.Repeat("a", 64), DelegationID: protocol.NewID()}
	var copies []outCopy
	for _, spec := range []struct {
		kind    string
		routed  bool
		foreign bool
	}{{envelope.KindMessage, true, false}, {envelope.KindQuestion, true, false}, {envelope.KindTask, true, false}, {envelope.KindMessage, false, false}, {envelope.KindMessage, true, true}} {
		h := HistoryItem{V: 1, ID: protocol.NewID(), LID: ref.ID, From: w.alice.Address, FromKey: ref.Fingerprint, TS: time.Now().Unix(), Kind: spec.kind, Body: "offline retained original", At: time.Now().UnixMilli()}
		if spec.routed {
			h.ReceiverRoute = route
		}
		if spec.foreign {
			h.FromKey = w.bob.Self().Fingerprint()
		}
		c, e := w.alice.historyCopy(w.alice.Self(), conv, raw, h)
		if e != nil {
			t.Fatal(e)
		}
		copies = append(copies, c)
	}
	path := filepath.Join(t.TempDir(), "offline.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = insertCopies(tx, copies); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for i, c := range copies {
		var body, sealed string
		if err = s.db.QueryRow(`SELECT body,envelope FROM outbox WHERE id=?`, c.env.ID).Scan(&body, &sealed); err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(c.env)
		if sealed != string(want) {
			t.Fatal("restart changed sealed history bytes")
		}
		needs, e := receiverCopyNeedsCapability(s.db, c.env.ID, c.in.Sub, body)
		if e != nil || needs != (i != 3) || (i == 3 && body != "") || (i != 3 && body != c.in.Body) {
			t.Fatalf("copy%d capability%t body%q error%v", i, needs, body, e)
		}
	}
	// The same reference in another conversation cannot erase this copy.
	tx, _ = s.db.Begin()
	foreign := ref
	foreign.Conv = strings.Repeat("b", 64)
	if err = redactReceiverHistory(tx, foreign); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	var body string
	s.db.QueryRow(`SELECT body FROM outbox WHERE id=?`, copies[0].env.ID).Scan(&body)
	if body == "" {
		t.Fatal("foreign conversation redacted routed snapshot")
	}
	tx, _ = s.db.Begin()
	if err = redactReceiverHistory(tx, ref); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	for i, c := range copies {
		var state string
		s.db.QueryRow(`SELECT body,state FROM outbox WHERE id=?`, c.env.ID).Scan(&body, &state)
		if i == 0 {
			if body != "" || state != stateNotDelivered {
				t.Fatalf("ordinary history survived deletion %q %s", body, state)
			}
		} else if state != stateQueued || (i != 3 && body != c.in.Body) {
			t.Fatalf("Q/T or foreign history changed copy%d %q %s", i, body, state)
		}
	}
}

func TestReceiverHistoryRouteExactDirectionNoAuthority(t *testing.T) {
	w, phone, _, _ := remoteReceiverWorld(t)
	sent, _ := remotePrepared(t, w, phone, envelope.KindMessage)
	b := groupReceiverBound(t, phone, sent.ID)
	full, e := replyReceiverIn(phone.store.db, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	route, e := receiverStoredRoute(phone.store.db, "out", sent.ID)
	if e != nil || route == nil || *route != full.remote.Route {
		t.Fatalf("frozen route %+v %v", route, e)
	}
	// One physical ID can independently exist in each direction.
	if e = phone.store.addInbox(envelope.Inner{ID: sent.ID, From: w.bob.Address, To: phone.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "unrelated inbound same ID"}, w.bob.Self().Fingerprint()); e != nil {
		t.Fatal(e)
	}
	if wrong, e := receiverStoredRoute(phone.store.db, "in", sent.ID); e != nil || wrong != nil {
		t.Fatalf("cross-direction route inferred %+v %v", wrong, e)
	}
	conv := strings.Repeat("a", 64)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: sent.ID, Conv: conv, From: phone.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "claimed original history", Replica: true, ReceiverRoute: route}
	if _, e = w.alice.store.addHistoryInbox(in, time.Now().UnixMilli(), phone.Self().Fingerprint(), phone.Address, protocol.NewID(), false, nil); e != nil {
		t.Fatal(e)
	}
	got, e := receiverStoredRoute(w.alice.store.db, "in", in.ID)
	if e != nil || got == nil || *got != *route {
		t.Fatalf("history provenance lost %+v %v", got, e)
	}
	var bindings, inputs, jobs int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receivers`).Scan(&bindings)
	w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs`).Scan(&inputs)
	w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE state IN (?,?)`, stateRunning, stateAccepted).Scan(&jobs)
	if bindings != 0 || inputs != 0 || jobs != 0 {
		t.Fatalf("history installed authority bindings%d inputs%d jobs%d", bindings, inputs, jobs)
	}
	foreign := *route
	foreign.Host = w.bob.Address
	foreign.HostKey = w.bob.Self().Fingerprint()
	in.ID = protocol.NewID()
	in.ReceiverRoute = &foreign
	if _, e = w.alice.store.addHistoryInbox(in, time.Now().UnixMilli(), phone.Self().Fingerprint(), phone.Address, protocol.NewID(), false, nil); e == nil {
		t.Fatal("foreign destination accepted from history")
	}
	if _, ok, e := w.alice.store.claimJob("default"); e != nil || ok {
		t.Fatalf("history default takeover %t %v", ok, e)
	}
}
