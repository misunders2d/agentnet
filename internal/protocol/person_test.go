package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/misunders2d/agentnet/internal/identity"
)

// Vector values (from the fixed keys below), shared with the browser's
// implementation.
const (
	VecDeskFP     = "19c77bce-aca933c7-80e1c0e9-e46fc988"
	VecPhoneFP    = "2d080c16-243e9286-0f7db07c-1e86db0c"
	VecPersonHash = "f78b94d0e4bf9f75df8a53076cb188f257acf1b660df845f0da670fdeef9f755"
	VecPersonSig  = "67dda2fc66eb44d3891d4487f00516d4d58ddbdc473bf594efeda21d513d34d9237af981f113ac94140704b2fac4fea1b9229b0fce040a9f351f346f87bf3b0f"
	VecLinkedHash = "bcdc5ee16b5dde5a905327b7e7dc56f9c22fb84d2faa085db1bdcfdaa4b4405b"
	VecLinkedJoin = "37e425a50e7cefd84eccd63de2de63b33ae9f7d67f339cb1e41309c540bf71733d3a663240dfdea18c3f799540bb18542bc8de4340c3acea01ad4e3e0ee2100c"
	VecLinkedSig  = "514d89ad8eb9e3af831801f47c15aaea96f35330162fe296d6709b9c0e09643cb924360db056077f5f4129910b9dce97c73b592cdf8641cf6c52d75ecbafd403"
	VecRootID     = "2618a06e39982409d2effa23e5570480598e4727c1b6d81b36d43d420b96a3b9"
	VecLinkMAC    = "d91ed8381d17571d5bb6100a1e52186d21e1986c601453558550b567ed796b7f"
)

const (
	vecPerson  = "0123456789abcdef0123456789abcdef"
	vecOther   = "fedcba9876543210fedcba9876543210"
	vecFP      = "01234567-89abcdef-01234567-89abcdef"
	vecSession = "00112233445566778899aabbccddeeff"
	// Test-only age identities for the vector devices (not secrets).
	vecBoxDesk  = "AGE-SECRET-KEY-1UDEAJGR0KMACNTZ9YKJ9UHGFW6M42W574X7VX8YUE57M043804KSTWTDSN"
	vecBoxPhone = "AGE-SECRET-KEY-1XHK7K767L0STF73QH80CZ4V4KR65E4Q8EY0TY88H2RT0JT6DJLNQANC3M8"
)

func vecKey() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(make([]byte, 32)) }

// vecDevice is a fixed device: an Ed25519 key from a repeated seed byte and
// a fixed age identity.
func vecDevice(seed byte, box, address string) (*identity.Identity, identity.Public) {
	b, err := age.ParseX25519Identity(box)
	if err != nil {
		panic(err)
	}
	id := &identity.Identity{Sign: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32)), Box: b}
	return id, id.Public(address)
}

func vecDesk() (*identity.Identity, identity.Public) {
	return vecDevice(0, vecBoxDesk, "vitalii/desk")
}

func vecPhone() (*identity.Identity, identity.Public) {
	return vecDevice(1, vecBoxPhone, "vitalii/phone")
}

// vecRoster is the first roster: the desk alone.
func vecRoster() PersonRoster {
	id, pub := vecDesk()
	r := PersonRoster{Person: vecPerson, Label: "Vitalii <&> Ю", Devices: []identity.Public{pub}}
	r.Sign(id.Sign)
	return r
}

// vecLinked is the second: the desk adds the phone, which consents.
func vecLinked() PersonRoster {
	r0 := vecRoster()
	desk, deskPub := vecDesk()
	phone, phonePub := vecPhone()
	r := PersonRoster{Person: vecPerson, Label: r0.Label, Seq: 1, Prev: r0.Hash(), Devices: []identity.Public{deskPub, phonePub}, By: deskPub.Fingerprint()}
	r.Join = ed25519.Sign(phone.Sign, JoinBytes(r.Person, 1, r.Prev, phonePub))
	r.Sign(desk.Sign)
	return r
}

func vecRoot(r PersonRoster) ConvRoot {
	desk, pub := vecDesk()
	c := ConvRoot{V: ConvRootVersion, Kind: ConvKindDM,
		Creator: ConvCreator{Person: vecPerson, Roster: r.Hash(), Address: "vitalii/desk", Fingerprint: pub.Fingerprint()},
		Members: []ConvMember{{Person: vecPerson, Roster: r.Hash()}, {Person: vecOther, Roster: "00" + r.Hash()[2:]}},
		Nonce:   vecSession, Created: 1790000000}
	c.Sign(desk.Sign)
	return c
}

