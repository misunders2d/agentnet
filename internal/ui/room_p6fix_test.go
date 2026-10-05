package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Seed the signed legacy records that an upgraded installation already holds.
// Public APIs deliberately prevent creating these duplicate invitations now.
func TestP6FixLegacyDuplicatesRemoveTogether(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	ah, bh := filepath.Join(t.TempDir(), "alice"), filepath.Join(t.TempDir(), "bob")
	a, err := client.Join(ctx, ah, testhub.BootstrapCode(t, hub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	code, err := a.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Join(ctx, bh, code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	aInfo, err := a.CreatePerson(ctx, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	bInfo, err := b.CreatePerson(ctx, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	stopA, stopB := runDaemon(t, a), runDaemon(t, b)
	defer stopA()
	defer stopB()
	wait := func(name string, fn func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !fn(); time.Sleep(25 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("timeout: " + name)
			}
		}
	}
	packet, err := a.CreateGroup(ctx, "Legacy duplicates")
	if err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	var invitation client.GroupInvitationInfo
	wait("invite member", func() bool { invitation, err = a.InviteGroup(ctx, conv, bInfo.Person, nil); return err == nil })
	wait("incoming invitation", func() bool {
		rows, e := b.GroupInvitations()
		if e != nil {
			return false
		}
		for _, r := range rows {
			if r.ID == invitation.ID {
				return true
			}
		}
		return false
	})
	if err = b.DecideGroupInvitation(ctx, invitation.ID, true); err != nil {
		t.Fatal(err)
	}
	wait("published membership", func() bool { packet, err = a.GroupContext(conv); return err == nil && len(packet.State.Members) == 2 })
	var lids []string
	for _, body := range []string{"first shared", "second shared"} {
		r, e := a.SendConv(ctx, conv, client.ConvOutgoing{Body: body})
		if e != nil {
			t.Fatal(e)
		}
		lids = append(lids, r.LID)
	}
	stopA()
	stopB()
	aKey, err := identity.Load(filepath.Join(ah, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	bKey, err := identity.Load(filepath.Join(bh, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(ah, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := func(ev protocol.ParticipationEvent) {
		t.Helper()
		raw, e := json.Marshal(ev)
		if e != nil {
			t.Fatal(e)
		}
		_, e = db.Exec(`INSERT INTO participation_events(hash,conv,pid,type,author,event,received_at,prev) VALUES(?,?,?,?,?,?,?,?)`, ev.Hash(), conv, ev.PID, ev.Type, ev.Author.Fingerprint, string(raw), time.Now().Unix(), ev.Prev)
		if e != nil {
			t.Fatal(e)
		}
	}
	var ids []string
	missingRef := protocol.NewID()
	for i, person := range []client.PersonInfo{aInfo, bInfo} {
		m, _ := packet.State.Member(person.Person)
		hm, _ := packet.State.Member(aInfo.Person)
		refs := []protocol.GrantRef{{LID: lids[0], Fingerprint: aInfo.Fingerprint}, {LID: missingRef, Fingerprint: aInfo.Fingerprint}}
		if i == 1 {
			refs = append(refs, protocol.GrantRef{LID: lids[1], Fingerprint: aInfo.Fingerprint})
		}
		// Both entries name the same unavailable selection: count it once.
		inv := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix() - int64(2-i), Author: protocol.EventAuthor{Person: person.Person, Roster: person.Roster, Address: person.Address, Fingerprint: person.Fingerprint, GroupAdmission: m.Admission.Hash()}, Host: &protocol.ParticipationHost{Person: aInfo.Person, Address: aInfo.Address, Fingerprint: aInfo.Fingerprint}, Audience: protocol.AudienceConversation, Grant: refs, TaskKeys: []string{person.Fingerprint}, Group: &protocol.ParticipationGroup{Seq: packet.State.Seq, Hash: packet.State.Hash(), HostRole: "member", HostAdmission: hm.Admission.Hash(), TaskAdmissions: []string{m.Admission.Hash()}}}
		key := aKey
		if i == 1 {
			key = bKey
		}
		inv.Sign(key.Sign)
		store(inv)
		accept := protocol.ParticipationEvent{V: 1, Conv: conv, PID: inv.PID, Type: protocol.EventAccept, Prev: inv.Hash(), TS: time.Now().Unix(), Author: protocol.EventAuthor{Person: aInfo.Person, Roster: aInfo.Roster, Address: aInfo.Address, Fingerprint: aInfo.Fingerprint, GroupAdmission: hm.Admission.Hash()}}
		accept.Sign(aKey.Sign)
		store(accept)
		ids = append(ids, inv.PID)
	}
	live := NewLive(a)
	thread, err := live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread.Agents) != 1 {
		t.Fatalf("duplicate cards: %+v", thread.Agents)
	}
	v := thread.Agents[0]
	if len(v.PIDs) != 2 || len(v.Inviters) != 2 || len(v.Shared) != 2 || v.Missing != 1 || len(v.TasksFrom) != 2 {
		t.Fatalf("lost duplicate data: %+v", v)
	}
	if _, err = live.DismissAgent(ids[0]); err != nil {
		t.Fatal(err)
	}
	thread, err = live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range thread.Agents {
		if v.State == client.PartActive || v.State == client.PartInvited {
			t.Fatalf("still active after removal: %+v", v)
		}
	}
}
