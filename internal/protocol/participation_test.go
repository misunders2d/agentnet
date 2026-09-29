package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func vecInvite() ParticipationEvent {
	r := vecRoster()
	e := ParticipationEvent{V: 1, Conv: vecRoot(r).ID(), PID: vecSession, Type: EventInvite, TS: 1790000000,
		Author:   EventAuthor{Person: vecPerson, Roster: r.Hash(), Address: "vitalii/desk", Fingerprint: vecFP},
		Host:     &ParticipationHost{Person: vecOther, Address: "sergey/laptop", Fingerprint: vecFP},
		Grant:    []GrantRef{{LID: strings.Repeat("1", 32), Fingerprint: vecFP}, {LID: strings.Repeat("2", 32), Fingerprint: vecFP}},
		Audience: AudienceConversation, TaskKeys: []string{vecFP}, Note: "check the deploy <&>"}
	e.Sign(vecKey())
	return e
}

// Exact canonical bytes, hash and signature of an invite and of the accept
// that answers it (key from an all-zero seed).
func TestParticipationVectors(t *testing.T) {
	inv := vecInvite()
	want := "agentnet-participation-v1\n" + `{"v":1,"conv":"e0758d3e1872da6abc62304e16423c9ae8782d39be3517e4503df4b6ac88b75a",` +
		`"pid":"00112233445566778899aabbccddeeff","type":"invite","prev":"","author":{"person":"0123456789abcdef0123456789abcdef",` +
		`"roster":"57cbbb8dd8d82a48e8949e376f4b8f75f4945ac496f770790554d55a78065a04","address":"vitalii/desk",` +
		`"fingerprint":"01234567-89abcdef-01234567-89abcdef"},"ts":1790000000,"host":{"person":"fedcba9876543210fedcba9876543210",` +
		`"address":"sergey/laptop","fingerprint":"01234567-89abcdef-01234567-89abcdef"},"grant":[{"lid":"11111111111111111111111111111111",` +
		`"fingerprint":"01234567-89abcdef-01234567-89abcdef"},{"lid":"22222222222222222222222222222222",` +
		`"fingerprint":"01234567-89abcdef-01234567-89abcdef"}],"audience":"conversation","task_keys":["01234567-89abcdef-01234567-89abcdef"],` +
		`"note":"check the deploy \u003c\u0026\u003e"}`
	if got := string(inv.Canonical()); got != want {
		t.Fatalf("invite bytes:\n got %q\nwant %q", got, want)
	}
	if inv.Hash() != "f8f1666eb4184ec2e81cdb8483c4702a63c45a3c05f76339a2f092afc4f2d818" ||
		hex.EncodeToString(inv.Sig) != "5f8a2a8b5144cc66a9d13b06daa46810d82a74922bccf7b14a84a1c2992ce2c18c5eac6062f91562c2ef942f49c4538e6c1617382d3c197ae354f9deebfcac05" {
		t.Fatalf("invite hash %s sig %x", inv.Hash(), inv.Sig)
	}
	acc := ParticipationEvent{V: 1, Conv: inv.Conv, PID: inv.PID, Type: EventAccept, Prev: inv.Hash(), TS: 1790000100,
		Author: EventAuthor{Person: vecOther, Roster: vecRoster().Hash(), Address: "sergey/laptop", Fingerprint: vecFP}}
	acc.Sign(vecKey())
	wantAcc := "agentnet-participation-v1\n" + `{"v":1,"conv":"e0758d3e1872da6abc62304e16423c9ae8782d39be3517e4503df4b6ac88b75a",` +
		`"pid":"00112233445566778899aabbccddeeff","type":"accept","prev":"f8f1666eb4184ec2e81cdb8483c4702a63c45a3c05f76339a2f092afc4f2d818",` +
		`"author":{"person":"fedcba9876543210fedcba9876543210","roster":"57cbbb8dd8d82a48e8949e376f4b8f75f4945ac496f770790554d55a78065a04",` +
		`"address":"sergey/laptop","fingerprint":"01234567-89abcdef-01234567-89abcdef"},"ts":1790000100}`
	if string(acc.Canonical()) != wantAcc || acc.Hash() != "8c77bf7bc19c7a372d13cc86fca4c433bcc407ffbebd94a5044c7ee8973d4cd2" {
		t.Fatalf("accept bytes:\n got %s\nhash %s", acc.Canonical(), acc.Hash())
	}
	pub := vecKey().Public().(ed25519.PublicKey)
	for _, e := range []ParticipationEvent{inv, acc} {
		if err := e.Verify(pub); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(e)
		if _, err := ParseParticipationEvent(data); err != nil {
			t.Fatal(err)
		}
	}
}

