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
}