// Exact canonical bytes, hashes and signatures (fixed keys above): the wire
// contract another implementation must reproduce.
func TestSignedRecordVectors(t *testing.T) {
	r, r1 := vecRoster(), vecLinked()
	_, deskPub := vecDesk()
	_, phonePub := vecPhone()
	check := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", what, got, want)
		}
	}
	deskJSON, _ := json.Marshal(deskPub)
	check("person bytes", string(r.Canonical()), "agentnet-person-v2\n"+
		`{"person":"0123456789abcdef0123456789abcdef","label":"Vitalii \u003c\u0026\u003e Ю","seq":0,"prev":"",`+
		`"devices":[`+string(deskJSON)+`]}`)
	check("desk fingerprint", deskPub.Fingerprint(), VecDeskFP)
	check("phone fingerprint", phonePub.Fingerprint(), VecPhoneFP)
	check("person hash", r.Hash(), VecPersonHash)
	check("person sig", hex.EncodeToString(r.Sig), VecPersonSig)
	check("linked hash", r1.Hash(), VecLinkedHash)
	check("join bytes", string(JoinBytes(vecPerson, 1, r.Hash(), phonePub)), "agentnet-person-join-v2\n"+
		`{"person":"0123456789abcdef0123456789abcdef","seq":1,"prev":"`+r.Hash()+`","device":`+func() string { b, _ := json.Marshal(phonePub); return string(b) }()+`}`)
	check("linked join", hex.EncodeToString(r1.Join), VecLinkedJoin)
	check("linked sig", hex.EncodeToString(r1.Sig), VecLinkedSig)

	c := vecRoot(r)
	check("root bytes", string(c.Canonical()), "agentnet-conv-root-v2\n"+
		`{"v":2,"kind":"dm","creator":{"person":"0123456789abcdef0123456789abcdef","roster":"`+r.Hash()+`",`+
		`"address":"vitalii/desk","fingerprint":"`+deskPub.Fingerprint()+`"},"members":[{"person":"0123456789abcdef0123456789abcdef",`+
		`"roster":"`+r.Hash()+`"},{"person":"fedcba9876543210fedcba9876543210",`+
		`"roster":"00`+r.Hash()[2:]+`"}],"nonce":"00112233445566778899aabbccddeeff","created":1790000000}`)
	check("conversation id", c.ID(), VecRootID)

	k := CapsRecord{Address: "vitalii/desk", Session: vecSession, Caps: []string{CapEnv2}, TS: 1790000000}
	k.Sign(vecKey())
	check("caps bytes", string(k.Canonical()), "agentnet-caps-v1\n"+
		`{"address":"vitalii/desk","session":"00112233445566778899aabbccddeeff","caps":["env2"],"ts":1790000000}`)
	check("caps sig", hex.EncodeToString(k.Sig), "b671087c9e1f96822b472e9cd439150c75603ee4ae9d804c68cf06ed60ef6ef6b56b0df4fad69c5141152c0db263c01b6dc4d61160ca4ce0811bbea4143ad300")

	if r.VerifyFirst() != nil || c.Verify(deskPub.SignKey) != nil || k.Verify(vecKey().Public().(ed25519.PublicKey)) != nil {
		t.Fatal("vectors do not verify")
	}
	if added, err := r1.VerifyNext(r); err != nil || added == nil || added.Fingerprint() != phonePub.Fingerprint() {
		t.Fatalf("linked step: %v %v", added, err)
	}
	// The signed forms round-trip through the strict parsers.
	for what, v := range map[string]any{"person": r, "linked": r1, "root": c, "caps": k} {
		data, _ := json.Marshal(v)
		var err error
		switch what {
		case "person", "linked":
			_, err = ParsePersonRoster(data)
		case "root":
			_, err = ParseConvRoot(data)
		case "caps":
			_, err = ParseCapsRecord(data)
		}
		if err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
}

