package ui

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

var b64 = base64.StdEncoding.EncodeToString

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// goRoster is a person record made and signed as the Go client does.
func goRoster(id *identity.Identity, address, label string) protocol.PersonRoster {
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: label,
		Devices: []identity.Public{id.Public(address)}}
	r.Sign(id.Sign)
	return r
}

// The browser device's version 2 wire (person rosters, DM roots, capability
// records, profiles and conversation envelopes) agrees with the Go code in
// both directions, and refuses what the Go code refuses. Participation
// events are left out while the core's participation records are in review.
func TestBrowserWireV2MatchesGo(t *testing.T) {
	w := startWireNode(t)
	if m := w.ok(map[string]any{"op": "support"})["missing"].([]any); len(m) > 0 {
		t.Skipf("this node lacks %v", m)
	}
	const dana = "dana/phone"
	setup := w.ok(map[string]any{"op": "setup", "address": dana})
	var pub identity.Public
	if err := strictJSON([]byte(setup["public"].(string)), &pub); err != nil {
		t.Fatal(err)
	}
	danaRecipient, _ := pub.Recipient()
	bobID, _ := identity.Generate()
	bob := bobID.Public("bob/desk")
	eveID, _ := identity.Generate()

	// both checks that the Go code and the device both refuse raw, a record
	// that Go's parse (and, if the record parses, verify) refuses.
	both := func(t *testing.T, what, op, raw string, key ed25519.PublicKey, goErr error) {
		t.Helper()
		if goErr == nil {
			t.Fatalf("%s: Go accepts it", what)
		}
		if v := w.call(map[string]any{"op": op, "json": raw, "key": b64(key)}); v["error"] == nil {
			t.Errorf("%s: the device accepts what Go refuses (%v)", what, goErr)
		}
	}

	var danaRoster protocol.PersonRoster
	pubJSON := func(p identity.Public) string { return marshal(t, p) }
	t.Run("person rosters", func(t *testing.T) {
		v := w.ok(map[string]any{"op": "roster", "label": "Dana <&> 😀"})
		r, err := protocol.ParsePersonRoster([]byte(v["json"].(string)))
		if err != nil || r.VerifyFirst() != nil {
			t.Fatalf("Go refuses the device's person: %v", err)
		}
		if marshal(t, r) != v["json"] || r.Hash() != v["hash"] || r.Devices[0].Address != dana || r.Devices[0].Fingerprint() != pub.Fingerprint() {
			t.Fatalf("person record differs from Go's:\n%s\n%s (%s vs %s)", v["json"], marshal(t, r), v["hash"], r.Hash())
		}
		danaRoster = r
		br := goRoster(bobID, bob.Address, "Bob")
		if got := w.ok(map[string]any{"op": "parseRoster", "json": marshal(t, br)}); got["hash"] != br.Hash() {
			t.Fatalf("hash of Go's person: %v, want %s", got["hash"], br.Hash())
		}
		for what, change := range map[string]func(*protocol.PersonRoster){
			"empty label":       func(r *protocol.PersonRoster) { r.Label = "" },
			"leading space":     func(r *protocol.PersonRoster) { r.Label = " Bob" },
			"control character": func(r *protocol.PersonRoster) { r.Label = "Bo\u0001b" },
			"long label":        func(r *protocol.PersonRoster) { r.Label = strings.Repeat("é", 33) },
			"later roster":      func(r *protocol.PersonRoster) { r.Seq = 1 },
			"prev":              func(r *protocol.PersonRoster) { r.Prev = strings.Repeat("a", 64) },
			"a signer at seq 0": func(r *protocol.PersonRoster) { r.By = bob.Fingerprint() },
			"two devices":       func(r *protocol.PersonRoster) { r.Devices = append(r.Devices, eveID.Public("eve/lab")) },
			"a device twice":    func(r *protocol.PersonRoster) { r.Devices = append(r.Devices, r.Devices[0]) },
			"bad device key":    func(r *protocol.PersonRoster) { r.Devices[0].SignKey = r.Devices[0].SignKey[:5] },
			"unbound box key":   func(r *protocol.PersonRoster) { r.Devices[0].BoxSig = eveID.Public(bob.Address).BoxSig },
			"bad address":       func(r *protocol.PersonRoster) { r.Devices[0].Address = "Bob" },
			"bad id":            func(r *protocol.PersonRoster) { r.Person = "x" },
		} {
			r := goRoster(bobID, bob.Address, "Bob")
			change(&r)
			r.Sign(bobID.Sign)
			raw := marshal(t, r)
			goErr := func() error {
				p, err := protocol.ParsePersonRoster([]byte(raw))
				if err != nil {
					return err
				}
				return p.VerifyFirst()
			}()
			both(t, what, "parseRoster", raw, bob.SignKey, goErr)
		}
		r = goRoster(bobID, bob.Address, "Bob")
		r.Sig[0] ^= 1
		both(t, "flipped signature", "parseRoster", marshal(t, r), bob.SignKey, r.VerifyFirst())
		r = goRoster(bobID, bob.Address, "Bob")
		r.Sign(eveID.Sign) // signed by a key that is not its device's
		both(t, "other key", "parseRoster", marshal(t, r), bob.SignKey, r.VerifyFirst())
		raw := strings.TrimSuffix(marshal(t, goRoster(bobID, bob.Address, "Bob")), "}") + `,"admin":true}`
		_, goErr := protocol.ParsePersonRoster([]byte(raw))
		both(t, "unknown field", "parseRoster", raw, bob.SignKey, goErr)
		raw = marshal(t, goRoster(bobID, bob.Address, "Bob")) + strings.Repeat(" ", protocol.MaxPersonRecord)
		_, goErr = protocol.ParsePersonRoster([]byte(raw))
		both(t, "oversize", "parseRoster", raw, bob.SignKey, goErr)

		// Labels: the device judges them as Go does.
		labels := []string{"Vitalii", "Сергей", "李雷", "a b", "<script>&", "e\u0301", strings.Repeat("x", 64), strings.Repeat("x", 65),
			"tab\tx", "nbsp\u00a0x", "\u00a0lead", "trail ", "zwj👩\u200d💻", "zero\u200bwidth", "\ufeffbom", "line\u2028sep", "😀", ""}
		got := w.ok(map[string]any{"op": "validLabel", "labels": labels})["ok"].([]any)
		for i, l := range labels {
			r := goRoster(bobID, bob.Address, "x")
			r.Label = l
			_, err := protocol.ParsePersonRoster([]byte(marshal(t, r)))
			if (err == nil) != got[i].(bool) {
				t.Errorf("label %q: Go %v, device %v", l, err, got[i])
			}
		}
	})

	// Roster chains: each side follows the other's steps, adding a device
	// (with its consent) or removing one, and both refuse what breaks the
	// chain.
	t.Run("roster chains", func(t *testing.T) {
		// Go's person adds the device (its consent signed there).
		b0 := goRoster(bobID, bob.Address, "Bob")
		join, _ := base64.StdEncoding.DecodeString(w.ok(map[string]any{"op": "joinSign", "person": b0.Person, "seq": 1, "prev": b0.Hash()})["join"].(string))
		b1 := protocol.PersonRoster{Person: b0.Person, Label: b0.Label, Seq: 1, Prev: b0.Hash(), Devices: []identity.Public{b0.Devices[0], pub}, By: bob.Fingerprint(), Join: join}
		b1.Sign(bobID.Sign)
		if _, err := b1.VerifyNext(b0); err != nil {
			t.Fatalf("Go refuses the device's consent: %v", err)
		}
		got := w.ok(map[string]any{"op": "parseRoster", "json": marshal(t, b1), "prev": marshal(t, b0)})
		if got["hash"] != b1.Hash() || got["added"] != dana {
			t.Fatalf("the device reads Go's step as %v, want %s adding %s", got, b1.Hash(), dana)
		}
		// The device's person adds a Go device (its consent signed by Go), then removes it.
		eve := eveID.Public("eve/lab")
		eveJoin := ed25519.Sign(eveID.Sign, protocol.JoinBytes(danaRoster.Person, 1, danaRoster.Hash(), eve))
		v := w.ok(map[string]any{"op": "nextRoster", "prev": marshal(t, danaRoster), "devices": []string{pubJSON(pub), pubJSON(eve)}, "join": b64(eveJoin)})
		d1, err := protocol.ParsePersonRoster([]byte(v["json"].(string)))
		if err != nil || marshal(t, d1) != v["json"] || d1.Hash() != v["hash"] {
			t.Fatalf("Go reads the device's step differently: %v\n%s\n%s", err, v["json"], marshal(t, d1))
		}
		if added, err := d1.VerifyNext(danaRoster); err != nil || added == nil || added.Address != eve.Address {
			t.Fatalf("Go refuses the device's step adding eve: %v %v", added, err)
		}
		v = w.ok(map[string]any{"op": "nextRoster", "prev": marshal(t, d1), "devices": []string{pubJSON(pub)}})
		d2, err := protocol.ParsePersonRoster([]byte(v["json"].(string)))
		if err != nil || d2.Hash() != v["hash"] {
			t.Fatalf("the removal: %v", err)
		}
		if added, err := d2.VerifyNext(d1); err != nil || added != nil {
			t.Fatalf("Go refuses the device's removal: %v %v", added, err)
		}
		// Refused alike: a broken chain.
		step := func(change func(*protocol.PersonRoster)) (string, error) {
			r := protocol.PersonRoster{Person: b0.Person, Label: b0.Label, Seq: 1, Prev: b0.Hash(), Devices: []identity.Public{b0.Devices[0], pub}, By: bob.Fingerprint(), Join: join}
			change(&r)
			r.Sign(bobID.Sign)
			raw := marshal(t, r)
			p, err := protocol.ParsePersonRoster([]byte(raw))
			if err == nil {
				_, err = p.VerifyNext(b0)
			}
			return raw, err
		}
		for what, change := range map[string]func(*protocol.PersonRoster){
			"no consent":       func(r *protocol.PersonRoster) { r.Join = nil },
			"another consent":  func(r *protocol.PersonRoster) { r.Join = eveJoin },
			"two added":        func(r *protocol.PersonRoster) { r.Devices = append(r.Devices, eve) },
			"a signer outside": func(r *protocol.PersonRoster) { r.By = eve.Fingerprint() },
			"wrong prev":       func(r *protocol.PersonRoster) { r.Prev = strings.Repeat("b", 64) },
			"skipped seq":      func(r *protocol.PersonRoster) { r.Seq = 2 },
			"moved device":     func(r *protocol.PersonRoster) { d := bobID.Public("bob/other"); r.Devices[0] = d },
			"other person":     func(r *protocol.PersonRoster) { r.Person = protocol.NewID() },
		} {
			raw, goErr := step(change)
			if goErr == nil {
				t.Fatalf("%s: Go accepts it", what)
			}
			if v := w.call(map[string]any{"op": "parseRoster", "json": raw, "prev": marshal(t, b0)}); v["error"] == nil {
				t.Errorf("%s: the device accepts what Go refuses (%v)", what, goErr)
			}
		}
		// A join consent shown to Go byte for byte.
		if got := protocol.JoinBytes(b0.Person, 1, b0.Hash(), pub); !ed25519.Verify(pub.SignKey, got, join) {
			t.Fatal("the device's consent is not over Go's join bytes")
		}
	})

	// Device links: an offer read and written alike; the MAC and a join
	// request that carries the link agree with Go.
	t.Run("device links", func(t *testing.T) {
		code := protocol.Invite{Hub: "https://hub.example", Label: "dana", Secret: protocol.NewID()}.Encode()
		secret := make([]byte, protocol.LinkSecretSize)
		rand.Read(secret)
		o := protocol.LinkOffer{V: 2, Invite: code, Offer: protocol.NewID(), Expires: time.Now().Add(5 * time.Minute).Unix(), Person: danaRoster.Person,
			Seq: 0, Roster: danaRoster.Hash(), Approver: protocol.LinkApprover{Address: dana, Fingerprint: pub.Fingerprint()}, Secret: secret}
		got := w.ok(map[string]any{"op": "decodeOffer", "code": "https://hub.example/#" + o.Encode()})["offer"].(map[string]any)
		if got["offer"] != o.Offer || got["invite"] != o.Invite || got["roster"] != o.Roster || got["secret"] != b64(secret) ||
			got["approver"].(map[string]any)["fingerprint"] != o.Approver.Fingerprint {
			t.Fatalf("the device reads Go's offer as %v", got)
		}
		back := w.ok(map[string]any{"op": "encodeOffer", "offer": got})["code"].(string)
		if back != o.Encode() {
			t.Fatalf("the device writes the offer differently:\n%s\n%s", back, o.Encode())
		}
		if _, err := protocol.DecodeLinkOffer(back); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"agentnet-link-v2:%%%", "agentnet-invite-v1:x", o.Encode()[:40]} {
			if v := w.call(map[string]any{"op": "decodeOffer", "code": bad}); v["error"] == nil {
				t.Errorf("the device reads a damaged code %q", bad)
			}
		}
		join := ed25519.Sign(eveID.Sign, protocol.JoinBytes(o.Person, 1, o.Roster, eveID.Public("eve/lab")))
		eve := eveID.Public("eve/lab")
		want := protocol.LinkMAC(o, eve, join)
		m := w.ok(map[string]any{"op": "linkMAC", "code": o.Encode(), "device": pubJSON(eve), "join": b64(join), "check": b64(want)})
		if m["mac"] != b64(want) || m["checks"] != true {
			t.Fatalf("the MAC differs from Go's: %v, want %s", m, b64(want))
		}
		bad := append([]byte{}, want...)
		bad[0] ^= 1
		if m := w.ok(map[string]any{"op": "linkMAC", "code": o.Encode(), "device": pubJSON(eve), "join": b64(join), "check": b64(bad)}); m["checks"] != false {
			t.Fatal("the device accepts a changed MAC")
		}
		// The device joining with a link: Go reads its request, consent and MAC.
		dj, _ := base64.StdEncoding.DecodeString(w.ok(map[string]any{"op": "joinSign", "person": o.Person, "seq": o.Seq + 1, "prev": o.Roster})["join"].(string))
		dm, _ := base64.StdEncoding.DecodeString(w.ok(map[string]any{"op": "linkMAC", "code": o.Encode(), "join": b64(dj)})["mac"].(string))
		body := w.ok(map[string]any{"op": "joinLink", "secret": "invite-secret", "offer": o.Offer, "join": b64(dj), "mac": b64(dm)})["body"].(string)
		var jr protocol.JoinRequest
		if err := strictJSON([]byte(body), &jr); err != nil || jr.Link == nil {
			t.Fatalf("the join request: %v %s", err, body)
		}
		if err := protocol.VerifyJoin(jr); err != nil || marshal(t, jr) != body {
			t.Fatalf("Go refuses or rewrites the device's join: %v\n%s\n%s", err, body, marshal(t, jr))
		}
		if !ed25519.Verify(jr.Public.SignKey, protocol.JoinBytes(o.Person, o.Seq+1, o.Roster, jr.Public), jr.Link.Join) || !protocol.CheckLinkMAC(o, jr.Public, jr.Link.Join, jr.Link.MAC) {
			t.Fatal("Go refuses the device's consent or MAC")
		}
	})

	t.Run("history items", func(t *testing.T) {
		// What a Go device forwards, read and written back byte for byte.
		full := client.HistoryItem{V: 1, From: bob.Address, FromKey: bob.Fingerprint(), ID: protocol.NewID(), LID: protocol.NewID(), TS: 1759150000,
			Kind: "message", Body: "a line\nand \"quotes\" <b>", ReplyTo: protocol.NewID(), Status: "done", Sub: "event", Origin: "ui", Emotion: "smile",
			Target: &envelope.Target{Address: dana, Fingerprint: pub.Fingerprint()}, PID: protocol.NewID(),
			Attachments: []envelope.Attachment{{Name: "notes é.txt", Size: 12, SHA256: strings.Repeat("ab", 32)}}, At: 1759150000123}
		bare := client.HistoryItem{V: 1, From: dana, FromKey: pub.Fingerprint(), ID: protocol.NewID(), LID: protocol.NewID(), TS: 1759150001, Kind: "message", Body: ""}
		for _, it := range []client.HistoryItem{full, bare} {
			raw := marshal(t, it)
			if got := w.ok(map[string]any{"op": "history", "json": raw})["json"]; got != raw {
				t.Fatalf("history item:\n go %s\n js %v", raw, got)
			}
		}
		for what, raw := range map[string]string{
			"another version": strings.Replace(marshal(t, bare), `"v":1`, `"v":2`, 1),
			"an unknown field": strings.Replace(marshal(t, bare), `"v":1`, `"v":1,"extra":true`, 1),
			"a bad key":        strings.Replace(marshal(t, bare), pub.Fingerprint(), "nope", 1),
			"a bad address":    strings.Replace(marshal(t, bare), dana, "dana", 1),
		} {
			if v := w.call(map[string]any{"op": "history", "json": raw}); v["error"] == nil {
				t.Errorf("%s: accepted", what)
			}
		}
	})

	bobRoster := goRoster(bobID, bob.Address, "Bob")
	var goRoot protocol.ConvRoot
	t.Run("DM roots", func(t *testing.T) {
		me := map[string]any{"person": danaRoster.Person, "roster": danaRoster.Hash(), "address": dana, "fingerprint": pub.Fingerprint()}
		other := map[string]any{"person": bobRoster.Person, "roster": bobRoster.Hash()}
		ids := map[string]bool{}
		for range 2 {
			v := w.ok(map[string]any{"op": "root", "me": me, "other": other})
			c, err := protocol.ParseConvRoot([]byte(v["json"].(string)))
			if err != nil || c.Verify(pub.SignKey) != nil || c.ID() != v["id"] || marshal(t, c) != v["json"] {
				t.Fatalf("Go refuses or differs from the device's root: %v\n%s\n%s", err, v["json"], marshal(t, c))
			}
			ids[c.ID()] = true
		}
		if len(ids) != 2 {
			t.Fatal("two DMs with the same person share an id")
		}
		mk := func() protocol.ConvRoot {
			members := []protocol.ConvMember{{Person: danaRoster.Person, Roster: danaRoster.Hash()}, {Person: bobRoster.Person, Roster: bobRoster.Hash()}}
			if members[0].Person > members[1].Person {
				members[0], members[1] = members[1], members[0]
			}
			c := protocol.ConvRoot{V: protocol.ConvRootVersion, Kind: protocol.ConvKindDM, Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix(),
				Creator: protocol.ConvCreator{Person: bobRoster.Person, Roster: bobRoster.Hash(), Address: bob.Address, Fingerprint: bob.Fingerprint()}}
			c.Sign(bobID.Sign)
			return c
		}
		goRoot = mk()
		if got := w.ok(map[string]any{"op": "parseRoot", "json": marshal(t, goRoot), "key": b64(bob.SignKey)}); got["id"] != goRoot.ID() {
			t.Fatalf("id of Go's root: %v, want %s", got["id"], goRoot.ID())
		}
		for what, change := range map[string]func(*protocol.ConvRoot){
			"unsorted":        func(c *protocol.ConvRoot) { c.Members[0], c.Members[1] = c.Members[1], c.Members[0] },
			"same person":     func(c *protocol.ConvRoot) { c.Members[1] = c.Members[0] },
			"one member":      func(c *protocol.ConvRoot) { c.Members = c.Members[:1] },
			"creator roster":  func(c *protocol.ConvRoot) { c.Creator.Roster = strings.Repeat("a", 64) },
			"creator outside": func(c *protocol.ConvRoot) { c.Creator.Person = protocol.NewID() },
			"group":           func(c *protocol.ConvRoot) { c.Kind = "group" },
			"version 1":       func(c *protocol.ConvRoot) { c.V = 1 },
			"bad nonce":       func(c *protocol.ConvRoot) { c.Nonce = "x" },
			"no time":         func(c *protocol.ConvRoot) { c.Created = 0 },
		} {
			c := mk()
			change(&c)
			c.Sign(bobID.Sign)
			raw := marshal(t, c)
			_, goErr := protocol.ParseConvRoot([]byte(raw))
			both(t, what, "parseRoot", raw, bob.SignKey, goErr)
		}
		c := mk()
		c.Sig[0] ^= 1
		both(t, "flipped signature", "parseRoot", marshal(t, c), bob.SignKey, c.Verify(bob.SignKey))
		raw := strings.TrimSuffix(marshal(t, mk()), "}") + `,"epoch":1}`
		_, goErr := protocol.ParseConvRoot([]byte(raw))
		both(t, "unknown field", "parseRoot", raw, bob.SignKey, goErr)
		raw = marshal(t, mk()) + strings.Repeat(" ", protocol.MaxConvRoot)
		_, goErr = protocol.ParseConvRoot([]byte(raw))
		both(t, "oversize", "parseRoot", raw, bob.SignKey, goErr)
	})

	t.Run("capabilities and profiles", func(t *testing.T) {
		session := protocol.NewID()
		v := w.ok(map[string]any{"op": "caps", "session": session})
		c, err := protocol.ParseCapsRecord([]byte(v["json"].(string)))
		if err != nil || c.Verify(pub.SignKey) != nil || !c.Has(protocol.CapEnv2) || marshal(t, c) != v["json"] {
			t.Fatalf("Go refuses or differs from the device's caps: %v %s", err, v["json"])
		}
		mk := func(id *identity.Identity, address, session string, caps []string) protocol.CapsRecord {
			c := protocol.CapsRecord{Address: address, Session: session, Caps: caps, TS: time.Now().Unix()}
			c.Sign(id.Sign)
			return c
		}
		if w.call(map[string]any{"op": "parseCaps", "json": marshal(t, mk(bobID, bob.Address, session, []string{"env2"})), "key": b64(bob.SignKey)})["error"] != nil {
			t.Fatal("the device refuses Go's caps")
		}
		for what, c := range map[string]protocol.CapsRecord{
			"unsorted":  mk(bobID, bob.Address, session, []string{"env2", "a"}),
			"duplicate": mk(bobID, bob.Address, session, []string{"env2", "env2"}),
			"bad name":  mk(bobID, bob.Address, session, []string{"Env2"}),
			"too many":  mk(bobID, bob.Address, session, strings.Split("a b c d e f g h i j k l m n o p q", " ")),
			"no time": func() protocol.CapsRecord {
				c := mk(bobID, bob.Address, session, nil)
				c.TS = 0
				c.Sign(bobID.Sign)
				return c
			}(),
			"session": mk(bobID, bob.Address, "x", []string{"env2"}),
		} {
			raw := marshal(t, c)
			_, goErr := protocol.ParseCapsRecord([]byte(raw))
			both(t, what, "parseCaps", raw, bob.SignKey, goErr)
		}
		// Which profiles say bob reads conversations: the device decides as Go does.
		s1, s2 := protocol.NewID(), protocol.NewID()
		raw := func(c protocol.CapsRecord) json.RawMessage { return json.RawMessage(marshal(t, c)) }
		for what, p := range map[string]protocol.Profile{
			"no sessions":       {Caps: []json.RawMessage{raw(mk(bobID, bob.Address, s1, []string{"env2"}))}},
			"one live session":  {Sessions: []string{s1}, Live: true, Caps: []json.RawMessage{raw(mk(bobID, bob.Address, s1, []string{"env2"}))}},
			"last session":      {Sessions: []string{s1}, Caps: []json.RawMessage{raw(mk(bobID, bob.Address, s1, []string{"env2"}))}},
			"one of two":        {Sessions: []string{s1, s2}, Caps: []json.RawMessage{raw(mk(bobID, bob.Address, s1, []string{"env2"}))}},
			"both":              {Sessions: []string{s1, s2}, Caps: []json.RawMessage{raw(mk(bobID, bob.Address, s1, []string{"env2"})), raw(mk(bobID, bob.Address, s2, []string{"a", "env2"}))}},
			"older program":     {Sessions: []string{s1}, Caps: []json.RawMessage{raw(mk(bobID, bob.Address, s1, nil))}},
			"other address":     {Sessions: []string{s1}, Caps: []json.RawMessage{raw(mk(bobID, "eve/lab", s1, []string{"env2"}))}},
			"other key":         {Sessions: []string{s1}, Caps: []json.RawMessage{raw(mk(eveID, bob.Address, s1, []string{"env2"}))}},
			"record not listed": {Sessions: []string{s2}, Caps: []json.RawMessage{raw(mk(bobID, bob.Address, s1, []string{"env2"}))}},
		} {
			want := p.Supports(bob.Address, bob.SignKey, protocol.CapEnv2)
			got := w.ok(map[string]any{"op": "supports", "profile": marshal(t, p), "address": bob.Address, "key": b64(bob.SignKey), "name": protocol.CapEnv2})
			if got["supports"] != want {
				t.Errorf("%s: Go %v, device %v", what, want, got["supports"])
			}
		}
	})

	t.Run("conversation envelopes", func(t *testing.T) {
		root := marshal(t, goRoot)
		conv := goRoot.ID()
		now := time.Now().Unix()
		msg := func(extra map[string]any) map[string]any {
			m := map[string]any{"v": 2, "id": protocol.NewID(), "to": bob.Address, "ts": now, "kind": "message", "body": "in the DM <&> \u2028",
				"conv": conv, "lid": protocol.NewID(), "root": root, "origin": "ui"}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		// Device to Go.
		m := msg(nil)
		v := w.ok(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": m})
		var env envelope.Envelope
		if err := strictJSON([]byte(v["envelope"].(string)), &env); err != nil {
			t.Fatal(err)
		}
		if marshal(t, env) != v["envelope"] || env.V != envelope.Version2 {
			t.Fatalf("envelope is not in Go's form: %s", v["envelope"])
		}
		if err := env.VerifySig(pub.SignKey); err != nil {
			t.Fatalf("the Hub would refuse it: %v", err)
		}
		in, err := envelope.Open(env, bobID, bob.Address, pub)
		if err != nil {
			t.Fatalf("the Go client cannot open it: %v", err)
		}
		if in.V != 2 || in.Conv != conv || in.LID != m["lid"] || in.Origin != "ui" || in.Body != m["body"] {
			t.Fatalf("opened %+v", in)
		}
		if c, err := protocol.ParseConvRoot(in.Root); err != nil || c.ID() != conv {
			t.Fatalf("root carried: %v", err)
		}
		in2 := func(v map[string]any) envelope.Inner {
			var env envelope.Envelope
			strictJSON([]byte(v["envelope"].(string)), &env)
			in, err := envelope.Open(env, bobID, bob.Address, pub)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			return in
		}
		q := in2(w.ok(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": msg(map[string]any{"kind": "question",
			"target": map[string]any{"address": bob.Address, "fingerprint": bob.Fingerprint()}})}))
		if q.Target == nil || q.Target.Address != bob.Address {
			t.Fatalf("target: %+v", q.Target)
		}
		// fan: the rosters the sender sent copies to, and history copies.
		fan := []map[string]any{{"person": danaRoster.Person, "roster": danaRoster.Hash()}, {"person": bobRoster.Person, "roster": bobRoster.Hash()}}
		f := in2(w.ok(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": msg(map[string]any{"fan": fan, "sub": "history", "replica": true})}))
		if len(f.Fan) != 2 || f.Fan[0].Person != danaRoster.Person || f.Fan[1].Roster != bobRoster.Hash() || f.Sub != envelope.SubHistory || !f.Replica {
			t.Fatalf("fan and history: %+v %q %v", f.Fan, f.Sub, f.Replica)
		}
		gfan := envelope.Inner{V: 2, ID: protocol.NewID(), From: bob.Address, To: dana, TS: now, Kind: "message", Body: "fanned", Conv: conv, LID: protocol.NewID(),
			Root: json.RawMessage(root), Origin: "ui", Fan: []envelope.Fan{{Person: bobRoster.Person, Roster: bobRoster.Hash()}}}
		genv, err := envelope.Seal(gfan, bobID.Sign, danaRecipient)
		if err != nil {
			t.Fatal(err)
		}
		gi := w.ok(map[string]any{"op": "open", "envelope": marshal(t, genv), "from": publicJSON(t, bob)})["inner"].(map[string]any)
		if gf, _ := gi["fan"].([]any); len(gf) != 1 || gf[0].(map[string]any)["roster"] != bobRoster.Hash() {
			t.Fatalf("the device reads Go's fan as %v", gi["fan"])
		}
		// What Go's Seal refuses, the device refuses to seal.
		for what, extra := range map[string]map[string]any{
			"v1 with a conversation": {"v": 1},
			"no root":                {"root": ""},
			"unknown sub":            {"sub": "vote"},
			"bad origin":             {"origin": "agent:"},
			"agent without emotion":  {"origin": "agent:claude"},
			"bad emotion":            {"origin": "agent:claude", "emotion": "Happy"},
			"target on a message":    {"target": map[string]any{"address": bob.Address, "fingerprint": bob.Fingerprint()}},
			"bad conversation id":    {"conv": "x"},
			"three in the fan":       {"fan": []map[string]any{{"person": protocol.NewID(), "roster": goRoot.Creator.Roster}, {"person": protocol.NewID(), "roster": goRoot.Creator.Roster}, {"person": protocol.NewID(), "roster": goRoot.Creator.Roster}}},
			"one person twice":       {"fan": []map[string]any{{"person": danaRoster.Person, "roster": goRoot.Creator.Roster}, {"person": danaRoster.Person, "roster": goRoot.Creator.Roster}}},
			"bad fan roster":         {"fan": []map[string]any{{"person": danaRoster.Person, "roster": "x"}}},
		} {
			m := msg(extra)
			goIn := envelope.Inner{V: 2, ID: m["id"].(string), From: dana, To: bob.Address, TS: now, Kind: "message", Body: "x",
				Conv: m["conv"].(string), LID: m["lid"].(string), Root: json.RawMessage(m["root"].(string)), Sub: str(m["sub"]),
				Origin: str(m["origin"]), Emotion: str(m["emotion"])}
			if extra["v"] == 1 {
				goIn.V = 1
			}
			if tg, ok := extra["target"].(map[string]any); ok {
				goIn.Target = &envelope.Target{Address: tg["address"].(string), Fingerprint: tg["fingerprint"].(string)}
			}
			if fs, ok := extra["fan"].([]map[string]any); ok {
				for _, f := range fs {
					goIn.Fan = append(goIn.Fan, envelope.Fan{Person: f["person"].(string), Roster: f["roster"].(string)})
				}
			}
			_, goErr := envelope.Seal(goIn, bobID.Sign, danaRecipient)
			vv := w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": m})
			if goErr == nil || vv["error"] == nil {
				t.Errorf("%s: Go %v, device %v", what, goErr, vv["error"])
			}
		}

		// Attention (NOTIFY.md §2): this DM's channel for the recipient, in
		// its signed place, both ways; what SealAttention refuses, refused.
		ch := protocol.NotifyChannel(conv, bob.Fingerprint())
		av := w.ok(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": msg(map[string]any{"chan": ch})})
		var aenv envelope.Envelope
		if err := strictJSON([]byte(av["envelope"].(string)), &aenv); err != nil || marshal(t, aenv) != av["envelope"] {
			t.Fatalf("attention envelope not in Go's form (%v): %s", err, av["envelope"])
		}
		if !aenv.Attn || aenv.Chan != ch || aenv.VerifySig(pub.SignKey) != nil {
			t.Fatalf("attention envelope: attn %v chan %q verify %v", aenv.Attn, aenv.Chan, aenv.VerifySig(pub.SignKey))
		}
		w.refuses("attention on version 1", w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob),
			"message": map[string]any{"id": protocol.NewID(), "to": bob.Address, "ts": now, "kind": "message", "body": "x", "chan": ch}}), "attention")
		w.refuses("attention with a bad channel", w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob),
			"message": msg(map[string]any{"chan": "CRTkM8HtV3rVNaVsnOnalx"})}), "attention")
		gAttn, err := envelope.SealAttention(envelope.Inner{V: 2, ID: protocol.NewID(), From: bob.Address, To: dana, TS: now, Kind: "message",
			Body: "look", Conv: conv, LID: protocol.NewID(), Root: json.RawMessage(root), Origin: envelope.OriginUI}, bobID.Sign, danaRecipient,
			protocol.NotifyChannel(conv, pub.Fingerprint()))
		if err != nil {
			t.Fatal(err)
		}
		if got := w.ok(map[string]any{"op": "open", "envelope": marshal(t, gAttn), "from": publicJSON(t, bob)})["inner"].(map[string]any); got["body"] != "look" {
			t.Fatalf("Go's attention envelope opened as %v", got)
		}

		// Go to device.
		goIn := envelope.Inner{V: 2, ID: protocol.NewID(), From: bob.Address, To: dana, TS: now, Kind: "question", Body: "can you check?",
			Conv: conv, LID: protocol.NewID(), Root: json.RawMessage(root), Origin: envelope.OriginUI}
		env, err = envelope.Seal(goIn, bobID.Sign, danaRecipient)
		if err != nil {
			t.Fatal(err)
		}
		got := w.ok(map[string]any{"op": "open", "envelope": marshal(t, env), "from": publicJSON(t, bob)})["inner"].(map[string]any)
		if got["v"] != float64(2) || got["conv"] != conv || got["lid"] != goIn.LID || got["origin"] != "ui" || got["body"] != goIn.Body {
			t.Fatalf("opened %v", got)
		}
		if c, err := protocol.ParseConvRoot([]byte(got["root"].(string))); err != nil || c.ID() != conv {
			t.Fatalf("root the device read: %v %v", err, got["root"])
		}
		// Inner fields Go's Open refuses, each validly encrypted and signed.
		crafted := func(v int, change func(*envelope.Inner)) envelope.Envelope {
			in := envelope.Inner{V: v, ID: protocol.NewID(), From: bob.Address, To: dana, TS: now, Kind: "message", Body: "x",
				Conv: conv, LID: protocol.NewID(), Root: json.RawMessage(root)}
			change(&in)
			e := envelope.Envelope{V: v, ID: in.ID, From: in.From, To: in.To, TS: in.TS, Kind: in.Kind, CT: encryptTo(t, []byte(marshal(t, in)), danaRecipient)}
			signEnvelope(&e, bobID.Sign)
			return e
		}
		for what, e := range map[string]envelope.Envelope{
			"v1 with a conversation": crafted(1, func(*envelope.Inner) {}),
			"v2 without root":        crafted(2, func(in *envelope.Inner) { in.Root = nil }),
			"unknown sub":            crafted(2, func(in *envelope.Inner) { in.Sub = "vote" }),
			"target on a message": crafted(2, func(in *envelope.Inner) {
				in.Target = &envelope.Target{Address: bob.Address, Fingerprint: bob.Fingerprint()}
			}),
			"bad logical id": crafted(2, func(in *envelope.Inner) { in.LID = "x" }),
		} {
			w.refuses(what, w.call(map[string]any{"op": "open", "envelope": marshal(t, e), "from": publicJSON(t, bob)}), "")
		}
		// A version 2 envelope signed in the version 1 domain passes for neither.
		wrong := crafted(2, func(*envelope.Inner) {})
		signEnvelopeAs(&wrong, bobID.Sign, "agentnet-envelope-v1\n")
		if wrong.VerifySig(bob.SignKey) == nil {
			t.Fatal("Go accepts a v2 envelope signed as v1")
		}
		w.refuses("v1 domain", w.call(map[string]any{"op": "open", "envelope": marshal(t, wrong), "from": publicJSON(t, bob)}), "signature invalid")
		// A version 1 envelope from Go still opens.
		v1 := envelope.Inner{ID: protocol.NewID(), From: bob.Address, To: dana, TS: now, Kind: "message", Body: "device history"}
		env, _ = envelope.Seal(v1, bobID.Sign, danaRecipient)
		if got := w.ok(map[string]any{"op": "open", "envelope": marshal(t, env), "from": publicJSON(t, bob)})["inner"].(map[string]any); got["v"] != float64(1) {
			t.Fatalf("v1: %v", got)
		}
	})

	t.Run("participation events", func(t *testing.T) {
		conv := goRoot.ID()
		fp := bob.Fingerprint()
		author := protocol.EventAuthor{Person: bobRoster.Person, Roster: bobRoster.Hash(), Address: bob.Address, Fingerprint: fp}
		invite := func() protocol.ParticipationEvent {
			e := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, Author: author, TS: time.Now().Unix(),
				Host:     &protocol.ParticipationHost{Person: danaRoster.Person, Address: dana, Fingerprint: pub.Fingerprint()},
				Grant:    []protocol.GrantRef{{LID: protocol.NewID(), Fingerprint: fp}, {LID: protocol.NewID(), Fingerprint: pub.Fingerprint()}},
				Audience: protocol.AudienceConversation, TaskKeys: []string{fp}, Note: "check the deploy <&>\nthen report"}
			e.Sign(bobID.Sign)
			return e
		}
		// Go's events in the device.
		inv := invite()
		acc := protocol.ParticipationEvent{V: 1, Conv: conv, PID: inv.PID, Type: protocol.EventAccept, Prev: inv.Hash(), Author: author, TS: time.Now().Unix()}
		acc.Sign(bobID.Sign)
		for _, e := range []protocol.ParticipationEvent{inv, acc} {
			got := w.ok(map[string]any{"op": "parseEvent", "json": marshal(t, e), "key": b64(bob.SignKey)})
			if got["hash"] != e.Hash() {
				t.Fatalf("%s: hash %v, want %s", e.Type, got["hash"], e.Hash())
			}
		}
		// The device's own: an invite of the other member's agent, and a dismissal.
		me := map[string]any{"person": danaRoster.Person, "roster": danaRoster.Hash(), "address": dana, "fingerprint": pub.Fingerprint()}
		pid := protocol.NewID()
		for _, ev := range []map[string]any{
			{"conv": conv, "pid": pid, "type": "invite", "author": me, "ts": time.Now().Unix(),
				"host":  map[string]any{"person": bobRoster.Person, "address": bob.Address, "fingerprint": fp},
				"grant": []any{map[string]any{"lid": protocol.NewID(), "fingerprint": pub.Fingerprint()}}, "audience": "conversation",
				"task_keys": []any{pub.Fingerprint()}, "note": "please look <&>"},
			{"conv": conv, "pid": pid, "type": "dismiss", "prev": inv.Hash(), "author": me, "ts": time.Now().Unix()},
		} {
			v := w.ok(map[string]any{"op": "event", "event": ev})
			e, err := protocol.ParseParticipationEvent([]byte(v["json"].(string)))
			if err != nil || e.Verify(pub.SignKey) != nil || e.Hash() != v["hash"] || marshal(t, e) != v["json"] {
				t.Fatalf("Go refuses or differs from the device's %s: %v\n%s\n%s", ev["type"], err, v["json"], marshal(t, e))
			}
		}
		// Refused alike.
		many := func(n int, f func() string) []string {
			var out []string
			for range n {
				out = append(out, f())
			}
			return out
		}
		for what, change := range map[string]func(*protocol.ParticipationEvent){
			"invite with prev":    func(e *protocol.ParticipationEvent) { e.Prev = strings.Repeat("a", 64) },
			"invite without host": func(e *protocol.ParticipationEvent) { e.Host = nil },
			"other audience":      func(e *protocol.ParticipationEvent) { e.Audience = "owner" },
			"grant repeated":      func(e *protocol.ParticipationEvent) { e.Grant = append(e.Grant, e.Grant[0]) },
			"grant bad lid":       func(e *protocol.ParticipationEvent) { e.Grant[0].LID = "x" },
			"grant too long": func(e *protocol.ParticipationEvent) {
				e.Grant = nil
				for range protocol.MaxGrant + 1 {
					e.Grant = append(e.Grant, protocol.GrantRef{LID: protocol.NewID(), Fingerprint: fp})
				}
			},
			"task keys repeated": func(e *protocol.ParticipationEvent) { e.TaskKeys = []string{fp, fp} },
			"task key bad":       func(e *protocol.ParticipationEvent) { e.TaskKeys = []string{"x"} },
			"task keys too many": func(e *protocol.ParticipationEvent) {
				e.TaskKeys = many(protocol.MaxTaskKeys+1, func() string { return strings.Repeat("a", 8) + "-" + protocol.NewID()[:8] + "-00000000-00000000" })
			},
			"note control":     func(e *protocol.ParticipationEvent) { e.Note = "a\u0007b" },
			"note too long":    func(e *protocol.ParticipationEvent) { e.Note = strings.Repeat("n", protocol.MaxInviteNote+1) },
			"bad pid":          func(e *protocol.ParticipationEvent) { e.PID = "x" },
			"no time":          func(e *protocol.ParticipationEvent) { e.TS = 0 },
			"version":          func(e *protocol.ParticipationEvent) { e.V = 2 },
			"bad author":       func(e *protocol.ParticipationEvent) { e.Author.Roster = "x" },
			"unknown type":     func(e *protocol.ParticipationEvent) { e.Type = "promote" },
			"accept with host": func(e *protocol.ParticipationEvent) { e.Type, e.Prev = protocol.EventAccept, inv.Hash() },
		} {
			e := invite()
			change(&e)
			e.Sign(bobID.Sign)
			raw := marshal(t, e)
			_, goErr := protocol.ParseParticipationEvent([]byte(raw))
			both(t, what, "parseEvent", raw, bob.SignKey, goErr)
		}
		// An accept that carries an empty grant list: present, so refused.
		raw := strings.TrimSuffix(marshal(t, acc), "}") + `,"grant":[]}`
		_, goErr := protocol.ParseParticipationEvent([]byte(raw))
		both(t, "accept with empty grant", "parseEvent", raw, bob.SignKey, goErr)
		flipped := invite()
		flipped.Sig[0] ^= 1
		both(t, "flipped signature", "parseEvent", marshal(t, flipped), bob.SignKey, flipped.Verify(bob.SignKey))
		widened := invite()
		widened.Grant = append(widened.Grant, protocol.GrantRef{LID: protocol.NewID(), Fingerprint: fp}) // after signing
		both(t, "widened grant", "parseEvent", marshal(t, widened), bob.SignKey, widened.Verify(bob.SignKey))
		raw = strings.TrimSuffix(marshal(t, invite()), "}") + `,"run":true}`
		_, goErr = protocol.ParseParticipationEvent([]byte(raw))
		both(t, "unknown field", "parseEvent", raw, bob.SignKey, goErr)

		// Participation ids on messages: the same rules in the device.
		root := marshal(t, goRoot)
		now := time.Now().Unix()
		evMsg := map[string]any{"v": 2, "id": protocol.NewID(), "to": bob.Address, "ts": now, "kind": "message", "body": marshal(t, inv),
			"conv": conv, "lid": protocol.NewID(), "root": root, "sub": "event", "pid": inv.PID}
		v := w.ok(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": evMsg})
		var env envelope.Envelope
		strictJSON([]byte(v["envelope"].(string)), &env)
		if in, err := envelope.Open(env, bobID, bob.Address, pub); err != nil || in.PID != inv.PID || in.Sub != envelope.SubEvent {
			t.Fatalf("event message: %v %+v", err, in)
		}
		for what, extra := range map[string]map[string]any{
			"pid on a plain message":          {"sub": "", "pid": inv.PID},
			"pid on a request without target": {"sub": "", "kind": "question", "pid": inv.PID},
			"bad pid":                         {"pid": "x"},
		} {
			m := map[string]any{}
			for k, x := range evMsg {
				m[k] = x
			}
			for k, x := range extra {
				m[k] = x
			}
			goIn := envelope.Inner{V: 2, ID: protocol.NewID(), From: dana, To: bob.Address, TS: now, Kind: str(m["kind"]), Body: "x",
				Conv: conv, LID: protocol.NewID(), Root: json.RawMessage(root), Sub: str(m["sub"]), PID: str(m["pid"])}
			_, goErr := envelope.Seal(goIn, bobID.Sign, danaRecipient)
			vv := w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": m})
			if goErr == nil || vv["error"] == nil {
				t.Errorf("%s: Go %v, device %v", what, goErr, vv["error"])
			}
		}
		// An agent's answer with its pid, from Go, opens in the device.
		ans := envelope.Inner{V: 2, ID: protocol.NewID(), From: bob.Address, To: dana, TS: now, Kind: "answer", Body: "done",
			Conv: conv, LID: protocol.NewID(), Root: json.RawMessage(root), PID: inv.PID, Origin: "agent:claude", Emotion: "calm"}
		aenv, err := envelope.Seal(ans, bobID.Sign, danaRecipient)
		if err != nil {
			t.Fatal(err)
		}
		got := w.ok(map[string]any{"op": "open", "envelope": marshal(t, aenv), "from": publicJSON(t, bob)})["inner"].(map[string]any)
		if got["pid"] != inv.PID || got["origin"] != "agent:claude" || got["emotion"] != "calm" {
			t.Fatalf("agent answer: %v", got)
		}
	})
	_ = age.X25519Recipient{}
	_ = bytes.Equal
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// The notification channel and the attention hint match Go byte for byte
// (docs/revival/NOTIFY.md §2; the core's vectors): the channel and its
// validity, the envelope's attn and chan in their signed position, both
// ways, and the shapes every reader refuses.
func TestBrowserNotifyWire(t *testing.T) {
	w := startWireNode(t)
	if m := w.ok(map[string]any{"op": "support"})["missing"].([]any); len(m) > 0 {
		t.Skipf("this node lacks %v", m)
	}
	const vconv = "e0758d3e1872da6abc62304e16423c9ae8782d39be3517e4503df4b6ac88b75a"
	for fp, want := range map[string]string{"01234567-89abcdef-01234567-89abcdef": "CRTkM8HtV3rVNaVsnOnalw", "fedcba98-76543210-fedcba98-76543210": "bs5zEfoANj9lBOw3SnQxAQ"} {
		if got := w.ok(map[string]any{"op": "channel", "conv": vconv, "fp": fp})["chan"]; got != want || protocol.NotifyChannel(vconv, fp) != want {
			t.Fatalf("channel for %s: %v, want %s", fp, got, want)
		}
	}
	id, _ := identity.Generate()
	fp := id.Public("dana/phone").Fingerprint()
	for i := 0; i < 5; i++ {
		conv := protocol.NewID() + protocol.NewID()
		if got := w.ok(map[string]any{"op": "channel", "conv": conv, "fp": fp})["chan"]; got != protocol.NotifyChannel(conv, fp) {
			t.Fatalf("channel for %s: %v", conv, got)
		}
	}
	for _, bad := range [][2]string{{"short", fp}, {strings.Repeat("A", 64), fp}, {strings.Repeat("a", 64), "not-a-key"}} {
		w.refuses("channel "+bad[0], w.call(map[string]any{"op": "channel", "conv": bad[0], "fp": bad[1]}), "invalid")
	}
	shapes := []string{"CRTkM8HtV3rVNaVsnOnalw", "CRTkM8HtV3rVNaVsnOnalx", "CRTkM8HtV3rVNaVsnOnal", "CRTkM8HtV3rVNaVsnOnalw=", "CRTkM8HtV3rVNaVsnOna+w",
		"CRTkM8HtV3rVNaVsnOnalg", "CRTkM8HtV3rVNaVsnOnalB", "", "AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAB"}
	valid := w.ok(map[string]any{"op": "validChannel", "list": shapes})["valid"].([]any)
	for i, s := range shapes {
		if valid[i] != protocol.ValidNotifyChannel(s) {
			t.Errorf("channel %q: browser %v, Go %v", s, valid[i], protocol.ValidNotifyChannel(s))
		}
	}

	// The core's attention vector: its signature verifies here.
	zero := ed25519.NewKeyFromSeed(make([]byte, 32))
	sig, _ := hex.DecodeString("71e8952944d3efcadea155cf5a53e05fd50b7f19c31895fa2f3918f1fc6ada109b77c1ba0e3aa23ce34c70cba00359db052e4eab7324b6372315d75e009d3705")
	vector := `{"v":2,"id":"00112233445566778899aabbccddeeff","from":"alice/a","to":"bob/b","ts":1790000000,"kind":"message","ct":"Y2lwaGVydGV4dA==",` +
		`"attn":true,"chan":"CRTkM8HtV3rVNaVsnOnalw","sig":"` + base64.StdEncoding.EncodeToString(sig) + `"}`
	zeroKey := base64.StdEncoding.EncodeToString(zero.Public().(ed25519.PublicKey))
	w.ok(map[string]any{"op": "verifyEnvelope", "envelope": vector, "key": zeroKey})
	// Hand-made shapes (signed as the core signs): each refused here and there.
	for name, mut := range map[string]func(*envelope.Envelope){
		"attn without chan":  func(e *envelope.Envelope) { e.Chan = "" },
		"chan without attn":  func(e *envelope.Envelope) { e.Attn = false },
		"version 1":          func(e *envelope.Envelope) { e.V = 1 },
		"non-canonical chan": func(e *envelope.Envelope) { e.Chan = "CRTkM8HtV3rVNaVsnOnalx" },
	} {
		var e envelope.Envelope
		strictJSON([]byte(vector), &e)
		mut(&e)
		e.Sig = nil
		domain := "agentnet-envelope-v2\n"
		if e.V == 1 {
			domain = "agentnet-envelope-v1\n"
		}
		e.Sig = ed25519.Sign(zero, append([]byte(domain), marshalBytes(t, e)...))
		if e.VerifySig(zero.Public().(ed25519.PublicKey)) == nil {
			t.Fatalf("%s: Go accepts it", name)
		}
		w.refuses(name, w.call(map[string]any{"op": "verifyEnvelope", "envelope": marshal(t, e), "key": zeroKey}), "")
	}
}
