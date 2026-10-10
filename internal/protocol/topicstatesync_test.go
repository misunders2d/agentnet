package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTopicStateSyncStrictPrivateMarks(t *testing.T) {
	base := TopicStateSync{V: 1, Person: NewID(), Roster: strings.Repeat("a", 64), Marks: []TopicMark{{Scope: strings.Repeat("b", 64), Topic: NewID(), Mark: TopicMarkDone, Count: 3, At: 1700000000, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}}}
	check := func(r TopicStateSync, want bool) {
		t.Helper()
		raw, _ := json.Marshal(r)
		_, err := ParseTopicStateSync(raw)
		if (err == nil) != want {
			t.Fatalf("valid=%v want=%v: %s %v", err == nil, want, raw, err)
		}
	}
	one := func(mutate func(*TopicMark)) TopicStateSync {
		r := base
		r.Marks = append([]TopicMark(nil), base.Marks...)
		mutate(&r.Marks[0])
		return r
	}
	check(base, true)
	for _, ok := range []func(*TopicMark){
		func(m *TopicMark) { m.Mark = TopicMarkOpen },
		func(m *TopicMark) { m.Mark = TopicMarkArchived },
		func(m *TopicMark) { m.Mark, m.Count = TopicMarkNone, 0 },
		func(m *TopicMark) { m.Scope = "admin/laptop" },
		func(m *TopicMark) { m.At, m.Count = MaxTopicTitleRevision, MaxTopicTitleRevision },
	} {
		check(one(ok), true)
	}
	for _, bad := range []func(*TopicMark){
		func(m *TopicMark) { m.Mark = "deleted" },
		func(m *TopicMark) { m.Mark = "Done" },
		func(m *TopicMark) { m.Count = 0 },
		func(m *TopicMark) { m.Count = -1 },
		func(m *TopicMark) { m.Mark = TopicMarkNone },
		func(m *TopicMark) { m.At = 0 },
		func(m *TopicMark) { m.At = MaxTopicTitleRevision + 1 },
		func(m *TopicMark) { m.Count = MaxTopicTitleRevision + 1 },
		func(m *TopicMark) { m.Scope = "not a scope" },
		func(m *TopicMark) { m.Topic = "short" },
		func(m *TopicMark) { m.Writer = "nobody" },
	} {
		check(one(bad), false)
	}
	dup := base
	dup.Marks = append([]TopicMark(nil), base.Marks[0], base.Marks[0])
	dup.Marks[1].At++
	check(dup, false)
	empty := base
	empty.Marks = nil
	check(empty, false)
	full := base
	full.Marks = nil
	for range MaxTopicMarks + 1 {
		m := base.Marks[0]
		m.Topic = NewID()
		full.Marks = append(full.Marks, m)
	}
	check(full, false)
	full.Marks = full.Marks[:MaxTopicMarks]
	check(full, true)
	raw, _ := json.Marshal(base)
	for _, bad := range []string{
		strings.Replace(string(raw), `"mark":"done",`, "", 1),
		strings.Replace(string(raw), `"mark":"done"`, `"mark":null`, 1),
		strings.Replace(string(raw), `"count":3,`, "", 1),
		strings.Replace(string(raw), `"count":3`, `"count":3.5`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":2`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"grant":true`, 1),
		strings.Replace(string(raw), `"writer"`, `"task":true,"writer"`, 1),
	} {
		if _, err := ParseTopicStateSync([]byte(bad)); err == nil {
			t.Fatalf("malformed mark accepted: %s", bad)
		}
	}
	// A title carrier is not a mark carrier, nor the reverse: each strict
	// reader refuses the other's body.
	if _, err := ParseTopicSync(raw); err == nil {
		t.Fatal("title reader accepted marks")
	}
}

func TestTopicMarkNewerConverges(t *testing.T) {
	low, high := "00000000-00000000-00000000-00000000", "ffffffff-ffffffff-ffffffff-ffffffff"
	marks := []TopicMark{
		{Mark: TopicMarkDone, Count: 2, At: 10, Writer: high},
		{Mark: TopicMarkOpen, Count: 2, At: 11, Writer: low},
		{Mark: TopicMarkArchived, Count: 2, At: 11, Writer: high},
		{Mark: TopicMarkOpen, Count: 2, At: 11, Writer: high},
		{Mark: TopicMarkOpen, Count: 3, At: 11, Writer: high},
	}
	// Every arrival order yields the same winner: the newest time, then the
	// writer, mark and count.
	want := marks[4]
	for _, order := range [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {2, 4, 0, 3, 1}, {1, 0, 4, 2, 3}} {
		var cur TopicMark
		for i, j := range order {
			if i == 0 || marks[j].Newer(cur) {
				cur = marks[j]
			}
		}
		if cur != want {
			t.Fatalf("order %v converged on %+v", order, cur)
		}
	}
	for _, m := range marks {
		if m.Newer(m) {
			t.Fatalf("%+v is newer than itself", m)
		}
	}
}
