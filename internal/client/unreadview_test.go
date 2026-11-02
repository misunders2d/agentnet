package client

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestUnreadConversationCandidatesKeepLegacyExcerpts(t *testing.T) {
	s := securityStore(t)
	a := &Agent{store: s}
	for _, tc := range []struct {
		name, sub string
		read      any
		local     int
		ref       any
		want      bool
	}{
		{name: "ordinary", want: true},
		{name: "already read", read: int64(1)},
		{name: "selected history", sub: envelope.SubExcerpt, want: true},
		{name: "local job", local: 1},
		{name: "control", ref: protocol.NewID()},
		{name: "participation record", sub: envelope.SubEvent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conv := strings.Repeat("a", 64)
			if _, err := s.db.Exec(`DELETE FROM inbox`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv,sub,read_at,local,ref_id) VALUES(?, 'bob/desk', 1, 'message', '', 1, ?, ?, ?, ?, ?)`, protocol.NewID(), conv, tc.sub, tc.read, tc.local, tc.ref); err != nil {
				t.Fatal(err)
			}
			candidates, err := a.UnreadConversationCandidates()
			if err != nil || candidates[conv] != tc.want {
				t.Fatalf("candidate=%v want %v: %v", candidates[conv], tc.want, err)
			}
		})
	}

	// The badge's running count recounts only chats a write marked
	// (MEL-731). A write's own OR IGNORE overrides a trigger's conflict
	// clause, so marking must still move an already marked chat past the
	// reader's mark; reading marks writes nothing.
	conv, id := strings.Repeat("b", 64), protocol.NewID()
	_, mark, err := a.UnreadDirtySince(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		write string
		want  []string
	}{
		{`INSERT OR IGNORE INTO inbox(id,sender,ts,kind,body,received_at,conv) VALUES(?2,'bob/desk',1,'message','',1,?1)`, []string{"c:" + conv}},
		{`UPDATE OR IGNORE inbox SET read_at=1 WHERE id=?2 AND ?1<>''`, []string{"c:" + conv}},
		{`UPDATE inbox SET status_due=5 WHERE id=?2 AND ?1<>''`, nil},
	} {
		if _, err := s.db.Exec(step.write, conv, id); err != nil {
			t.Fatal(err)
		}
		before, _ := s.rowChanges()
		keys, next, err := a.UnreadDirtySince(mark)
		if after, _ := s.rowChanges(); err != nil || strings.Join(keys, ",") != strings.Join(step.want, ",") || after != before {
			t.Fatalf("%s: marked %v want %v, reader wrote %d rows: %v", step.write, keys, step.want, after-before, err)
		}
		mark = next
	}
}
