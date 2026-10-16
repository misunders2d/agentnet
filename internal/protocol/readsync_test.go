package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadSyncStrictBoundedRefs(t *testing.T) {
	r := ReadSync{V: 1, Person: NewID(), Roster: strings.Repeat("a", 64), Refs: []ReadRef{{Conv: "", Fingerprint: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", LID: NewID()}}}
	raw, _ := json.Marshal(r)
	if _, err := ParseReadSync(raw); err != nil {
		t.Fatal(err)
	}
	r.Refs = append(r.Refs, r.Refs[0])
	raw, _ = json.Marshal(r)
	if _, err := ParseReadSync(raw); err == nil {
		t.Fatal("duplicate refs")
	}
	if _, err := ParseReadSync([]byte(`{"v":1,"extra":true}`)); err == nil {
		t.Fatal("unknown field")
	}
	r.Refs = nil
	for i := 0; i <= MaxReadRefs; i++ {
		r.Refs = append(r.Refs, ReadRef{Fingerprint: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", LID: NewID()})
	}
	raw, _ = json.Marshal(r)
	if _, err := ParseReadSync(raw); err == nil {
		t.Fatal("unbounded refs")
	}
}

func TestReadSyncV2AttentionCompatibility(t *testing.T) {
	raw := []byte(`{"v":2,"person":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","roster":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","marks":[{"conv":"","fingerprint":"aaaaaaaa-bbbbbbbb-cccccccc-dddddddd","lid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","state":"unread","rev":1,"writer":"aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}]}`)
	if _, err := ParseReadSync(raw); err != nil {
		t.Fatal("valid V2 attention rejected", err)
	}
}

func TestReadSyncAttentionStrictGenerations(t *testing.T) {
	ref := ReadRef{Fingerprint: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", LID: NewID()}
	r := ReadSync{V: 2, Person: NewID(), Roster: strings.Repeat("a", 64), Marks: []AttentionMark{{ReadRef: ref, AttentionGeneration: AttentionGeneration{Rev: 1, Writer: ref.Fingerprint}, State: "unread"}}}
	valid, _ := json.Marshal(r)
	for _, raw := range []string{strings.Replace(string(valid), `"rev":1`, `"rev":0`, 1), strings.Replace(string(valid), `"rev":1`, `"rev":1.5`, 1), strings.Replace(string(valid), `"unread"`, `"unknown"`, 1), strings.Replace(string(valid), `"v":2`, `"v":1`, 1), strings.Replace(string(valid), `"rev":1`, `"rev":9007199254740992`, 1)} {
		if _, err := ParseReadSync([]byte(raw)); err == nil {
			t.Fatal("malformed mark accepted", raw)
		}
	}
	r.Refs = []ReadRef{ref}
	raw, _ := json.Marshal(r)
	if _, err := ParseReadSync(raw); err == nil {
		t.Fatal("tuple duplicated across arrays")
	}
	legacy := ReadSync{V: 1, Person: r.Person, Roster: r.Roster, Refs: []ReadRef{ref}}
	raw, _ = json.Marshal(legacy)
	want := `{"v":1,"person":"` + legacy.Person + `","roster":"` + legacy.Roster + `","refs":[{"conv":"","fingerprint":"` + ref.Fingerprint + `","lid":"` + ref.LID + `"}]}`
	if string(raw) != want {
		t.Fatal("V1 bytes changed")
	}
	if _, err := ParseReadSync([]byte(strings.TrimSuffix(string(raw), "}") + `,"marks":null}`)); err == nil {
		t.Fatal("V1 marks field accepted")
	}
	c := CapsRecord{Caps: []string{CapReadSyncV2}}
	if !c.Reads(CapReadSync) || !c.Reads(CapReadSyncV2) || (CapsRecord{Caps: []string{CapReadSync}}).Reads(CapReadSyncV2) {
		t.Fatal("rd2 successor relation")
	}
}

func TestReadSyncAttentionCombinedBound(t *testing.T) {
	fp := "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"
	r := ReadSync{V: 2, Person: NewID(), Roster: strings.Repeat("a", 64)}
	for i := 0; i < MaxReadRefs; i++ {
		r.Marks = append(r.Marks, AttentionMark{ReadRef: ReadRef{Fingerprint: fp, LID: NewID()}, AttentionGeneration: AttentionGeneration{Rev: MaxAttentionRevision, Writer: fp}, State: "read"})
	}
	raw, _ := json.Marshal(r)
	if _, err := ParseReadSync(raw); err != nil {
		t.Fatal("valid 64-mark batch", err)
	}
	r.Refs = []ReadRef{{Fingerprint: fp, LID: NewID()}}
	raw, _ = json.Marshal(r)
	if _, err := ParseReadSync(raw); err == nil {
		t.Fatal("combined 65 refs/marks accepted")
	}
	r.Refs = nil
	r.Marks[0].Writer = "sender/device"
	raw, _ = json.Marshal(r)
	if _, err := ParseReadSync(raw); err == nil {
		t.Fatal("address used as fingerprint writer")
	}
}
