package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const (
	vecPerson  = "0123456789abcdef0123456789abcdef"
	vecOther   = "fedcba9876543210fedcba9876543210"
	vecFP      = "01234567-89abcdef-01234567-89abcdef"
	vecSession = "00112233445566778899aabbccddeeff"
)

func vecKey() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(make([]byte, 32)) }

func vecRoster() PersonRoster {
	r := PersonRoster{Person: vecPerson, Label: "Vitalii <&> Ю", Devices: []RosterDevice{{Address: "vitalii/desk", Fingerprint: vecFP}}}
	r.Sign(vecKey())
	return r
}

func vecRoot(r PersonRoster) ConvRoot {
	c := ConvRoot{V: 1, Kind: ConvKindDM,
		Creator: ConvCreator{Person: vecPerson, Roster: r.Hash(), Address: "vitalii/desk", Fingerprint: vecFP},
		Members: []ConvMember{{Person: vecPerson, Roster: r.Hash()}, {Person: vecOther, Roster: "00" + r.Hash()[2:]}},
		Nonce:   vecSession, Created: 1790000000}
	c.Sign(vecKey())
	return c
}

// Exact canonical bytes, hashes and signatures (key from an all-zero seed):
// the wire contract another implementation must reproduce.
func TestSignedRecordVectors(t *testing.T) {
	r := vecRoster()
	check := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", what, got, want)
		}
	}
	check("person bytes", string(r.Canonical()), "agentnet-person-v1\n"+
		`{"person":"0123456789abcdef0123456789abcdef","label":"Vitalii \u003c\u0026\u003e Ю","seq":0,"prev":"",`+
		`"devices":[{"address":"vitalii/desk","fingerprint":"01234567-89abcdef-01234567-89abcdef"}]}`)
	check("person hash", r.Hash(), "57cbbb8dd8d82a48e8949e376f4b8f75f4945ac496f770790554d55a78065a04")
	check("person sig", hex.EncodeToString(r.Sig), "e98f012ea6df8e4c43ebaf71408b0b89afdd8072c416033749a8ecca5f857a7df6e9e49c0c667ca0b0115423deb042e22d5c71258651e1b5f39e529b2d5d8c0c")

	c := vecRoot(r)
	check("root bytes", string(c.Canonical()), "agentnet-conv-root-v1\n"+
		`{"v":1,"kind":"dm","creator":{"person":"0123456789abcdef0123456789abcdef","roster":"57cbbb8dd8d82a48e8949e376f4b8f75f4945ac496f770790554d55a78065a04",`+
		`"address":"vitalii/desk","fingerprint":"01234567-89abcdef-01234567-89abcdef"},"members":[{"person":"0123456789abcdef0123456789abcdef",`+
		`"roster":"57cbbb8dd8d82a48e8949e376f4b8f75f4945ac496f770790554d55a78065a04"},{"person":"fedcba9876543210fedcba9876543210",`+
		`"roster":"00cbbb8dd8d82a48e8949e376f4b8f75f4945ac496f770790554d55a78065a04"}],"nonce":"00112233445566778899aabbccddeeff","created":1790000000}`)
	check("conversation id", c.ID(), "e0758d3e1872da6abc62304e16423c9ae8782d39be3517e4503df4b6ac88b75a")
	check("root sig", hex.EncodeToString(c.Sig), "4666794171e815172cf5c3cc7c5165a2efcff3848e4f767d52f5d5f302983ea9f8f004204ee9641b4fcbf23586a67adf596fa981702ce910127c5518aa2c5906")

	k := CapsRecord{Address: "vitalii/desk", Session: vecSession, Caps: []string{CapEnv2}, TS: 1790000000}
	k.Sign(vecKey())
	check("caps bytes", string(k.Canonical()), "agentnet-caps-v1\n"+
		`{"address":"vitalii/desk","session":"00112233445566778899aabbccddeeff","caps":["env2"],"ts":1790000000}`)
	check("caps sig", hex.EncodeToString(k.Sig), "b671087c9e1f96822b472e9cd439150c75603ee4ae9d804c68cf06ed60ef6ef6b56b0df4fad69c5141152c0db263c01b6dc4d61160ca4ce0811bbea4143ad300")

	pub := vecKey().Public().(ed25519.PublicKey)
	if r.Verify(pub) != nil || c.Verify(pub) != nil || k.Verify(pub) != nil {
		t.Fatal("vectors do not verify")
	}
	// The signed forms round-trip through the strict parsers.
	for what, v := range map[string]any{"person": r, "root": c, "caps": k} {
		data, _ := json.Marshal(v)
		var err error
		switch what {
		case "person":
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

// Anything but the signed bytes, or a record out of bounds, is refused.
func TestSignedRecordsRefuse(t *testing.T) {
	pub := vecKey().Public().(ed25519.PublicKey)
	other := ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32))).Public().(ed25519.PublicKey)
	r := vecRoster()
	bad := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s accepted", what)
		}
	}
	bad("another key", r.Verify(other))
	changed := r
	changed.Label = "Mallory"
	bad("a changed label", changed.Verify(pub))
	for what, mut := range map[string]func(*PersonRoster){
		"seq 1":           func(r *PersonRoster) { r.Seq = 1 },
		"prev":            func(r *PersonRoster) { r.Prev = strings.Repeat("a", 64) },
		"two devices":     func(r *PersonRoster) { r.Devices = append(r.Devices, r.Devices[0]) },
		"no device":       func(r *PersonRoster) { r.Devices = nil },
		"empty label":     func(r *PersonRoster) { r.Label = "" },
		"padded label":    func(r *PersonRoster) { r.Label = " x" },
		"control char":    func(r *PersonRoster) { r.Label = "a\x07b" },
		"long label":      func(r *PersonRoster) { r.Label = strings.Repeat("a", MaxPersonLabel+1) },
		"bad id":          func(r *PersonRoster) { r.Person = "x" },
		"bad address":     func(r *PersonRoster) { r.Devices[0].Address = "Vitalii" },
		"bad fingerprint": func(r *PersonRoster) { r.Devices[0].Fingerprint = "abc" },
	} {
		m := vecRoster()
		m.Devices = append([]RosterDevice(nil), m.Devices...)
		mut(&m)
		bad(what, m.Validate())
	}
	if _, err := ParsePersonRoster([]byte(`{"person":"` + vecPerson + `","label":"x","seq":0,"prev":"","devices":[],"extra":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := ParsePersonRoster(make([]byte, MaxPersonRecord+1)); err == nil {
		t.Error("oversized record accepted")
	}

	c := vecRoot(r)
	bad("root by another key", c.Verify(other))
	for what, mut := range map[string]func(*ConvRoot){
		"three members": func(c *ConvRoot) {
			c.Members = append(c.Members, ConvMember{Person: strings.Repeat("c", 32), Roster: r.Hash()})
		},
		"unsorted":          func(c *ConvRoot) { c.Members[0], c.Members[1] = c.Members[1], c.Members[0] },
		"same person":       func(c *ConvRoot) { c.Members[1] = c.Members[0] },
		"creator no member": func(c *ConvRoot) { c.Creator.Person = strings.Repeat("d", 32) },
		"creator roster":    func(c *ConvRoot) { c.Creator.Roster = strings.Repeat("e", 64) },
		"kind":              func(c *ConvRoot) { c.Kind = "group" },
		"version":           func(c *ConvRoot) { c.V = 2 },
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

// A full member list stays within one push event even when every member
// carries a person record of the largest size.
func TestMembersWithPersonsFitOneEvent(t *testing.T) {
	var m Members
	person := json.RawMessage(`"` + strings.Repeat("p", MaxPersonRecord-2) + `"`)
	for i := range MaxMembers {
		m.Members = append(m.Members, Member{Address: strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31) + string(rune('a'+i%26)),
			Presence: PresenceReconnecting, Joined: 1790000000, Person: person})
	}
	data, _ := json.Marshal(m)
	if len(data) >= MaxBody {
		t.Fatalf("a full list is %d bytes, over %d", len(data), MaxBody)
	}
}
