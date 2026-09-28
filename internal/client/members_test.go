package client

import (
	"net/http"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func memberPresence(v MemberView) map[string]string {
	out := map[string]string{}
	for _, m := range v.Members.Members {
		out[m.Address] = m.Presence
	}
	return out
}

// trustRows counts what this agent has recorded about address: pinned key,
// approvals, task grants.
func trustRows(t *testing.T, a *Agent, address string) int {
	t.Helper()
	n := 0
	for _, q := range []string{
		`SELECT count(*) FROM peers WHERE address = ?`,
		`SELECT count(*) FROM approvals WHERE address = ?`,
		`SELECT count(*) FROM task_grants WHERE address = ?`,
	} {
		var c int
		if err := a.store.db.QueryRow(q, address).Scan(&c); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		n += c
	}
	return n
}

// A member who has just joined is discovered without knowing the address:
// the running daemon's list shows it (offline, then connected, then away),
// the Hub's list agrees, a revoked member disappears, and being listed
// records no key, approval or grant.
func TestMembersDiscoveredAndPushed(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	eventually(t, "bob's first list", func() bool {
		v := w.bob.MemberView()
		p := memberPresence(v)
		return v.Listed == MembersListed && v.Current && p[w.alice.Address] == protocol.PresenceConnected && p[w.bob.Address] == protocol.PresenceConnected
	})

	vit := mustJoin(t, t.TempDir(), w.aliceInvites("vitalii"), "desk")
	eventually(t, "the new member in bob's list", func() bool {
		return memberPresence(w.bob.MemberView())[vit.Address] == protocol.PresenceOffline
	})
	got, err := w.bob.Members(tctx(t))
	if err != nil || len(got.Members) != 3 || got.Members[0].Address != vit.Address || got.Truncated {
		t.Fatalf("Hub list: %+v %v", got, err)
	}
	if n := trustRows(t, w.bob, vit.Address); n != 0 {
		t.Fatalf("listing recorded %d trust rows for the new member", n)
	}

	stopVit := runAgent(t, vit)
	eventually(t, "the new member connected", func() bool {
		return memberPresence(w.bob.MemberView())[vit.Address] == protocol.PresenceConnected
	})
	stopVit()
	eventually(t, "the new member away", func() bool {
		return memberPresence(w.bob.MemberView())[vit.Address] == protocol.PresenceReconnecting
	})
	if err := w.alice.Revoke(tctx(t), vit.Address); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the revoked member gone", func() bool {
		_, listed := memberPresence(w.bob.MemberView())[vit.Address]
		return !listed
	})
	if _, err := vit.Members(tctx(t)); err == nil {
		t.Fatal("a revoked agent listed the members")
	}
	if n := trustRows(t, w.bob, vit.Address); n != 0 {
		t.Fatalf("the list recorded %d trust rows", n)
	}
}

// What the daemon shows follows the stream: nothing is known before it
// connects; a Hub without the header does not list members; a list is
// current only while its connection lasts and until something unreadable
// arrives, and an unreadable list never replaces the last good one.
func TestMemberViewFollowsStream(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	if v := a.MemberView(); v.Listed != MembersUnknown || v.Current || !v.At.IsZero() {
		t.Fatalf("before connecting: %+v", v)
	}
	seq0, _ := a.Changed()
	a.membersConnected(http.Header{})
	if v := a.MemberView(); v.Listed != MembersNotListed || v.Current {
		t.Fatalf("older Hub: %+v", v)
	}
	h := http.Header{}
	h.Set(protocol.MembersHeader, "1")
	a.membersConnected(h)
	if v := a.MemberView(); v.Listed != MembersListed || v.Current {
		t.Fatalf("connected, no list yet: %+v", v)
	}
	a.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5}],"truncated":false}`))
	v := a.MemberView()
	if !v.Current || v.At.IsZero() || memberPresence(v)["vitalii/desk"] != protocol.PresenceConnected {
		t.Fatalf("after a list: %+v", v)
	}
	at := v.At
	for _, bad := range []string{
		`{"members":[`,
		`{"members":[{"address":"no-slash","presence":"connected","joined":1}]}`,
		`{"members":[{"address":"a/b","presence":"here","joined":1}]}`,
		`{"members":[{"address":"a/b","presence":"offline"},{"address":"a/b","presence":"offline"}]}`,
	} {
		a.onMembers([]byte(bad))
		v := a.MemberView()
		if v.Current || !v.At.Equal(at) || memberPresence(v)["vitalii/desk"] != protocol.PresenceConnected {
			t.Fatalf("after %s: %+v", bad, v)
		}
		a.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5}]}`))
		at = a.MemberView().At
	}
	a.membersDisconnected()
	if v := a.MemberView(); v.Current || len(v.Members.Members) != 1 {
		t.Fatalf("after the stream closed: %+v", v)
	}
	if seq, _ := a.Changed(); seq <= seq0 {
		t.Fatal("changes not signalled")
	}
}
