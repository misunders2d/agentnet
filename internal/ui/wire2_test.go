package ui

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
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
		Devices: []protocol.RosterDevice{{Address: address, Fingerprint: id.Public(address).Fingerprint()}}}
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
	t.Run("person rosters", func(t *testing.T) {
		v := w.ok(map[string]any{"op": "roster", "label": "Dana <&> 😀"})
		r, err := protocol.ParsePersonRoster([]byte(v["json"].(string)))
		if err != nil || r.Verify(pub.SignKey) != nil {
			t.Fatalf("Go refuses the device's person: %v", err)
		}
		if marshal(t, r) != v["json"] || r.Hash() != v["hash"] || r.Devices[0].Address != dana || r.Devices[0].Fingerprint != pub.Fingerprint() {
			t.Fatalf("person record differs from Go's:\n%s\n%s (%s vs %s)", v["json"], marshal(t, r), v["hash"], r.Hash())
		}
		danaRoster = r
		br := goRoster(bobID, bob.Address, "Bob")
		if got := w.ok(map[string]any{"op": "parseRoster", "json": marshal(t, br), "key": b64(bob.SignKey)}); got["hash"] != br.Hash() {
			t.Fatalf("hash of Go's person: %v, want %s", got["hash"], br.Hash())
		}
		for what, change := range map[string]func(*protocol.PersonRoster){
			"empty label":       func(r *protocol.PersonRoster) { r.Label = "" },
			"leading space":     func(r *protocol.PersonRoster) { r.Label = " Bob" },
			"control character": func(r *protocol.PersonRoster) { r.Label = "Bo\u0001b" },
			"long label":        func(r *protocol.PersonRoster) { r.Label = strings.Repeat("é", 33) },
			"later roster":      func(r *protocol.PersonRoster) { r.Seq = 1 },
			"prev":              func(r *protocol.PersonRoster) { r.Prev = strings.Repeat("a", 64) },
			"two devices":       func(r *protocol.PersonRoster) { r.Devices = append(r.Devices, r.Devices[0]) },
			"bad fingerprint":   func(r *protocol.PersonRoster) { r.Devices[0].Fingerprint = "x" },
			"bad address":       func(r *protocol.PersonRoster) { r.Devices[0].Address = "Bob" },
			"bad id":            func(r *protocol.PersonRoster) { r.Person = "x" },
		} {
			r := goRoster(bobID, bob.Address, "Bob")
			change(&r)
			r.Sign(bobID.Sign)
			raw := marshal(t, r)
			_, goErr := protocol.ParsePersonRoster([]byte(raw))
			both(t, what, "parseRoster", raw, bob.SignKey, goErr)
		}
		r = goRoster(bobID, bob.Address, "Bob")
		r.Sig[0] ^= 1
		both(t, "flipped signature", "parseRoster", marshal(t, r), bob.SignKey, r.Verify(bob.SignKey))
		r = goRoster(eveID, bob.Address, "Bob") // signed by another key
		both(t, "other key", "parseRoster", marshal(t, r), bob.SignKey, r.Verify(bob.SignKey))
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
			c := protocol.ConvRoot{V: 1, Kind: protocol.ConvKindDM, Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix(),
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
			"version":         func(c *protocol.ConvRoot) { c.V = 2 },
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
			_, goErr := envelope.Seal(goIn, bobID.Sign, danaRecipient)
			vv := w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": m})
			if goErr == nil || vv["error"] == nil {
				t.Errorf("%s: Go %v, device %v", what, goErr, vv["error"])
			}
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
	_ = age.X25519Recipient{}
	_ = bytes.Equal
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
