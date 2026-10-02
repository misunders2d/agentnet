package client

import "testing"

func TestGroupHistoryLifecycleCarriersNotTurns(t *testing.T) {
	w, p := groupLifecycleFixture(t, true)
	invitation := groupLifecycleInvite(t, w, p, nil)
	awaitGroupInvitation(t, w.bob, invitation.ID, "pending")
	if err := w.bob.DecideGroupInvitation(tctx(t), invitation.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "accepted current context", func() bool { got, e := w.bob.GroupContext(p.State.Conv); return e == nil && got.State.Seq == 1 })
	for _, a := range []*Agent{w.alice, w.bob} {
		turns, err := a.ConversationMessages(p.State.Conv)
		if err != nil || len(turns) != 0 {
			t.Fatalf("lifecycle carriers leaked timeline %+v %v", turns, err)
		}
		unread, err := a.ConvUnread()
		if err != nil || len(unread[p.State.Conv]) != 0 {
			t.Fatalf("lifecycle carriers unread %+v %v", unread, err)
		}
		var n int
		if err = a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND sub IN('group-invite','group-consent')`, p.State.Conv).Scan(&n); err != nil || n != 1 {
			t.Fatalf("durable lifecycle record lost %d %v", n, err)
		}
	}
}
