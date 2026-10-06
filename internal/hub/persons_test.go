package hub

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// personOf publishes m's first roster and returns it.
func personOf(t *testing.T, h *Hub, m member) protocol.PersonRoster {
	t.Helper()
	r := firstRoster(m, "Vitalii")
	data, _ := json.Marshal(r)
	if c, b := m.call(t, h, "PUT", "/v1/person", data); c != http.StatusNoContent {
		t.Fatalf("publish: %d %s", c, b)
	}
	return r
}

// deviceInvite asks for a device invite as m and returns its secret.
func deviceInvite(t *testing.T, h *Hub, m member, offer string) string {
	t.Helper()
	c, body := m.call(t, h, "POST", "/v1/person/device-invite", protocol.DeviceInviteRequest{Offer: offer, Expires: time.Now().Unix() + 300})
	if c != http.StatusCreated {
		t.Fatalf("device invite: %d %s", c, body)
	}
	var inv protocol.DeviceInvite
	json.Unmarshal(body, &inv)
	code, err := protocol.DecodeInvite(inv.Code)
	if err != nil {
		t.Fatal(err)
	}
	return code.Secret
}

// joinLinked joins id at address with a device invite, consenting to be
// the step after head of person.
func joinLinked(t *testing.T, h *Hub, secret, address, offer string, head protocol.PersonRoster, id *identity.Identity) (int, protocol.Error) {
	t.Helper()
	pub := id.Public(address)
	req := protocol.JoinRequest{Secret: secret, Public: pub,
		Link: &protocol.JoinLink{Offer: offer, Join: ed25519.Sign(id.Sign, protocol.JoinBytes(head.Person, head.Seq+1, head.Hash(), pub)), MAC: []byte("mac")}}
	protocol.SignJoin(&req, id.Sign)
	body, _ := json.Marshal(req)
	w := serve(h, httptest.NewRequest("POST", "/v1/join", bytes.NewReader(body)))
	var e protocol.Error
	json.Unmarshal(w.Body.Bytes(), &e)
	return w.Code, e
}

// step is the roster after prev with devices, signed by signer (and, for
// an added device, its consent).
func step(prev protocol.PersonRoster, signer member, joiner *member, devices ...identity.Public) protocol.PersonRoster {
	r := protocol.PersonRoster{Person: prev.Person, Label: prev.Label, Seq: prev.Seq + 1, Prev: prev.Hash(), Devices: devices,
		By: signer.id.Public(signer.addr).Fingerprint()}
	// As a normal link: the person's human devices stay human, a joiner is one.
	for _, fp := range prev.Humans() {
		if slices.ContainsFunc(devices, func(d identity.Public) bool { return d.Fingerprint() == fp }) {
			r.HumanKeys = append(r.HumanKeys, fp)
		}
	}
	if joiner != nil {
		r.HumanKeys = append(r.HumanKeys, joiner.id.Public(joiner.addr).Fingerprint())
		r.Join = ed25519.Sign(joiner.id.Sign, protocol.JoinBytes(r.Person, r.Seq, r.Prev, joiner.id.Public(joiner.addr)))
	}
	r.Sign(signer.id.Sign)
	return r
}

func put(t *testing.T, h *Hub, m member, r protocol.PersonRoster) (int, protocol.Error) {
	t.Helper()
	data, _ := json.Marshal(r)
	c, body := m.call(t, h, "PUT", "/v1/person", data)
	var e protocol.Error
	json.Unmarshal(body, &e)
	return c, e
}

func listed(t *testing.T, h *Hub, asker member, address string) *protocol.Member {
	t.Helper()
	for _, m := range listMembers(t, h, asker).Members {
		if m.Address == address {
			return &m
		}
	}
	return nil
}