// Anything but the signed bytes, a broken chain, or a record out of bounds,
// is refused.
func TestSignedRecordsRefuse(t *testing.T) {
	other := ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32)))
	r, r1 := vecRoster(), vecLinked()
	desk, deskPub := vecDesk()
	phone, phonePub := vecPhone()
	bad := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s accepted", what)
		}
	}
	forged := r
	forged.Sign(other)
	bad("another key", forged.VerifyFirst())
	changed := r
	changed.Label = "Mallory"
	bad("a changed label", changed.VerifyFirst())
	for what, mut := range map[string]func(*PersonRoster){
		"seq 0 with prev":   func(r *PersonRoster) { r.Prev = strings.Repeat("a", 64) },
		"seq 0 with by":     func(r *PersonRoster) { r.By = deskPub.Fingerprint() },
		"two first devices": func(r *PersonRoster) { r.Devices = append(r.Devices, phonePub) },
		"no device":         func(r *PersonRoster) { r.Devices = nil },
		"empty label":       func(r *PersonRoster) { r.Label = "" },
		"padded label":      func(r *PersonRoster) { r.Label = " x" },
		"control char":      func(r *PersonRoster) { r.Label = "a\x07b" },
		"long label":        func(r *PersonRoster) { r.Label = strings.Repeat("a", MaxPersonLabel+1) },
		"bad id":            func(r *PersonRoster) { r.Person = "x" },
		"bad address":       func(r *PersonRoster) { r.Devices[0].Address = "Vitalii" },
		"broken binding":    func(r *PersonRoster) { r.Devices[0].BoxSig = []byte("x") },
	} {
		m := vecRoster()
		m.Devices = append([]identity.Public(nil), m.Devices...)
		mut(&m)
		bad(what, m.Validate())
	}
	next := func(mut func(*PersonRoster), signer ed25519.PrivateKey) error {
		m := vecLinked()
		m.Devices = append([]identity.Public(nil), m.Devices...)
		mut(&m)
		m.Join = ed25519.Sign(phone.Sign, JoinBytes(m.Person, m.Seq, m.Prev, phonePub))
		m.Sign(signer)
		_, err := m.VerifyNext(r)
		return err
	}
	bad("signed by the new device", next(func(m *PersonRoster) { m.By = phonePub.Fingerprint() }, phone.Sign))
	bad("signed by another key", next(func(*PersonRoster) {}, other))
	bad("wrong prev", next(func(m *PersonRoster) { m.Prev = strings.Repeat("b", 64) }, desk.Sign))
	bad("skipped seq", next(func(m *PersonRoster) { m.Seq = 2 }, desk.Sign))
	bad("another person", next(func(m *PersonRoster) { m.Person = vecOther }, desk.Sign))
	_, third := vecDevice(2, vecBoxPhone, "vitalii/tablet")
	bad("two added", next(func(m *PersonRoster) { m.Devices = append(m.Devices, third) }, desk.Sign))
	moved := phonePub
	moved.Address = "vitalii/moved"
	bad("a kept key with another address", func() error {
		m := vecLinked()
		m.Devices = []identity.Public{deskPub, phonePub}
		m.Sign(desk.Sign)
		r2 := PersonRoster{Person: vecPerson, Label: m.Label, Seq: 2, Prev: m.Hash(), Devices: []identity.Public{deskPub, moved}, By: deskPub.Fingerprint()}
		r2.Sign(desk.Sign)
		_, err := r2.VerifyNext(m)
		return err
	}())
	noJoin := r1
	noJoin.Join = nil
	bad("an addition without consent", func() error { _, err := noJoin.VerifyNext(r); return err }())
	otherJoin := r1
	otherJoin.Join = ed25519.Sign(phone.Sign, JoinBytes(vecPerson, 1, strings.Repeat("c", 64), phonePub))
	bad("consent to another step", func() error { _, err := otherJoin.VerifyNext(r); return err }())
	// Removing a device needs no consent; the removed device may sign it.
	gone := PersonRoster{Person: vecPerson, Label: r1.Label, Seq: 2, Prev: r1.Hash(), Devices: []identity.Public{deskPub}, By: phonePub.Fingerprint()}
	gone.Sign(phone.Sign)
	if added, err := gone.VerifyNext(r1); err != nil || added != nil {
		t.Fatalf("self-removal: %v %v", added, err)
	}
	if _, err := ParsePersonRoster([]byte(`{"person":"` + vecPerson + `","label":"x","seq":0,"prev":"","devices":[],"extra":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := ParsePersonRoster(make([]byte, MaxPersonRecord+1)); err == nil {
		t.Error("oversized record accepted")
	}
	full := vecRoster()
	for i := 1; len(full.Devices) <= MaxPersonDevices; i++ {
		_, d := vecDevice(byte(i+10), vecBoxPhone, fmt.Sprintf("vitalii/d%d", i))
		full.Devices = append(full.Devices, d)
	}
	full.Seq, full.Prev, full.By = 1, r.Hash(), deskPub.Fingerprint()
	bad("nine devices", full.Validate())
	full.Devices = full.Devices[:MaxPersonDevices]
	full.Label = strings.Repeat("W", MaxPersonLabel)
	full.Sign(desk.Sign)
	full.Join = make([]byte, 64)
	if data, _ := json.Marshal(full); len(data) > MaxPersonRecord {
		t.Errorf("a full roster is %d bytes, over %d", len(data), MaxPersonRecord)
	}

	c := vecRoot(r)
	bad("root by another key", c.Verify(other.Public().(ed25519.PublicKey)))
	for what, mut := range map[string]func(*ConvRoot){
		"three members": func(c *ConvRoot) {
			c.Members = append(c.Members, ConvMember{Person: strings.Repeat("c", 32), Roster: r.Hash()})
		},
		"unsorted":          func(c *ConvRoot) { c.Members[0], c.Members[1] = c.Members[1], c.Members[0] },
		"same person":       func(c *ConvRoot) { c.Members[1] = c.Members[0] },
		"creator no member": func(c *ConvRoot) { c.Creator.Person = strings.Repeat("d", 32) },
		"creator roster":    func(c *ConvRoot) { c.Creator.Roster = strings.Repeat("e", 64) },
		"kind":              func(c *ConvRoot) { c.Kind = "group" },
		"version 1":         func(c *ConvRoot) { c.V = 1 },
		"nonce":             func(c *ConvRoot) { c.Nonce = "" },
	} {
		m := vecRoot(r)
		m.Members = append([]ConvMember(nil), m.Members...)
		mut(&m)
		bad(what, m.Validate())
	}
	k := CapsRecord{Address: "vitalii/desk", Session: vecSession, Caps: []string{"b", "a"}, TS: 1}
	bad("unsorted caps", k.Validate())
	k.Caps = []string{"env2", "env2"}
	bad("repeated caps", k.Validate())
	k.Caps, k.Session = []string{"env2"}, "x"
	bad("bad session", k.Validate())
}

// A device supports a capability only if every session the relay lists has
// a verifying record naming it: an old program's live session makes the
// device the least capable; an offline device keeps its last session's.
func TestProfileSupports(t *testing.T) {
	key := vecKey()
	pub := key.Public().(ed25519.PublicKey)
	rec := func(session string, caps ...string) json.RawMessage {
		k := CapsRecord{Address: "vitalii/desk", Session: session, Caps: caps, TS: 5}
		k.Sign(key)
		data, _ := json.Marshal(k)
		return data
	}
	s1, s2 := strings.Repeat("1", 32), strings.Repeat("2", 32)
	forged := CapsRecord{Address: "vitalii/desk", Session: s2, Caps: []string{CapEnv2}, TS: 5}
	forged.Sign(ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32))))
	forgedData, _ := json.Marshal(forged)
	for _, c := range []struct {
		name string
		p    Profile
		want bool
	}{
		{"one new session", Profile{Sessions: []string{s1}, Live: true, Caps: []json.RawMessage{rec(s1, CapEnv2)}}, true},
		{"old and new sessions", Profile{Sessions: []string{s1, s2}, Live: true, Caps: []json.RawMessage{rec(s1, CapEnv2)}}, false},
		{"both new", Profile{Sessions: []string{s1, s2}, Live: true, Caps: []json.RawMessage{rec(s1, CapEnv2), rec(s2, CapEnv2)}}, true},
		{"offline, last session new", Profile{Sessions: []string{s1}, Caps: []json.RawMessage{rec(s1, CapEnv2)}}, true},
		{"no sessions", Profile{}, false},
		{"a record for another session", Profile{Sessions: []string{s2}, Caps: []json.RawMessage{rec(s1, CapEnv2)}}, false},
		{"without the capability", Profile{Sessions: []string{s1}, Caps: []json.RawMessage{rec(s1)}}, false},
		{"forged record", Profile{Sessions: []string{s2}, Caps: []json.RawMessage{forgedData}}, false},
	} {
		if got := c.p.Supports("vitalii/desk", pub, CapEnv2); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if (Profile{Sessions: []string{s1}, Caps: []json.RawMessage{rec(s1, CapEnv2)}}).Supports("other/desk", pub, CapEnv2) {
		t.Error("a record naming another device counted")
	}
}

// ROOM_V1 §2.1: rm1 implies the capabilities it lists and nothing else, in
// every session as Supports already requires; a record parses with up to
// MaxCaps names (headroom over the MaxAdvertisedCaps a device lists), so a
// 17-name record from a later program parses, and a 33-name one does not.
func TestCapsRoomImplicationAndHeadroom(t *testing.T) {
	key := vecKey()
	pub := key.Public().(ed25519.PublicKey)
	rec := func(session string, caps ...string) json.RawMessage {
		k := CapsRecord{Address: "vitalii/desk", Session: session, Caps: caps, TS: 5}
		k.Sign(key)
		data, _ := json.Marshal(k)
		return data
	}
	s1, s2 := strings.Repeat("1", 32), strings.Repeat("2", 32)
	room := Profile{Sessions: []string{s1}, Caps: []json.RawMessage{rec(s1, CapEnv2, CapRoom)}}
	for _, name := range []string{CapExternalParticipation, CapAgentIdentity, CapHumanParticipation, CapAgentReaction, CapProgress, CapGroup, CapReplyReceiver, CapConvClear, CapRoom, CapEnv2} {
		if !room.Supports("vitalii/desk", pub, name) {
			t.Errorf("rm1 does not imply %s", name)
		}
	}
	for _, name := range []string{CapPerson, CapControl, CapHeadless, CapDriveSpace, CapNotify, CapTyping} {
		if room.Supports("vitalii/desk", pub, name) {
			t.Errorf("rm1 implies %s, which it does not list", name)
		}
	}
	if (Profile{Sessions: []string{s1, s2}, Caps: []json.RawMessage{rec(s1, CapRoom), rec(s2, CapEnv2)}}).Supports("vitalii/desk", pub, CapHumanParticipation) {
		t.Error("one session's rm1 spoke for another session")
	}
	if (Profile{Sessions: []string{s1}, Caps: []json.RawMessage{rec(s1, CapHumanParticipation)}}).Supports("vitalii/desk", pub, CapRoom) {
		t.Error("an implied capability implied rm1")
	}
	names := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("c%02d", i))
		}
		return out
	}
	for _, c := range []struct {
		n  int
		ok bool
	}{{17, true}, {32, true}, {33, false}} {
		n, ok := c.n, c.ok
		k := CapsRecord{Address: "vitalii/desk", Session: s1, Caps: names(n), TS: 5}
		k.Sign(key)
		data, _ := json.Marshal(k)
		if _, err := ParseCapsRecord(data); (err == nil) != ok {
			t.Errorf("a %d-name record: parse error %v, want parsed %v", n, err, ok)
		}
	}
	if MaxCaps != 2*MaxAdvertisedCaps || MaxAdvertisedCaps != 16 {
		t.Fatalf("parse %d, advertise %d", MaxCaps, MaxAdvertisedCaps)
	}
}

// A full member list stays within one push event: members carry only a
// reference to their person's roster, never the record.
func TestMembersWithPersonsFitOneEvent(t *testing.T) {
	var m Members
	ref := &PersonRef{ID: vecPerson, Seq: 1 << 40, Hash: strings.Repeat("f", 64)}
	for i := range MaxMembers {
		m.Members = append(m.Members, Member{Address: fmt.Sprintf("%s%04d/%s", strings.Repeat("a", 28), i, strings.Repeat("b", 32)),
			Presence: PresenceReconnecting, Joined: 1790000000, Person: ref})
	}
	data, _ := json.Marshal(m)
	if len(data) >= MaxBody/2 {
		t.Fatalf("a full list is %d bytes, over half of %d", len(data), MaxBody)
	}
	if err := m.Valid(); err != nil {
		t.Fatal(err)
	}
	m.Members[0].Person = &PersonRef{ID: "x", Hash: "y"}
	if m.Valid() == nil {
		t.Fatal("a malformed person reference passed")
	}
}

// A label is refused with the rule it breaks: a joiner, a non-breaking or
// ideographic space and a bidirectional mark are no control characters,
// and the refusal says what a label may hold instead.
func TestValidLabelSaysWhatItMayHold(t *testing.T) {
	const rule = "label may hold only letters, marks, numbers, punctuation, symbols and plain spaces"
	for _, l := range []string{"Anna\u200dMaria", "Anna\u200cMaria", "Anna\u00a0Maria", "Anna\u3000Maria", "Anna \u200f(Sales)", "Bo\x01b", "tab\tx", "line\u2028sep", "private\ue000use"} {
		err := ValidLabel(l)
		if err == nil || !strings.Contains(err.Error(), rule) {
			t.Errorf("label %q: %v", l, err)
		}
	}
	for _, l := range []string{"Anna Maria", "Сергей", "李雷", "e\u0301", "😀"} {
		if err := ValidLabel(l); err != nil {
			t.Errorf("label %q refused: %v", l, err)
		}
	}
}
