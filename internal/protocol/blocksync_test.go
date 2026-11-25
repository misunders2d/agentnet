package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBlockSync(t *testing.T) {
	m := BlockMark{Person: NewID(), Blocked: true, Revision: 1790000000000, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}
	r := BlockSync{V: 1, Person: NewID(), Roster: strings.Repeat("a", 64), Blocks: []BlockMark{m}}
	raw, _ := json.Marshal(r)
	if got, err := ParseBlockSync(raw); err != nil || len(got.Blocks) != 1 || !got.Blocks[0].Blocked {
		t.Fatalf("valid: %+v %v", got, err)
	}
	unblock := m
	unblock.Blocked = false
	unblock.Revision++
	if !unblock.Newer(m) || m.Newer(unblock) {
		t.Fatal("a later unblock wins")
	}
	for name, change := range map[string]func(*BlockSync){
		"bad person":      func(r *BlockSync) { r.Blocks[0].Person = "x" },
		"self":            func(r *BlockSync) { r.Blocks[0].Person = r.Person },
		"bad writer":      func(r *BlockSync) { r.Blocks[0].Writer = "x" },
		"zero revision":   func(r *BlockSync) { r.Blocks[0].Revision = 0 },
		"duplicate":       func(r *BlockSync) { r.Blocks = append(r.Blocks, r.Blocks[0]) },
		"empty":           func(r *BlockSync) { r.Blocks = nil },
		"version":         func(r *BlockSync) { r.V = 2 },
		"unsafe revision": func(r *BlockSync) { r.Blocks[0].Revision = MaxTopicTitleRevision + 1 },
	} {
		c := r
		c.Blocks = append([]BlockMark(nil), r.Blocks...)
		change(&c)
		raw, _ := json.Marshal(c)
		if _, err := ParseBlockSync(raw); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := ParseBlockSync([]byte(`{"v":1,"person":"` + r.Person + `","roster":"` + r.Roster + `","blocks":[{"person":"` + m.Person + `","revision":1,"writer":"` + m.Writer + `"}]}`)); err == nil {
		t.Fatal("a block without its blocked field was accepted")
	}
	caps := CapsRecord{Caps: []string{CapReminderSyncV2}}
	if !caps.Reads(CapReminderSync) || !caps.Reads(CapReminderSyncV2) {
		t.Fatal("rem2 implies rem1")
	}
}