// A person's device admits a new device of that person with a device invite:
// it waits PENDING (no member, only its stream), the inviter is told, and
// the step adding it activates it in the same transaction.
func TestDeviceLinkActivates(t *testing.T) {
	h, _, _ := testHub(t)
	bob, desk := joinMember(t, h, "bob"), joinMember(t, h, "vitalii")
	r0 := personOf(t, h, desk)
	if c, _ := bob.call(t, h, "POST", "/v1/person/device-invite", protocol.DeviceInviteRequest{Offer: protocol.NewID(), Expires: time.Now().Unix() + 60}); c != http.StatusConflict {
		t.Fatalf("a device without a person got a device invite: %d", c)
	}
	offer := protocol.NewID()
	secret := deviceInvite(t, h, desk, offer)
	phoneID, _ := identity.Generate()
	phone := member{phoneID, "vitalii/phone"}
	if c, e := joinAs(t, h, secret, phone.addr, phoneID); c != http.StatusForbidden {
		t.Fatalf("a device invite used without its link: %d %+v", c, e)
	}
	if c, e := joinLinked(t, h, secret, phone.addr, offer, r0, phoneID); c != http.StatusCreated {
		t.Fatalf("join: %d %+v", c, e)
	}
	// Pending: only its stream; not a member anywhere.
	for _, path := range []string{"/v1/agents", "/v1/release", "/v1/agents/bob/x/profile"} {
		if c, body := phone.call(t, h, "GET", path, nil); c != http.StatusForbidden || !bytes.Contains(body, []byte(protocol.CodeLinkPending)) {
			t.Errorf("pending GET %s: %d %s", path, c, body)
		}
	}
	if listed(t, h, bob, phone.addr) != nil {
		t.Fatal("a pending device is listed")
	}
	if c, _ := bob.call(t, h, "GET", "/v1/agents/vitalii/phone", nil); c != http.StatusNotFound {
		t.Fatalf("pending directory entry: %d", c)
	}
	links, err := h.store.pendingLinks(desk.addr)
	if err != nil || len(links) != 1 {
		t.Fatalf("link events %v %v", links, err)
	}
	var ev protocol.LinkEvent
	if json.Unmarshal(links[phone.addr], &ev); ev.Offer != offer || ev.Device.Fingerprint() != phoneID.Public(phone.addr).Fingerprint() {
		t.Fatalf("link event %+v", ev)
	}
	// Only the inviting device activates it, with the step its consent names.
	if c, e := put(t, h, desk, step(r0, desk, nil, desk.id.Public(desk.addr), phoneID.Public(phone.addr))); c != http.StatusBadRequest {
		t.Fatalf("an addition without consent: %d %+v", c, e)
	}
	r1 := step(r0, desk, &phone, desk.id.Public(desk.addr), phoneID.Public(phone.addr))
	if c, e := put(t, h, desk, r1); c != http.StatusNoContent {
		t.Fatalf("activation: %d %+v", c, e)
	}
	if c, e := put(t, h, desk, r1); c != http.StatusNoContent {
		t.Fatalf("the same step again: %d %+v", c, e)
	}
	m := listed(t, h, bob, phone.addr)
	if m == nil || m.Person == nil || *m.Person != (protocol.PersonRef{ID: r0.Person, Seq: 1, Hash: r1.Hash()}) {
		t.Fatalf("activated member %+v", m)
	}
	if c, _ := phone.call(t, h, "GET", "/v1/agents", nil); c != http.StatusOK {
		t.Fatalf("activated device: %d", c)
	}
	if page := chainOf(t, h, bob, r0.Person, 0); len(page.Records) != 1 {
		t.Fatalf("chain after 0: %+v", page)
	}
	// A competing step for seq 1 is stale, never a fork.
	other := step(r0, desk, nil, desk.id.Public(desk.addr))
	other.Label = "Other"
	other.Sign(desk.id.Sign)
	if c, e := put(t, h, desk, other); c != http.StatusConflict || e.Code != protocol.CodeRosterStale {
		t.Fatalf("competing step: %d %+v", c, e)
	}
	if links, _ := h.store.pendingLinks(desk.addr); len(links) != 0 {
		t.Fatalf("an active device is still pending: %v", links)
	}
}

