package ui

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Keep the fixture's signed authority real while stopping its daemons before
// projection: no native worker runs and delivery cannot change between reads.
func dmProjectionFixture(t *testing.T) (*Live, *client.Agent, *sql.DB, client.GroupContext, string) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx := t.Context()
	hubDir := t.TempDir()
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	home := t.TempDir()
	alice, err := client.Join(ctx, home, testhub.BootstrapCode(t, hubDir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	join := func(name string) *client.Agent {
		t.Helper()
		code, e := alice.Invite(ctx, name, time.Hour, false)
		if e != nil {
			t.Fatal(e)
		}
		a, e := client.Join(ctx, t.TempDir(), code, name)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	bob, carol := join("desk"), join("guest")
	for _, a := range []*client.Agent{alice, bob, carol} {
		if _, err := a.CreatePerson(ctx, a.Address); err != nil {
			t.Fatal(err)
		}
	}
	stopA, stopB, stopC := runDaemon(t, alice), runDaemon(t, bob), runDaemon(t, carol)
	conv := liveTwoMemberGroup(t, alice, bob)
	wait := func(what string, fn func() bool) { waitFor(t, what, fn) }
	guest := inviteGuest(t, alice, carol, conv, wait)
	if _, err := bob.SendConv(ctx, conv, client.ConvOutgoing{Body: "Synthetic unread group message"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "group message at member", goHas(alice, conv, "Synthetic unread group message", nil))
	stopA()
	stopB()
	stopC()
	if _, err := alice.DismissParticipation(ctx, guest.PID); err != nil {
		t.Fatal(err)
	}
	packet, err := alice.GroupContext(conv)
	if err != nil {
		t.Fatal(err)
	}
	me, _, err := alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	key, err := identity.Load(filepath.Join(home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	member, _ := packet.State.Member(me.Person)
	storeEvent := func(ev protocol.ParticipationEvent) {
		t.Helper()
		ev.Sign(key.Sign)
		if err := ev.Verify(alice.Self().SignKey); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO participation_events(hash,conv,pid,type,author,event,received_at,prev) VALUES(?,?,?,?,?,?,?,?)`, ev.Hash(), conv, ev.PID, ev.Type, ev.Author.Fingerprint, string(raw), time.Now().Unix(), ev.Prev); err != nil {
			t.Fatal(err)
		}
	}
	author := protocol.EventAuthor{Person: me.Person, Roster: me.Roster, Address: me.Address, Fingerprint: me.Fingerprint, GroupAdmission: member.Admission.Hash()}
	for range 4 {
		invite := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(), Author: author,
			Host: &protocol.ParticipationHost{Person: me.Person, Address: me.Address, Fingerprint: me.Fingerprint, AgentID: protocol.NewID()}, Audience: protocol.AudienceConversation,
			TaskKeys: []string{me.Fingerprint}, Group: &protocol.ParticipationGroup{Seq: packet.State.Seq, Hash: packet.State.Hash(), HostRole: "member", HostAdmission: member.Admission.Hash(), TaskAdmissions: []string{member.Admission.Hash()}}}
		storeEvent(invite)
		// Sign the invite value too: storeEvent receives it by value.
		invite.Sign(key.Sign)
		storeEvent(protocol.ParticipationEvent{V: 1, Conv: conv, PID: invite.PID, Type: protocol.EventAccept, TS: time.Now().Unix(), Prev: invite.Hash(), Author: author})
	}
	// The recipient daemons are stopped; normalize already sealed end copies to
	// custody so the exact held-end projection stays deterministic.
	if _, err = db.Exec(`UPDATE outbox SET state='custody' WHERE conv=? AND pid=? AND sub='event' AND json_extract(body,'$.type')='dismiss'`, conv, guest.PID); err != nil {
		t.Fatal(err)
	}
	return NewLive(alice), alice, db, packet, guest.PID
}

func dmProjectionGET(t *testing.T, live *Live, conv string) (int, []byte) {
	t.Helper()
	srv := New(live, "127.0.0.1:12345", testToken)
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:12345/api/dm?id="+conv, nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func TestLiveDMProjectionFreshGroupHead(t *testing.T) {
	live, alice, _, packet, guestPID := dmProjectionFixture(t)
	status, body := dmProjectionGET(t, live, packet.State.Conv)
	if status != http.StatusOK {
		t.Fatalf("initial GET status=%d body=%s", status, body)
	}
	var first DMThread
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Agents) != 4 || len(first.Guests) != 1 || first.Guests[0].PID != guestPID || first.Guests[0].State != client.PartDismissed || first.Guests[0].CanSend || first.Guests[0].CanEnd || !first.Guests[0].AudiencePending || !first.AudiencePending {
		t.Fatalf("lost exact participant/held-end projection: agents=%+v guests=%+v pending=%v", first.Agents, first.Guests, first.AudiencePending)
	}
	for _, agent := range first.Agents {
		if agent.State != client.PartActive || agent.Held != 0 {
			t.Fatalf("agent did not resolve active signed state: %+v", agent)
		}
	}
	unread := false
	for _, m := range first.Messages {
		unread = unread || m.Unread && m.Body == "Synthetic unread group message"
	}
	if !unread {
		t.Fatal("GET lost unread message state")
	}
	if err := alice.NoteGroupHead(protocol.GroupHead{Bootstrap: packet.Root.Creator.Fingerprint, Conv: packet.State.Conv, Seq: packet.State.Seq + 1, Hash: strings.Repeat("e", 64)}); err != nil {
		t.Fatal(err)
	}
	status, body = dmProjectionGET(t, live, packet.State.Conv)
	if status == http.StatusOK {
		t.Fatalf("later GET reused successful projection after a newer head froze current context: %s", body)
	}
}
