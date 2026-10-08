package client

import (
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
	"testing"
)

func TestGroupDeclineStaleOffline(t *testing.T) {
	for _, mode := range []string{"fresh", "stale", "roster"} {
		t.Run(mode, func(t *testing.T) {
			w, p := groupLifecycleFixture(t, false)
			inv := groupLifecycleInvite(t, w, p, nil)
			deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
			deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
			if mode == "stale" {
				for _, entry := range []struct {
					a         *Agent
					direction string
				}{{w.alice, "out"}, {w.bob, "in"}} {
					if _, e := entry.a.store.db.Exec(`UPDATE group_invitations SET state='stale' WHERE id=? AND direction=?`, inv.ID, entry.direction); e != nil {
						t.Fatal(e)
					}
				}
			}
			if mode == "roster" {
				if _, e := w.bob.RenamePerson(tctx(t), "Bob changed roster"); e != nil {
					t.Fatal(e)
				}
				self, _, _ := w.bob.store.selfPerson(w.bob.Address)
				if _, e := w.alice.refreshPerson(tctx(t), self.roster.Person, true); e != nil {
					t.Fatal(e)
				}
			}
			if mode != "fresh" {
				if e := w.bob.DecideGroupInvitation(tctx(t), inv.ID, true); e == nil {
					t.Fatal("stale acceptance allowed")
				}
			}
			w.hub.Stop()
			if e := w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); e != nil {
				t.Fatalf("offline decline: %v", e)
			}
			if e := w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); e != nil {
				t.Fatal(e)
			}
			row, e := groupInvitationIn(w.bob.store.db, inv.ID, "in")
			if e != nil || row.State != "declined" || len(row.consent) == 0 {
				t.Fatalf("decline missing: %#v %v", row, e)
			}
			assertNoGroupJoin(t, w.bob, p.Root.ID())
			home := w.bob.home
			w.bob.Close()
			w.bob, e = Open(home)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { w.bob.Close() })
			if e = w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); e != nil {
				t.Fatal(e)
			}
			w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
			deliverGroupLifecycleSubtype(t, w.bob, w.alice, envelope.SubGroupConsent)
			out, e := groupInvitationIn(w.alice.store.db, inv.ID, "out")
			if e != nil || out.State != "declined" {
				t.Fatalf("exact inviter decline missing: %s %v", out.State, e)
			}
			all, e := w.alice.GroupInvitations()
			if e != nil || len(all) != 1 {
				t.Fatalf("decline reissued invitation: %d %v", len(all), e)
			}
			assertNoGroupJoin(t, w.bob, p.Root.ID())
		})
	}
}

func TestGroupDeclineSignerAndMissingInviter(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupProof)
	deliverGroupLifecycleSubtype(t, w.alice, w.bob, envelope.SubGroupInvite)
	address := w.bob.Address
	w.bob.Address = "removed/device"
	if e := w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); e == nil {
		t.Fatal("inactive signer declined")
	}
	w.bob.Address = address
	if _, e := w.bob.store.db.Exec(`UPDATE persons SET state='conflict' WHERE state='self'`); e != nil {
		t.Fatal(e)
	}
	if e := w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); e == nil {
		t.Fatal("frozen signer declined")
	}
	if _, e := w.bob.store.db.Exec(`UPDATE persons SET state='self' WHERE state='conflict'`); e != nil {
		t.Fatal(e)
	}
	if _, e := w.bob.store.db.Exec(`DELETE FROM peers WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	w.hub.Stop()
	if e := w.bob.DecideGroupInvitation(tctx(t), inv.ID, false); e != nil {
		t.Fatal(e)
	}
	var n int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM group_invitation_copies WHERE direction='in' AND invitation=?`, inv.ID).Scan(&n)
	if n != 0 {
		t.Fatal("missing inviter key invented delivery")
	}
	assertNoGroupJoin(t, w.bob, p.Root.ID())
}
