package hub

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/googleauth"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGoogleReviewPendingGrace(t *testing.T) {
	h, _, _ := testHub(t)
	claims := googleauth.Claims{Email: "person@example.com", Domain: "example.com", Subject: "fixture"}
	if _, err := h.store.changeGoogleAccess(protocol.GoogleAccessChange{Email: claims.Email}); err != nil {
		t.Fatal(err)
	}
	id, _ := identity.Generate()
	first := id.Public(protocol.GoogleLabel(claims.Email) + "/laptop")
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Person", Email: claims.Email, Devices: []identity.Public{first}}
	r.Sign(id.Sign)
	if _, _, err := h.store.enrollGoogle(claims, protocol.GoogleRequest{Public: first, First: &r}); err != nil {
		t.Fatal(err)
	}
	key, _ := identity.Generate()
	pub := key.Public(protocol.GoogleLabel(claims.Email) + "/phone")
	l := protocol.GoogleLink{Email: claims.Email, Person: r.Person, Seq: r.Seq, Roster: r.Hash(), Approver: protocol.LinkApprover{Address: first.Address, Fingerprint: first.Fingerprint()}, Offer: protocol.NewID(), Expires: time.Now().Unix() + 1, Join: ed25519.Sign(key.Sign, protocol.JoinBytes(r.Person, 1, r.Hash(), pub))}
	if _, _, err := h.store.enrollGoogle(claims, protocol.GoogleRequest{Public: pub, Link: &l}); err != nil {
		t.Fatal(err)
	}
	a, err := h.store.agent(pub.Address)
	if err != nil || a.PendingUntil != l.Expires+protocol.PendingGrace {
		t.Fatalf("pending grace %+v %v", a, err)
	}
	// Make the signed offer expire just before the actual stream request,
	// retaining only its publication grace in the pending row.
	l.Expires = time.Now().Unix() - 1
	ev, _ := json.Marshal(protocol.LinkEvent{Offer: l.Offer, Device: pub, Join: l.Join, Google: &l})
	if _, err = h.store.db.Exec(`UPDATE agents SET pending_until=?,pending_event=? WHERE address=?`, l.Expires+protocol.PendingGrace, string(ev), pub.Address); err != nil {
		t.Fatal(err)
	}
	request := signed(t, key, pub.Address, "GET", "/v1/stream", nil)
	request.Pattern = "GET /v1/stream"
	if _, ok := h.authenticate(httptest.NewRecorder(), request); !ok {
		t.Fatal("pending stream revoked during grace")
	}
	next := protocol.PersonRoster{Person: r.Person, Label: r.Label, Email: r.Email, Seq: 1, Prev: r.Hash(), Devices: []identity.Public{first, pub}, By: first.Fingerprint(), Join: l.Join}
	next.Sign(id.Sign)
	raw, _ := json.Marshal(next)
	res, err := h.store.putPersonStep(first, raw, next, time.Unix(l.Expires+2, 0))
	if err != nil || res.activated != pub.Address {
		t.Fatalf("late publication failed: %+v %v", res, err)
	}
}

func TestGoogleReviewDetachedLegacyDeviceOffboarded(t *testing.T) {
	h, _, _ := testHub(t)
	claims := googleauth.Claims{Email: "legacy@example.com", Domain: "example.com", Subject: "fixture"}
	if _, err := h.store.changeGoogleAccess(protocol.GoogleAccessChange{Email: claims.Email}); err != nil {
		t.Fatal(err)
	}
	id, _ := identity.Generate()
	pub := id.Public(protocol.GoogleLabel(claims.Email) + "/laptop")
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Legacy", Email: claims.Email, Devices: []identity.Public{pub}}
	r.Sign(id.Sign)
	if _, _, err := h.store.enrollGoogle(claims, protocol.GoogleRequest{Public: pub, First: &r}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`UPDATE agents SET person_id=NULL,linked=0 WHERE address=?`, pub.Address); err != nil {
		t.Fatal(err)
	}
	removed, err := h.store.changeGoogleAccess(protocol.GoogleAccessChange{Email: claims.Email, Remove: true})
	if err != nil || len(removed) != 1 || removed[0] != pub.Address {
		t.Fatalf("detached device not offboarded: %v %v", removed, err)
	}
	a, err := h.store.agent(pub.Address)
	if err != nil || !a.Revoked {
		t.Fatalf("legacy device active: %+v %v", a, err)
	}
	var n int
	if err = h.store.db.QueryRow(`SELECT count(*) FROM google_people WHERE email=?`, claims.Email).Scan(&n); err != nil || n != 0 {
		t.Fatalf("binding survived offboarding: %d %v", n, err)
	}
}
