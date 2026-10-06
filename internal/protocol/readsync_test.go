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
