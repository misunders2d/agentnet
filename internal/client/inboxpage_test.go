package client

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestInboxPageKeysetPreservesUnreadAndSameSecondOrder(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("%032x", 100-i)
		if _, err := s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state) VALUES(?,'peer/device',1,'message',?,1,'')`, id, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	o := InboxPageOptions{Unread: true, Limit: 2}
	for pass := 0; pass < 5; pass++ {
		p, err := a.InspectInbox(o)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range p.Items {
			got = append(got, m.Body)
			ids = append(ids, m.ID)
		}
		if err := s.markRead(ids); err != nil {
			t.Fatal(err)
		}
		if pass == 0 {
			if _, err := s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state) VALUES(?,'peer/device',1,'message','later',1,'')`, strings.Repeat("a", 32)); err != nil {
				t.Fatal(err)
			}
			// The cursor remains valid if its prior row is removed meanwhile.
			if _, err := s.db.Exec(`DELETE FROM inbox WHERE id=?`, ids[len(ids)-1]); err != nil {
				t.Fatal(err)
			}
		}
		if p.Next == "" {
			break
		}
		o.Before = p.Next
	}
	if strings.Join(got, ",") != "7,6,5,4,3,2,1" {
		t.Fatalf("paged receipt order: %v", got)
	}
	p, err := a.InspectInbox(InboxPageOptions{Unread: true, Limit: 2})
	if err != nil || len(p.Items) != 1 || p.Items[0].Body != "later" {
		t.Fatalf("new arrival hidden/read: %+v %v", p, err)
	}
	if _, err := a.InspectInbox(InboxPageOptions{Limit: 2, Before: "malformed"}); err == nil {
		t.Fatal("accepted malformed cursor")
	}
}

func TestInboxPageLoadsOnlySelectedRowsAndExactReadsDoNothing(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	old, id := strings.Repeat("a", 32), strings.Repeat("b", 32)
	if _, err := s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,target) VALUES(?,'peer/device',1,'message','older',1,'','invalid json'),(?,'peer/device',2,'task',?,2,'awaiting',NULL)`, old, id, strings.Repeat("exact body", 10000)); err != nil {
		t.Fatal(err)
	}
	p, err := a.InspectInbox(InboxPageOptions{Limit: 1})
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != id || p.Next == "" {
		t.Fatalf("bounded row load: %+v %v", p, err)
	}
	for _, review := range []bool{false, true} {
		p, err := a.InspectInbox(InboxPageOptions{Limit: 1, ID: id, Review: review})
		if err != nil || len(p.Items) != 1 || p.Items[0].Body != strings.Repeat("exact body", 10000) {
			t.Fatalf("exact body: %v", err)
		}
	}
	var unchanged bool
	if err := s.db.QueryRow(`SELECT read_at IS NULL AND state='awaiting' FROM inbox WHERE id=?`, id).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("inspection mutated message: %v", err)
	}
}

func TestInboxNoticePageKeepsExactPendingInvitation(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	p, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, lids[:1], nil, "bounded invitation note")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "pending invitation", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	page, err := w.bob.InspectInboxNotices("invites", InboxPageOptions{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].PID != p.PID || page.Items[0].Conv != conv {
		t.Fatalf("exact invitation metadata: %+v %v", page, err)
	}
	exact, err := w.bob.InspectInboxNotices("invites", InboxPageOptions{Limit: 1, ID: p.PID})
	if err != nil || len(exact.Items) != 1 || exact.Items[0].Detail != "bounded invitation note" || exact.Items[0].Content == nil {
		t.Fatalf("exact invitation: %+v %v", exact, err)
	}
	if got := stateAt(t, w.bob, p.PID).State; got != PartInvited {
		t.Fatalf("read decided invitation: %s", got)
	}
}
