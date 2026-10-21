package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReminderSyncStrictAndOrder(t *testing.T) {
	m := ReminderMark{Fingerprint: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", LID: NewID(), Due: 1790000000, State: "pending", Revision: 1790000000000, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}
	r := ReminderSync{V: 1, Person: NewID(), Roster: strings.Repeat("a", 64), Reminders: []ReminderMark{m}}
	raw, _ := json.Marshal(r)
	if _, err := ParseReminderSync(raw); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "done", "cancelled", "replied"} {
		r.Reminders[0].State = state
		b, _ := json.Marshal(r)
		if _, err := ParseReminderSync(b); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
	}
	for name, change := range map[string]func(*ReminderMark){"bad state": func(m *ReminderMark) { m.State = "working" }, "missing author": func(m *ReminderMark) { m.Fingerprint = "" }, "bad conv": func(m *ReminderMark) { m.Conv = "other/topic" }, "bad lid": func(m *ReminderMark) { m.LID = "x" }, "bad writer": func(m *ReminderMark) { m.Writer = "x" }, "zero revision": func(m *ReminderMark) { m.Revision = 0 }, "unsafe revision": func(m *ReminderMark) { m.Revision = MaxTopicTitleRevision + 1 }, "zero due": func(m *ReminderMark) { m.Due = 0 }, "unsafe due": func(m *ReminderMark) { m.Due = MaxTopicTitleRevision + 1 }} {
		t.Run(name, func(t *testing.T) {
			r.Reminders = []ReminderMark{m}
			change(&r.Reminders[0])
			b, _ := json.Marshal(r)
			if _, err := ParseReminderSync(b); err == nil {
				t.Fatal("accepted invalid reminder")
			}
		})
	}
	for _, b := range []string{strings.Replace(string(raw), `"conv":"",`, "", 1), strings.Replace(string(raw), `"due":1790000000`, `"due":null`, 1), strings.Replace(string(raw), `"v":1`, `"v":1,"extra":true`, 1), strings.Replace(string(raw), `"state":"pending"`, `"state":"pending","execute":true`, 1), string(raw) + strings.Repeat(" ", 65536)} {
		if _, err := ParseReminderSync([]byte(b)); err == nil {
			t.Fatal("accepted incomplete or oversized carrier")
		}
	}
	r.Reminders = []ReminderMark{m, m}
	b, _ := json.Marshal(r)
	if _, err := ParseReminderSync(b); err == nil {
		t.Fatal("duplicate original")
	}
	r.Reminders = nil
	for i := 0; i < MaxReminderMarks+1; i++ {
		next := m
		next.LID = NewID()
		r.Reminders = append(r.Reminders, next)
	}
	b, _ = json.Marshal(r)
	if _, err := ParseReminderSync(b); err == nil {
		t.Fatal("unbounded carrier")
	}
	newer := m
	newer.Revision++
	if !newer.Newer(m) || m.Newer(newer) {
		t.Fatal("revision order")
	}
	newer = m
	newer.Writer = "ffffffff-ffffffff-ffffffff-ffffffff"
	if !newer.Newer(m) || m.Newer(newer) {
		t.Fatal("writer tie break")
	}
	newer = m
	newer.Due++
	if newer.Newer(m) {
		t.Fatal("payload does not invent another generation")
	}
}
