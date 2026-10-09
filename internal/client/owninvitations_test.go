package client

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOwnInvitationVisibleOnLinkedDevice(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	stop := runAgent(t, w.alice)
	phone := linked(t, w.alice)
	stop()
	inv := groupLifecycleInvite(t, w, p, nil)
	w.alice.convWork.due(convHistory)
	w.alice.convSync(tctx(t))
	rows, err := w.alice.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='invitation-sync' ORDER BY rowid`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	var carriers []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var e envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		carriers = append(carriers, e)
	}
	rows.Close()
	for _, e := range carriers {
		if err = phone.verifyAndStore(tctx(t), e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := phone.GroupInvitations()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range got {
		if g.ID == inv.ID && g.State == "pending" && g.Direction == "out" {
			if g.CanCancel || g.CanRefresh {
				t.Fatal("mirrored intent grants local actions")
			}
			var n int
			phone.store.db.QueryRow(`SELECT count(*) FROM group_invitations WHERE id=?`, inv.ID).Scan(&n)
			if n != 0 {
				t.Fatal("mirror became executable invitation intent")
			}
			testOwnInvitationUpdates(t, w, phone, inv, carriers[0])
			return
		}
	}
	t.Fatalf("own pending invitation absent on linked device: got %d rows", len(got))
}

func testOwnInvitationUpdates(t *testing.T, w *world, phone *Agent, inv GroupInvitationInfo, first envelope.Envelope) {
	t.Helper()
	if first.Attn {
		t.Fatal("inert view requested notification")
	}
	if err := phone.CancelGroupInvitation(tctx(t), inv.ID); err == nil {
		t.Fatal("mirror cancelled source intent")
	}
	if err := w.alice.CancelGroupInvitation(tctx(t), inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.syncInvitations(); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub='invitation-sync' ORDER BY rowid DESC LIMIT 1`, phone.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var latest envelope.Envelope
	if err := json.Unmarshal(raw, &latest); err != nil {
		t.Fatal(err)
	}
	if err := phone.verifyAndStore(tctx(t), latest); err != nil {
		t.Fatal(err)
	}
	// New envelope ID carrying the earlier revision must not restore pending.
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	r := protocol.InvitationSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, ID: inv.ID, Revision: 1, Status: "pending", Proposal: inv.Proposal}
	receive := func(sender *Agent, r protocol.InvitationSync) envelope.Envelope {
		t.Helper()
		body, _ := json.Marshal(r)
		e := craft(t, sender, phone, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubInvitationSync, Replica: true, Body: string(body)})
		if err := phone.verifyAndStore(tctx(t), e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	receive(w.alice, r)
	rows, err := phone.GroupInvitations()
	if err != nil || len(rows) != 1 || rows[0].State != "cancelled" {
		t.Fatalf("reordered pending overwrote cancellation: %+v %v", rows, err)
	}
	r.Revision = 2
	r.Status = "accepted"
	conflict := receive(w.alice, r)
	if heldReason(t, phone, conflict.ID) != reasonInvalid {
		t.Fatal("conflicting same revision accepted")
	}
	r.Revision = 3
	foreign := receive(w.bob, r)
	if heldReason(t, phone, foreign.ID) != reasonInvalid {
		t.Fatal("foreign person's invitation view accepted")
	}
	if n := count(t, phone, "group_invitations"); n != 0 {
		t.Fatal("mirror imported local intent", n)
	}
	if err = w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err = again.syncInvitations(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, again, "outbox WHERE sub='invitation-sync'"); n != 2 {
		t.Fatal("restart duplicate", n)
	}
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	if err = again.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil {
		t.Fatal(err)
	}
	publish := func(ts int64, caps []string) {
		t.Helper()
		slices.Sort(caps)
		r := protocol.CapsRecord{Address: phone.Address, Session: profile.Sessions[0], TS: ts, Caps: caps}
		r.Sign(phone.id.Sign)
		if err := phone.hub.do(tctx(t), "PUT", "/v1/caps", r, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish(time.Now().Unix()+100, []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapRoom})
	if handled, allowed, err := again.mayDeliverInvitationSync(latest); err != nil || !handled || allowed {
		t.Fatalf("old reader allowed %v %v %v", handled, allowed, err)
	}
	own := slices.DeleteFunc(slices.Clone(ownCaps), func(c string) bool { return c == protocol.CapConvClear || c == protocol.CapOwnSyncV2 })
	publish(time.Now().Unix()+200, append(own, protocol.CapOwnSyncV2))
	features, err := again.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	again.releaseConv(tctx(t), features)
	if handled, allowed, err := again.mayDeliverInvitationSync(latest); err != nil || !handled || !allowed {
		t.Fatalf("upgraded reader stays held %v %v %v", handled, allowed, err)
	}
	transport := again.hub.http.Transport
	changed := false
	again.hub.http.Transport = workspaceRealmTransport(func(req *http.Request) (*http.Response, error) {
		response, e := transport.RoundTrip(req)
		if e == nil && strings.HasSuffix(req.URL.Path, "/profile") && !changed {
			changed = true
			_, e = again.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address)
		}
		return response, e
	})
	defer func() { again.hub.http.Transport = transport }()

	if _, allowed, err := again.mayDeliverInvitationSync(latest); err != nil || allowed {
		t.Fatal("key changed during capability lookup was allowed", err)
	}
	if !changed {
		t.Fatal("capability race hook did not run")
	}
	if _, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE state=?`, personConflict, personSelf); err != nil {
		t.Fatal(err)
	}
	hidden, err := phone.GroupInvitations()
	if err != nil || len(hidden) != 0 {
		t.Fatal("conflicted own person retained trusted view", err)
	}
}
