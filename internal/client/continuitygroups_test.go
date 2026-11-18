package client

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// S6e: a group send checks only its own group's recovered devices, and only
// when the continuity inputs or the group's context changed. A recovered
// device still gets the group context queued ahead of the turn, also when its
// person changes while sends are running.
func TestGroupSendSyncsOnlyItsGroupWhenContinuityChanged(t *testing.T) {
	w := newMEL736OneWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	w.ctx = ctx
	oldDevice := mel736OneJoin(t, w, "lost")
	peer := w.admin
	if _, e := peer.CreatePerson(ctx, "Colleague"); e != nil {
		t.Fatal(e)
	}
	stopOld := runAgent(t, oldDevice)
	stopPeer := runAgent(t, peer)
	waitNamedAgentCaps(t, oldDevice)
	waitNamedAgentCaps(t, peer)
	old, _, _ := oldDevice.store.selfPerson(oldDevice.Address)
	me, _, _ := peer.store.selfPerson(peer.Address)
	realm, _ := peer.RealmID()
	group := func(title string) string {
		root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: protocol.ConvCreator{Person: me.roster.Person, Roster: me.roster.Hash(), Address: peer.Address, Fingerprint: peer.Self().Fingerprint()}, Members: []protocol.ConvMember{{Person: old.roster.Person, Roster: old.roster.Hash()}, {Person: me.roster.Person, Roster: me.roster.Hash()}}, Nonce: protocol.NewID(), Created: time.Now().Unix(), Realm: realm, Title: title, Admins: []string{me.roster.Person}}
		slices.SortFunc(root.Members, func(a, b protocol.ConvMember) int { return strings.Compare(a.Person, b.Person) })
		root.Sign(peer.id.Sign)
		state := protocol.GroupState{V: 1, Conv: root.ID(), Realm: realm, Title: root.Title}
		for _, m := range root.Members {
			by := peer
			if m.Person == old.roster.Person {
				by = oldDevice
			}
			ad, e := by.SignGroupAdmission(root, 0, "", nil)
			if e != nil {
				t.Fatal(e)
			}
			state.Members = append(state.Members, protocol.GroupMember{ConvMember: m, Admin: by == peer, Admission: ad})
		}
		packet, e := peer.SignGroupState(ctx, GroupContext{Root: root, State: state})
		if e != nil {
			t.Fatal(e)
		}
		commit, e := peer.BuildGroupCommit(ctx, packet)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = peer.PublishGroup(ctx, commit, packet); e != nil {
			t.Fatal(e)
		}
		eventually(t, title+" context at the old device", func() bool { _, e := oldDevice.GroupContext(root.ID()); return e == nil })
		return root.ID()
	}
	sent, other := group("Sent group"), group("Other group")
	// The peer's daemon would check every group in the background; the send
	// path alone is under test from here.
	stopPeer()
	stopOld()
	if e := peer.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: old.roster.Email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e := peer.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: old.roster.Email}); e != nil {
		t.Fatal(e)
	}
	fresh := mel736OneJoin(t, w, "new")
	runAgent(t, fresh)
	waitNamedAgentCaps(t, fresh)
	next, _, _ := fresh.Person()
	if _, e := peer.refreshPerson(ctx, next.Person, false); e != nil {
		t.Fatal(e)
	}
	if e := peer.refreshContinuity(ctx); e != nil {
		t.Fatal(e)
	}

	// carriersBefore: the group's context is queued to device ahead of the
	// turn's copy to it; none is queued for another group.
	carriers := func(conv, device string) []int64 {
		rows, e := peer.store.db.Query(`SELECT rowid FROM outbox WHERE conv=? AND recipient=? AND sub IN ('group-proof','group-context') ORDER BY rowid`, conv, device)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}
	carriersBefore := func(turn ConvSent, device string) {
		t.Helper()
		var copyRow int64
		if e := peer.store.db.QueryRow(`SELECT rowid FROM outbox WHERE conv=? AND recipient=? AND lid=?`, sent, device, turn.LID).Scan(&copyRow); e != nil {
			t.Fatalf("no turn copy to %s: %v", device, e)
		}
		ids := carriers(sent, device)
		if len(ids) == 0 || ids[len(ids)-1] > copyRow {
			t.Fatalf("group context for %s %v not queued before the turn copy %d", device, ids, copyRow)
		}
	}

	// A recovered device without the context: the send makes its copies,
	// which takes the spool lock, and goes once the lock is free.
	release := holdSpool(t, peer)
	done := make(chan error, 1)
	var first ConvSent
	go func() {
		var e error
		first, e = peer.SendConv(context.Background(), sent, ConvOutgoing{Body: "after recovery"})
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatalf("context copies were made without the spool lock: %v", e)
	case <-time.After(time.Second):
	}
	release()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	carriersBefore(first, fresh.Address)
	if ids := carriers(other, fresh.Address); len(ids) != 0 {
		t.Fatalf("a send in one group queued another group's context: %v", ids)
	}

	// Nothing changed: the next text send neither checks nor waits.
	release = holdSpool(t, peer)
	if _, e := sendWithin(t, peer, sent, ConvOutgoing{Body: "nothing changed"}, 5*time.Second); e != nil {
		t.Fatal(e)
	}
	before := len(carriers(sent, fresh.Address))
	release()

	// The recovered person links a phone while sends are running: a send
	// after the change still queues the context to it ahead of its turn.
	phone := mel736OneJoin(t, w, "phone")
	request := pendingLink(t, fresh)
	if e := fresh.DecideLink(ctx, request.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, e := phone.finishLink(ctx); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var sendErr error
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, e := peer.SendConv(context.Background(), sent, ConvOutgoing{Body: "during the change"}); e != nil && sendErr == nil {
				sendErr = e
			}
		}
	})
	_, e := peer.refreshPerson(ctx, next.Person, false)
	close(stop)
	wg.Wait()
	if e != nil {
		t.Fatal(e)
	}
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	last, e := peer.SendConv(ctx, sent, ConvOutgoing{Body: "after the change"})
	if e != nil {
		t.Fatal(e)
	}
	carriersBefore(last, phone.Address)
	if len(carriers(sent, fresh.Address)) != before {
		t.Fatal("a device that had the context got it again")
	}
	if ids := carriers(other, phone.Address); len(ids) != 0 {
		t.Fatalf("another group's context queued: %v", ids)
	}

	// A context copy that lapses (here: failed at the Hub) is queued again
	// by the group's next send, as before the check was skipped.
	if _, e = peer.store.db.Exec(`UPDATE outbox SET state=? WHERE conv=? AND recipient=? AND sub IN ('group-proof','group-context')`, stateFailed, sent, phone.Address); e != nil {
		t.Fatal(e)
	}
	again, e := peer.SendConv(ctx, sent, ConvOutgoing{Body: "after a lapse"})
	if e != nil {
		t.Fatal(e)
	}
	var live int
	if e = peer.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND recipient=? AND sub IN ('group-proof','group-context') AND state<>?`, sent, phone.Address, stateFailed).Scan(&live); e != nil {
		t.Fatal(e)
	}
	if live == 0 {
		t.Fatal("a lapsed context copy was not queued again")
	}
	carriersBefore(again, phone.Address)
}
