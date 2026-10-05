package client

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"strings"
	"testing"
)

func TestConversationCopiesKeepNeedsYou(t *testing.T) {
	for _, localFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "local-last", true: "local-first"}[localFirst], func(t *testing.T) {
			s := securityStore(t)
			localID, otherID := "b-local", "a-other"
			if localFirst {
				localID, otherID = "a-local", "b-other"
			}
			detail := "Which account should I use?\n\nPlease choose the company account.\n" + strings.Repeat("long", 100)
			for _, c := range []struct{ id, to string }{{localID, "me/laptop"}, {otherID, "me/phone"}} {
				if _, err := s.db.Exec(`INSERT INTO outbox(id,lid,recipient,body,envelope,state,created_at,created_ms,kind,conv,pid) VALUES(?, 'logical', ?, 'Original request', '{}', 'delivered', 1, 1, 'task', 'chat', 'agent')`, c.id, c.to); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`INSERT INTO inbox(id,lid,sender,ts,kind,body,received_at,state,detail,local,conv,pid) VALUES(?, 'logical', 'me/laptop', 1, 'task', 'Original request', 1, 'needs_human', ?, 1, 'chat', 'agent')`, localID, detail); err != nil {
				t.Fatal(err)
			}
			msgs, err := s.convMessages("chat", "me/laptop", "key", map[string]bool{"me/laptop": true, "me/phone": true})
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 1 || msgs[0].ID != localID || msgs[0].Job != stateNeedHuman || msgs[0].JobDetail != detail || len(msgs[0].Copies) != 2 {
				t.Fatalf("lost local result or actionable id: %+v", msgs)
			}
		})
	}
}

func TestNeedsYouPrivateReportScope(t *testing.T) {
	s := securityStore(t)
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := id.Public("me/phone")
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Me", Devices: []identity.Public{pub}}
	r.Sign(id.Sign)
	raw, _ := json.Marshal(r)
	if _, err = s.db.Exec(`INSERT INTO persons(person,label,seq,hash,record,state,pinned_at) VALUES(?,'Me',0,?,?,'self',1)`, r.Person, r.Hash(), string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO person_devices(address,person,fingerprint,added) VALUES(?,?,?,0)`, pub.Address, r.Person, pub.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if err = s.pin(pub); err != nil {
		t.Fatal(err)
	}
	holds := func(want bool) {
		t.Helper()
		got, err := ownerReportHolds(s.db, pub.Address, pub.Fingerprint())
		if err != nil || got != want {
			t.Fatalf("current owner = %v, %v; want %v", got, err, want)
		}
	}
	holds(true)
	// Native owner devices project the same report beside the exact request.
	agent := &Agent{store: s, Address: pub.Address}
	requestID := protocol.NewID()
	full := "Private choice\n\nAll paragraphs survive."
	reportItems := []ReportItem{{ID: requestID, From: pub.Address, Key: pub.Fingerprint(), Kind: envelope.KindTask, State: stateNeedHuman, Blocker: BlockerNeedsHuman, Since: 1, Conv: true}}
	body := agent.reportBodyFor(reportItems, map[string]string{requestID: full}, true, false)
	if _, err = s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,status) VALUES(?,?,1,'message',?,1,'needs_human',?,'review_notice')`, protocol.NewID(), pub.Address, body, pub.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	turn := ConvMessage{ID: requestID, LID: requestID, Key: pub.Fingerprint(), Target: &envelope.Target{Address: pub.Address, Fingerprint: pub.Fingerprint()}, Exec: &ExecView{State: stateNeedHuman}}
	turns := []ConvMessage{turn}
	if err = agent.privateNeedsYou(turns); err != nil || turns[0].JobDetail != full || turns[0].Job != "" {
		t.Fatalf("native private turn: %+v %v", turns, err)
	}
	turns = []ConvMessage{turn}
	turns[0].Key = "other-key"
	if err = agent.privateNeedsYou(turns); err != nil || turns[0].JobDetail != "" {
		t.Fatalf("another requester received text: %+v %v", turns, err)
	}
	if op, err := operatorHolds(s.db, pub.Address, pub.Fingerprint()); err != nil || op {
		t.Fatalf("visibility granted operator access: %v %v", op, err)
	}
	changed, _ := identity.Generate()
	if err = s.setPending(changed.Public(pub.Address)); err != nil {
		t.Fatal(err)
	}
	holds(false)
	s.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, pub.Address)
	holds(true)
	s.db.Exec(`UPDATE persons SET state='conflict' WHERE person=?`, r.Person)
	holds(false)
	s.db.Exec(`UPDATE persons SET state='pinned' WHERE person=?`, r.Person)
	holds(false) // another person is not the installation's owner
	s.db.Exec(`UPDATE persons SET state='self' WHERE person=?`, r.Person)
	s.db.Exec(`DELETE FROM person_devices WHERE address=?`, pub.Address)
	holds(false)
	a := &Agent{Address: "me/laptop"}
	text := "Private agent question\n\nFull second paragraph " + strings.Repeat("long", 100)
	items := []ReportItem{{ID: protocol.NewID(), State: stateNeedHuman, Excerpt: "Original request"}}
	for _, tc := range []struct {
		name             string
		full, actionable bool
		want             string
	}{{"own", true, false, text}, {"steward", true, true, text}, {"operator", false, true, "Original request"}} {
		parsed, ok := ParseReport(a.reportBodyFor(items, map[string]string{items[0].ID: text}, tc.full, tc.actionable))
		if !ok || len(parsed.Items) != 1 || parsed.Items[0].Excerpt != tc.want || parsed.Items[0].Actionable != tc.actionable {
			t.Fatalf("%s: %+v", tc.name, parsed)
		}
	}
	public, detail, ok := statusOf(stateNeedHuman)
	if !ok || public != "needs_human" || detail != BlockerNeedsHuman || strings.Contains(detail, text) {
		t.Fatalf("private output entered public status: %s %s", public, detail)
	}
}