// Out-of-shape events are refused: each type carries only its own fields,
// within bounds, and a changed field breaks the signature.
func TestParticipationEventRefuses(t *testing.T) {
	pub := vecKey().Public().(ed25519.PublicKey)
	inv := vecInvite()
	changed := inv
	changed.Grant = append([]GrantRef{{LID: strings.Repeat("3", 32), Fingerprint: vecFP}}, inv.Grant...)
	if changed.Verify(pub) == nil {
		t.Fatal("a widened grant still verified")
	}
	many := func(n int, f func(int) string) []string {
		var out []string
		for i := range n {
			out = append(out, f(i))
		}
		return out
	}
	for name, mut := range map[string]func(*ParticipationEvent){
		"invite with prev":    func(e *ParticipationEvent) { e.Prev = strings.Repeat("a", 64) },
		"invite without host": func(e *ParticipationEvent) { e.Host = nil },
		"other audience":      func(e *ParticipationEvent) { e.Audience = "owner" },
		"grant too long": func(e *ParticipationEvent) {
			e.Grant = nil
			for _, id := range many(MaxGrant+1, func(i int) string { return hexID(i) }) {
				e.Grant = append(e.Grant, GrantRef{LID: id, Fingerprint: vecFP})
			}
		},
		"repeated grant":        func(e *ParticipationEvent) { e.Grant = []GrantRef{e.Grant[0], e.Grant[0]} },
		"grant without its key": func(e *ParticipationEvent) { e.Grant = []GrantRef{{LID: e.Grant[0].LID}} },
		"bad grant id":          func(e *ParticipationEvent) { e.Grant = []GrantRef{{LID: "x", Fingerprint: vecFP}} },
		"too many task keys":    func(e *ParticipationEvent) { e.TaskKeys = many(MaxTaskKeys+1, func(i int) string { return hexFP(i) }) },
		"bad task key":          func(e *ParticipationEvent) { e.TaskKeys = []string{"k"} },
		"long note":             func(e *ParticipationEvent) { e.Note = strings.Repeat("n", MaxInviteNote+1) },
		"control in note":       func(e *ParticipationEvent) { e.Note = "a\x07" },
		"unknown type":          func(e *ParticipationEvent) { e.Type = "promote" },
		"bad pid":               func(e *ParticipationEvent) { e.PID = "p" },
		"accept with a grant": func(e *ParticipationEvent) {
			e.Type, e.Prev, e.Host, e.Audience = EventAccept, strings.Repeat("a", 64), nil, ""
		},
		"dismiss without prev": func(e *ParticipationEvent) {
			e.Type, e.Prev, e.Host, e.Grant, e.Audience, e.TaskKeys, e.Note = EventDismiss, "", nil, nil, "", nil, ""
		},
		"no time":                func(e *ParticipationEvent) { e.TS = 0 },
		"bad author fingerprint": func(e *ParticipationEvent) { e.Author.Fingerprint = "f" },
	} {
		e := vecInvite()
		mut(&e)
		if e.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ParseParticipationEvent(make([]byte, MaxParticipationEvent+1)); err == nil {
		t.Error("oversized event parsed")
	}
}

func hexID(i int) string {
	return strings.Repeat("0", 28) + hex.EncodeToString([]byte{byte(i >> 8), byte(i)})
}
func hexFP(i int) string {
	s := hexID(i)
	return s[:8] + "-" + s[8:16] + "-" + s[16:24] + "-" + s[24:]
}
