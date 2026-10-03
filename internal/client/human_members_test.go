package client

import (
	"slices"
	"testing"
)

// A pending guest sees both verified original people before deciding, not
// the stored peer or its inviter alone; members' own views are unchanged.
func TestHumanGuestSeesBothOriginalsBeforeConsent(t *testing.T) {
	w, carol, conv, _, _ := humanWorld(t)
	p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "same DM")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "human invite", func() bool { return stateAt(t, carol, p.PID).State == PartInvited })
	var want []string
	for _, a := range []*Agent{w.alice, w.bob} {
		me, ok, err := a.Person()
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		want = append(want, me.Person)
	}
	slices.Sort(want)
	view := func(a *Agent) ConversationInfo {
		t.Helper()
		rows, err := a.Conversations()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range rows {
			if c.ID == conv {
				return c
			}
		}
		t.Fatal("conversation not listed")
		return ConversationInfo{}
	}
	guest := view(carol)
	var got []string
	for _, m := range guest.Members {
		got = append(got, m.Person)
	}
	slices.Sort(got)
	if guest.Role != "visitor" || !slices.Equal(got, want) {
		t.Fatalf("guest view role %q members %v, want both originals %v", guest.Role, got, want)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		if c := view(a); c.Role != "member" || len(c.Members) != 0 {
			t.Fatalf("member view changed: role %q members %v", c.Role, c.Members)
		}
	}
}