// Refused, expired and removed linked devices end revoked, with the reason
// they hear; an admin-admitted device leaves its person but stays a member.
func TestDeviceLinkEnds(t *testing.T) {
	h, _, _ := testHub(t)
	bob, desk := joinMember(t, h, "bob"), joinMember(t, h, "vitalii")
	r0 := personOf(t, h, desk)
	pending := func(name string) member {
		offer := protocol.NewID()
		secret := deviceInvite(t, h, desk, offer)
		id, _ := identity.Generate()
		m := member{id, "vitalii/" + name}
		if c, e := joinLinked(t, h, secret, m.addr, offer, r0, id); c != http.StatusCreated {
			t.Fatalf("join %s: %d %+v", name, c, e)
		}
		return m
	}
	refused := pending("refused")
	if c, _ := bob.call(t, h, "POST", "/v1/person/device-refuse", protocol.DeviceRefusal{Address: refused.addr}); c != http.StatusNotFound {
		t.Fatalf("someone else refused it: %d", c)
	}
	if c, b := desk.call(t, h, "POST", "/v1/person/device-refuse", protocol.DeviceRefusal{Address: refused.addr}); c != http.StatusNoContent {
		t.Fatalf("refuse: %d %s", c, b)
	}
	if c, body := refused.call(t, h, "GET", "/v1/agents", nil); c != http.StatusForbidden || !bytes.Contains(body, []byte(protocol.CodeLinkRefused)) {
		t.Fatalf("refused device: %d %s", c, body)
	}
	if c, e := put(t, h, desk, step(r0, desk, &refused, desk.id.Public(desk.addr), refused.id.Public(refused.addr))); c != http.StatusConflict || e.Code != protocol.CodeLinkExpired {
		t.Fatalf("activating a refused device: %d %+v", c, e)
	}
	late := pending("late")
	h.store.db.Exec(`UPDATE agents SET pending_until = ? WHERE address = ?`, time.Now().Unix()-1, late.addr)
	if c, e := put(t, h, desk, step(r0, desk, &late, desk.id.Public(desk.addr), late.id.Public(late.addr))); c != http.StatusConflict || e.Code != protocol.CodeLinkExpired {
		t.Fatalf("activating an expired device: %d %+v", c, e)
	}
	if c, body := late.call(t, h, "GET", "/v1/agents", nil); c != http.StatusForbidden || !bytes.Contains(body, []byte(protocol.CodeLinkExpired)) {
		t.Fatalf("expired device: %d %s", c, body)
	}
	// A consent to an old step is stale at join.
	phone := pending("phone")
	r1 := step(r0, desk, &phone, desk.id.Public(desk.addr), phone.id.Public(phone.addr))
	if c, e := put(t, h, desk, r1); c != http.StatusNoContent {
		t.Fatalf("activation: %d %+v", c, e)
	}
	offer := protocol.NewID()
	secret := deviceInvite(t, h, desk, offer)
	stale, _ := identity.Generate()
	if c, e := joinLinked(t, h, secret, "vitalii/stale", offer, r0, stale); c != http.StatusConflict || e.Code != protocol.CodeRosterStale {
		t.Fatalf("consent to an old step: %d %+v", c, e)
	}
	// The phone removes the desk, which an admin admitted: the desk leaves
	// the person, stays a member, and can no longer sign its steps.
	r2 := step(r1, phone, nil, phone.id.Public(phone.addr))
	if c, e := put(t, h, phone, r2); c != http.StatusNoContent {
		t.Fatalf("remove the desk: %d %+v", c, e)
	}
	if m := listed(t, h, bob, desk.addr); m == nil || m.Person != nil {
		t.Fatalf("the removed desk: %+v", m)
	}
	if c, e := put(t, h, desk, step(r2, desk, nil, phone.id.Public(phone.addr))); c != http.StatusBadRequest {
		t.Fatalf("a removed device signed a step: %d %+v", c, e)
	}
	// Linked devices removed from their person are revoked.
	h2, _, _ := testHub(t)
	d2 := joinMember(t, h2, "vitalii")
	p0 := personOf(t, h2, d2)
	o := protocol.NewID()
	s2 := deviceInvite(t, h2, d2, o)
	ph2ID, _ := identity.Generate()
	ph2 := member{ph2ID, "vitalii/phone"}
	if c, e := joinLinked(t, h2, s2, ph2.addr, o, p0, ph2ID); c != http.StatusCreated {
		t.Fatalf("join: %d %+v", c, e)
	}
	p1 := step(p0, d2, &ph2, d2.id.Public(d2.addr), ph2.id.Public(ph2.addr))
	if c, e := put(t, h2, d2, p1); c != http.StatusNoContent {
		t.Fatalf("activation: %d %+v", c, e)
	}
	// The phone removes itself (the signer may be the removed device).
	if c, e := put(t, h2, ph2, step(p1, ph2, nil, d2.id.Public(d2.addr))); c != http.StatusNoContent {
		t.Fatalf("self-removal: %d %+v", c, e)
	}
	if c, body := ph2.call(t, h2, "GET", "/v1/agents", nil); c != http.StatusForbidden || !bytes.Contains(body, []byte(protocol.CodeRevoked)) {
		t.Fatalf("removed linked device: %d %s", c, body)
	}
}

// A person holds at most MaxPersonDevices, counting those waiting.
func TestDeviceLinkLimit(t *testing.T) {
	h, _, _ := testHub(t)
	desk := joinMember(t, h, "vitalii")
	r0 := personOf(t, h, desk)
	for i := 1; i < protocol.MaxPersonDevices; i++ {
		offer := protocol.NewID()
		secret := deviceInvite(t, h, desk, offer)
		id, _ := identity.Generate()
		if c, e := joinLinked(t, h, secret, "vitalii/d"+string(rune('a'+i)), offer, r0, id); c != http.StatusCreated {
			t.Fatalf("join %d: %d %+v", i, c, e)
		}
	}
	if c, _ := desk.call(t, h, "POST", "/v1/person/device-invite", protocol.DeviceInviteRequest{Offer: protocol.NewID(), Expires: time.Now().Unix() + 60}); c != http.StatusConflict {
		t.Fatalf("an invite past the limit: %d", c)
	}
}
