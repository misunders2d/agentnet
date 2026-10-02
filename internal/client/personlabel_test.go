package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type personLabelTransport func(*http.Request) (*http.Response, error)

func (f personLabelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPersonLabelPublicationLinkedIdentityAndNoAuthorityEffects(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	phone := linked(t, w.alice)
	before, _, err := w.alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	role, _ := w.alice.Role()
	bodies := convBodies(t, w.alice, conv)
	counts := map[string]int{}
	for _, table := range []string{"inbox WHERE kind IN ('question', 'task')", "approvals", "task_grants", "participation_events"} {
		var n int
		if err := w.alice.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	bob, _, _ := w.bob.Person()
	renamed, err := w.alice.RenamePerson(tctx(t), bob.Label)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Person != before.Person || renamed.Label != bob.Label || renamed.Person == bob.Person || !reflect.DeepEqual(renamed.Devices, before.Devices) {
		t.Fatalf("identity changed: %+v %+v", before, renamed)
	}
	if after, _ := w.alice.Role(); after != role {
		t.Fatal("role changed")
	}
	if !reflect.DeepEqual(bodies, convBodies(t, w.alice, conv)) {
		t.Fatal("history changed")
	}
	for table, n := range counts {
		var got int
		if err := w.alice.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil || got != n {
			t.Fatalf("%s authority changed %d %d %v", table, n, got, err)
		}
	}
	eventually(t, "linked and peer display labels", func() bool {
		p, _, _ := phone.Person()
		b, ok, _ := w.bob.store.personByID(before.Person)
		return p.Person == before.Person && p.Label == bob.Label && ok && b.info.Label == bob.Label
	})
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	if _, err := me.roster.VerifyNext(func() protocol.PersonRoster {
		r, _, _ := w.alice.store.chainStep(before.Person, before.Roster)
		return r
	}()); err != nil {
		t.Fatal(err)
	}
}

func TestPersonLabelCASRebaseAndUnknownAcceptance(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	original := me.info
	base := w.alice.hub.http.Transport
	puts := 0
	w.alice.hub.http.Transport = personLabelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method == "PUT" && req.URL.Path == "/v1/person" {
			puts++
			if puts == 1 {
				// A legitimate independent device/head transition precedes this request.
				r := protocol.PersonRoster{Person: me.roster.Person, Label: "Concurrent", Seq: me.roster.Seq + 1, Prev: me.roster.Hash(), Devices: me.roster.Devices, By: w.alice.Self().Fingerprint()}
				r.Sign(w.alice.id.Sign)
				raw, _ := json.Marshal(r)
				otherReq, err := w.alice.hub.request(tctx(t), "PUT", "/v1/person", raw)
				if err != nil {
					return nil, err
				}
				resp, err := base.RoundTrip(otherReq)
				if err != nil {
					return nil, err
				}
				resp.Body.Close()
			}
		}
		return base.RoundTrip(req)
	})
	after, err := w.alice.RenamePerson(tctx(t), "Desired")
	if err != nil || puts != 2 || after.Person != original.Person || after.Seq != original.Seq+2 {
		t.Fatalf("CAS %+v %v puts%d", after, err, puts)
	}
	accepted := after
	w.alice.hub.http.Transport = personLabelTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method == "PUT" && req.URL.Path == "/v1/person" {
			resp, err := base.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			resp.Body.Close()
			return nil, errors.New("synthetic lost response")
		}
		return base.RoundTrip(req)
	})
	if _, err := w.alice.RenamePerson(tctx(t), "Remote accepted"); err == nil {
		t.Fatal("lost response claimed confirmed")
	}
	local, _, _ := w.alice.Person()
	if local.Label != accepted.Label || local.Roster != accepted.Roster {
		t.Fatal("unconfirmed optimistic label")
	}
	w.alice.hub.http.Transport = base
	if _, err := w.alice.refreshPerson(tctx(t), after.Person, false); err != nil {
		t.Fatal(err)
	}
	latest, _, _ := w.alice.Person()
	if latest.Label != "Remote accepted" {
		t.Fatal("signed recovery failed")
	}
	// A delayed accepted older response cannot roll back newer pinned state.
	old, _, _ := w.alice.store.chainStep(accepted.Person, accepted.Roster)
	raw, _ := json.Marshal(old)
	if _, err := w.alice.store.pinChain(accepted.Person, [][]byte{raw}, w.alice.Self(), false); err != nil {
		t.Fatal(err)
	}
	latest, _, _ = w.alice.Person()
	if latest.Label != "Remote accepted" {
		t.Fatal("stale response rolled back label")
	}
}

func TestPersonLabelForgedAndRemovedSignerRefused(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	r := protocol.PersonRoster{Person: me.info.Person, Label: "Forged", Seq: me.info.Seq + 1, Prev: me.info.Roster, Devices: me.roster.Devices, By: w.alice.Self().Fingerprint()}
	r.Sign(w.bob.id.Sign)
	raw, _ := json.Marshal(r)
	if err := w.alice.hub.doBytes(tctx(t), "PUT", "/v1/person", raw, nil); err == nil {
		t.Fatal("forged roster signature accepted")
	}
	if _, err := w.alice.RenamePerson(tctx(t), " bad "); err == nil {
		t.Fatal("invalid label accepted")
	}
	// Actual linked-device removal refuses current signer authority.
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	phone := linked(t, w.alice)
	if err := w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.RenamePerson(tctx(t), "Revoked"); err == nil {
		t.Fatal("removed signer renamed person")
	}
}
