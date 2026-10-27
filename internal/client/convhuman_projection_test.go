package client

import (
	"reflect"
	"testing"
)

// The bulk query must reproduce storedHuman's exact selected physical source,
// even when logical-copy selection changes ID or own history changes Dir.
func TestConversationBulkHumanKeepsPhysicalSource(t *testing.T) {
	for _, mode := range []string{"in", "own-history", "out", "counterparty", "local-job", "empty", "invalid-selected", "invalid-discarded"} {
		t.Run(mode, func(t *testing.T) {
			s := securityStore(t)
			addOut := func(id, to, raw string) {
				t.Helper()
				if _, err := s.db.Exec(`INSERT INTO outbox(id,lid,recipient,body,envelope,state,created_at,created_ms,kind,conv,pid,human) VALUES(?,'logical',?,'body','{"ts":1}','delivered',1,1,'message','chat','agent',?)`, id, to, raw); err != nil {
					t.Fatal(err)
				}
			}
			addIn := func(id, sender, raw string, local int) {
				t.Helper()
				if _, err := s.db.Exec(`INSERT INTO inbox(id,lid,sender,ts,kind,body,received_at,received_ms,local,conv,pid,human,state) VALUES(?,'logical',?,1,'message','body',1,1,?,'chat','agent',?,'needs_human')`, id, sender, local, raw); err != nil {
					t.Fatal(err)
				}
			}
			first, second := `{"author_pid":"first"}`, `{"author_pid":"selected"}`
			own := map[string]bool{"me/laptop": true, "me/phone": true}
			switch mode {
			case "in":
				addIn("incoming", "other/desk", first, 0)
			case "own-history":
				addIn("imported", "me/phone", first, 0)
			case "out":
				addOut("native", "other/desk", first)
			case "counterparty":
				addOut("a-own", "me/phone", first)
				addOut("b-other", "other/desk", second)
			case "local-job":
				addOut("a-other", "other/desk", first)
				addOut("b-job", "me/laptop", second)
				addIn("b-job", "me/laptop", `{"author_pid":"wrong-local-job"}`, 1)
			case "empty":
				addOut("native", "other/desk", "")
			case "invalid-selected":
				addOut("native", "other/desk", "{")
			case "invalid-discarded":
				addOut("a-own", "me/phone", "{")
				addOut("b-other", "other/desk", second)
			}
			got, err := s.convMessages("chat", "me/laptop", "key", own)
			if mode == "invalid-selected" {
				if err == nil {
					t.Fatal("invalid selected scope accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("rows=%d", len(got))
			}
			m := got[0]
			dir := m.Dir
			if m.Via != "" {
				dir = "in"
			}
			want, err := storedHuman(s.db, dir, m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m.Human, want) {
				t.Fatalf("selected %s/%s scope differs: got %+v want %+v", dir, m.ID, m.Human, want)
			}
			if mode == "counterparty" && m.ID != "b-other" || mode == "local-job" && m.ID != "b-job" {
				t.Fatalf("wrong selected copy %s", m.ID)
			}
			if mode == "own-history" && (m.Dir != "out" || m.Via != "me/phone") {
				t.Fatalf("lost own-history source: %+v", m)
			}
		})
	}
}
