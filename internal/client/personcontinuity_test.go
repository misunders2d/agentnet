package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func mel736OneRoster(t *testing.T, id, name string) protocol.PersonRoster {
	t.Helper()
	k, e := identity.Generate()
	if e != nil {
		t.Fatal(e)
	}
	r := protocol.PersonRoster{Person: id, Label: "Bohdan", Email: "bohdan@mellanni.com", Devices: []identity.Public{k.Public("bohdan/" + name)}}
	r.Sign(k.Sign)
	return r
}
func mel736OnePin(t *testing.T, s *store, r protocol.PersonRoster) {
	t.Helper()
	raw, _ := json.Marshal(r)
	if _, e := s.pinChain(r.Person, [][]byte{raw}, identity.Public{}, false); e != nil {
		t.Fatal(e)
	}
}
func mel736OneLeave(t *testing.T, s *store, r protocol.PersonRoster, complete bool) {
	t.Helper()
	for _, d := range r.Devices {
		if e := s.endRecipient(d.Address, d.Fingerprint(), true); e != nil {
			t.Fatal(e)
		}
	}
	if complete {
		if e := s.setConfig(map[string]string{continuityHeadPrefix + r.Person: r.Hash()}); e != nil {
			t.Fatal(e)
		}
	}
}
func TestMEL736OneBohdanResolverRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	seedFixtureStore(t, filepath.Dir(path))
	s, e := openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	old := mel736OneRoster(t, "faeb3e71bb3aac661d198083239baa6f", "lost")
	next := mel736OneRoster(t, "59c89e536113a047e24569888bcc106a", "new")
	mel736OnePin(t, s, old)
	mel736OneLeave(t, s, old, true)
	mel736OnePin(t, s, next)
	got, e := s.logicalPerson(old.Person)
	if e != nil || got != next.Person {
		t.Fatal(got, e)
	}
	if e = s.db.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	got, e = s.logicalPerson(old.Person)
	if e != nil || got != next.Person {
		t.Fatal("restart", got, e)
	}
	third := mel736OneRoster(t, protocol.NewID(), "third")
	mel736OneLeave(t, s, next, true)
	mel736OnePin(t, s, third)
	for _, id := range []string{old.Person, next.Person} {
		got, e = s.logicalPerson(id)
		if e != nil || got != third.Person {
			t.Fatal("terminal", got, e)
		}
	}
}
func TestMEL736OneIncompleteHeadFailsClosed(t *testing.T) {
	home := t.TempDir()
	seedFixtureStore(t, home)
	s, e := openStore(filepath.Join(home, "agent.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	old := mel736OneRoster(t, protocol.NewID(), "lost")
	next := mel736OneRoster(t, protocol.NewID(), "new")
	mel736OnePin(t, s, old)
	mel736OneLeave(t, s, old, false)
	mel736OnePin(t, s, next)
	got, e := s.logicalPerson(old.Person)
	if e != nil || got != old.Person {
		t.Fatal("incomplete head aliased", got, e)
	}
}

func TestMEL736OneBohdanTimelinesAndReplies(t *testing.T) {
	home := t.TempDir()
	seedFixtureStore(t, home)
	s, e := openStore(filepath.Join(home, "agent.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	old := mel736OneRoster(t, "faeb3e71bb3aac661d198083239baa6f", "lost")
	next := mel736OneRoster(t, "59c89e536113a047e24569888bcc106a", "new")
	mel736OnePin(t, s, old)
	mel736OneLeave(t, s, old, true)
	mel736OnePin(t, s, next)
	a := &Agent{store: s, Address: "peer/laptop"}
	me := protocol.NewID()
	var roots []protocol.ConvRoot
	for i, p := range []protocol.PersonRoster{old, next} {
		r := protocol.ConvRoot{V: 1, Kind: protocol.ConvKindDM, Created: int64(10 + i), Nonce: protocol.NewID(), Members: []protocol.ConvMember{{Person: me, Roster: old.Hash()}, {Person: p.Person, Roster: p.Hash()}}}
		raw, _ := json.Marshal(r)
		if e = s.addConversation(r, raw, p.Person); e != nil {
			t.Fatal(e)
		}
		roots = append(roots, r)
	}
	ids := []string{protocol.NewID(), protocol.NewID()}
	lids := []string{protocol.NewID(), protocol.NewID()}
	for i, r := range roots {
		_, e = s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,received_ms,conv,lid,read_at) VALUES(?, 'bohdan/new',1,'message',?,1,?, ?, ?,?)`, ids[i], []string{"old history", "new history"}[i], 1000+i, r.ID(), lids[i], []any{int64(1), nil}[i])
		if e != nil {
			t.Fatal(e)
		}
	}
	family, e := a.continuityDMs(roots[1].ID())
	if e != nil || len(family) != 2 {
		t.Fatal(family, e)
	}
	parent, known, e := a.continuityReplyParent(roots[1].ID(), lids[0])
	if e != nil || !known || parent != lids[0] {
		t.Fatal("cross-root logical reply", parent, known, e)
	}
	if got, e := a.currentDM(context.Background(), roots[0].ID()); e != nil || got != roots[1].ID() {
		t.Fatal("routing root", got, e)
	}
	for range 2 {
		notices, e := a.continuityNotices(roots[0].ID(), family)
		if e != nil || len(notices) != 1 || notices[0].Body != "Bohdan signed in again" {
			t.Fatal(notices, e)
		}
	}
	var count int
	s.db.QueryRow(`SELECT count(*) FROM inbox WHERE sub='continuity'`).Scan(&count)
	if count != 1 {
		t.Fatal("notice duplicated", count)
	}
	// Original rows and their read state never move or change.
	var c string
	var read sql.NullInt64
	s.db.QueryRow(`SELECT conv,read_at FROM inbox WHERE id=?`, ids[0]).Scan(&c, &read)
	if c != roots[0].ID() || !read.Valid {
		t.Fatal("old row changed", c, read)
	}
	s.db.QueryRow(`SELECT read_at FROM inbox WHERE id=?`, ids[1]).Scan(&read)
	if read.Valid {
		t.Fatal("new unread lost")
	}
	exact, _, e := humanReplyParent(s.db, roots[1].ID(), lids[0])
	if e == nil || exact != "" {
		t.Fatal("native exact scope inherited", exact, e)
	}
}

func TestMEL736OneAmbiguousAndLiveFailClosed(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			home := t.TempDir()
			seedFixtureStore(t, home)
			s, e := openStore(filepath.Join(home, "agent.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer s.db.Close()
			old := mel736OneRoster(t, protocol.NewID(), "old")
			next := mel736OneRoster(t, protocol.NewID(), "new")
			mel736OnePin(t, s, old)
			if complete {
				mel736OneLeave(t, s, old, true)
				mel736OnePin(t, s, next)
			} else {
				if e = s.setConfig(map[string]string{continuityHeadPrefix + old.Person: old.Hash()}); e != nil {
					t.Fatal(e)
				}
				raw, _ := json.Marshal(next)
				if _, e = s.pinChain(next.Person, [][]byte{raw}, identity.Public{}, false); !errors.Is(e, errPersonConflict) {
					t.Fatal("live old holder did not block", e)
				}
			}
			if complete {
				third := mel736OneRoster(t, protocol.NewID(), "third")
				raw, _ := json.Marshal(third)
				_, e = s.db.Exec(`INSERT INTO persons(person,label,seq,hash,record,state,pinned_at) VALUES(?,?,0,?,?,?,0)`, third.Person, third.Label, third.Hash(), string(raw), personPinned)
				if e != nil {
					t.Fatal(e)
				}
			}
			aliases, e := logicalPersons(s.db)
			if e != nil || aliases[old.Person] != old.Person {
				t.Fatal("ambiguous/incomplete aliased", aliases, e)
			}
		})
	}
}

func TestMEL736OneGroupMembershipRemovalAndDedupe(t *testing.T) {
	home := t.TempDir()
	seedFixtureStore(t, home)
	s, e := openStore(filepath.Join(home, "agent.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	old := mel736OneRoster(t, protocol.NewID(), "old")
	next := mel736OneRoster(t, protocol.NewID(), "new")
	mel736OnePin(t, s, old)
	mel736OneLeave(t, s, old, true)
	mel736OnePin(t, s, next)
	member := func(p protocol.PersonRoster) protocol.GroupMember {
		return protocol.GroupMember{ConvMember: protocol.ConvMember{Person: p.Person, Roster: p.Hash()}, Admission: protocol.GroupAdmission{Person: p.Person, Roster: p.Hash()}}
	}
	packet := GroupContext{State: protocol.GroupState{Conv: protocol.NewID(), Members: []protocol.GroupMember{member(old)}}}
	ms, e := logicalGroupMembers(s.db, packet)
	if e != nil || len(ms) != 1 {
		t.Fatal(ms, e)
	}
	p, ok, e := messagingPersonIn(s.db, ms[0].Person)
	if e != nil || !ok || p.info.Person != next.Person {
		t.Fatal(p, ok, e)
	}
	packet.State.Members = append(packet.State.Members, member(next))
	ms, e = logicalGroupMembers(s.db, packet)
	if e != nil || len(ms) != 1 || ms[0].Person != next.Person {
		t.Fatal("restored duplicate", ms, e)
	}
	original := packet.State.Members
	packet.Withdrawals = []protocol.GroupWithdrawal{{Conv: packet.State.Conv, Realm: packet.State.Realm, Person: next.Person, Admission: original[1].Admission.Hash()}}
	ms, e = logicalGroupMembers(s.db, packet)
	if e != nil || len(ms) != 0 {
		t.Fatal("withdrawn terminal resurrected old duplicate", ms, e)
	}
	packet.Withdrawals = nil
	packet.Proof = []protocol.GroupState{packet.State}
	packet.State.Members = []protocol.GroupMember{original[0]}
	ms, e = logicalGroupMembers(s.db, packet)
	if e != nil || len(ms) != 0 {
		t.Fatal("removed terminal resurrected old duplicate", ms, e)
	}
	packet.Proof = nil
	// Removing the current slot does not resurrect a removed old slot.
	packet.State.Members = nil
	ms, e = logicalGroupMembers(s.db, packet)
	if e != nil || len(ms) != 0 {
		t.Fatal("removed resurrected", ms, e)
	}
}

func TestMEL736OneHubRecoveryDMAndNoGrantInheritance(t *testing.T) {
	w := newMEL736OneWorld(t)
	first := mel736OneJoin(t, w, "lost")
	peer := w.admin
	if _, e := peer.CreatePerson(w.ctx, "Colleague"); e != nil {
		t.Fatal(e)
	}
	stopOld := runAgent(t, first)
	runAgent(t, peer)
	waitNamedAgentCaps(t, first)
	waitNamedAgentCaps(t, peer)
	old, _, _ := first.Person()
	conv, e := peer.CreateDM(w.ctx, first.Address)
	if e != nil {
		t.Fatal(e)
	}
	sent, e := peer.SendConv(w.ctx, conv, ConvOutgoing{Body: "before recovery"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "old DM delivered", func() bool { ms, _ := first.ConversationMessages(conv); return len(ms) > 0 })
	if _, e = peer.GrantTasks(old.Person); e != nil {
		t.Fatal(e)
	}
	stopOld()
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email}); e != nil {
		t.Fatal(e)
	}
	fresh := mel736OneJoin(t, w, "new")
	runAgent(t, fresh)
	waitNamedAgentCaps(t, fresh)
	next, _, _ := fresh.Person()
	if _, e = peer.refreshPerson(w.ctx, next.Person, false); e != nil {
		t.Fatal(e)
	}
	if to, e := peer.store.logicalPerson(old.Person); e != nil || to != next.Person {
		t.Fatal("Hub evidence", to, e)
	}
	if granted, e := taskGranted(peer.store.db, fresh.Address, fresh.Self().Fingerprint()); e != nil || granted {
		t.Fatal("inherited native grant", granted, e)
	}
	reply, e := peer.SendConv(w.ctx, conv, ConvOutgoing{Body: "after recovery", ReplyTo: sent.LID})
	if e != nil {
		t.Fatal("reply through old link", e)
	}
	var current string
	eventually(t, "new device receives reply", func() bool {
		cs, _ := fresh.Conversations()
		for _, c := range cs {
			ms, _ := fresh.ConversationMessages(c.ID)
			for _, m := range ms {
				if m.LID == reply.LID {
					current = c.ID
					return m.ReplyTo == sent.LID && m.Body == "after recovery"
				}
			}
		}
		return false
	})
	if current == conv {
		t.Fatal("forged old root")
	}
	if ref, e := peer.MessagingRefOf(conv, reply.ID, "out"); e != nil || ref.Conv != current {
		t.Fatal("message control root", ref, e)
	}
	views, e := peer.MessagingConversations()
	if e != nil {
		t.Fatal(e)
	}
	if len(views) != 1 || views[0].Peer.Person != next.Person {
		t.Fatal("two DM views", views)
	}
	for range 2 {
		ms, e := peer.MessagingConversationMessages(conv)
		if e != nil {
			t.Fatal(e)
		}
		notices := 0
		before, after := false, false
		for _, m := range ms {
			if m.Sub == "continuity" {
				notices++
			}
			before = before || m.Body == "before recovery"
			after = after || m.Body == "after recovery"
		}
		if notices != 1 || !before || !after {
			t.Fatalf("union before=%v after=%v notices=%d", before, after, notices)
		}
	}
	// Recover once more through the same old link. Earlier signed roots and
	// the first transition notice remain intact; the terminal gets a new root.
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email}); e != nil {
		t.Fatal(e)
	}
	third := mel736OneJoin(t, w, "third")
	runAgent(t, third)
	waitNamedAgentCaps(t, third)
	terminal, _, _ := third.Person()
	if _, e = peer.refreshPerson(w.ctx, terminal.Person, false); e != nil {
		t.Fatal(e)
	}
	again, e := peer.SendConv(w.ctx, conv, ConvOutgoing{Body: "After second recovery", ReplyTo: reply.LID})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "terminal device gets continued reply", func() bool {
		cs, _ := third.Conversations()
		for _, c := range cs {
			ms, _ := third.ConversationMessages(c.ID)
			for _, m := range ms {
				if m.LID == again.LID {
					return true
				}
			}
		}
		return false
	})
	ms, e := peer.MessagingConversationMessages(conv)
	if e != nil {
		t.Fatal(e)
	}
	notices := 0
	for _, m := range ms {
		if m.Sub == "continuity" {
			notices++
		}
	}
	if notices != 2 {
		t.Fatal("second recovery notice count", notices)
	}

}

func TestMEL736OneStaleRosterAndDirectoryRetry(t *testing.T) {
	w := newMEL736OneWorld(t)
	first := mel736OneJoin(t, w, "lost")
	peer := w.admin
	runAgent(t, first)
	old, _, _ := first.Person()
	if _, e := peer.refreshPerson(w.ctx, old.Person, false); e != nil {
		t.Fatal(e)
	}
	phone := mel736OneJoin(t, w, "phone")
	request := pendingLink(t, first)
	if e := first.DecideLink(w.ctx, request.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, e := phone.finishLink(w.ctx); e != nil {
		t.Fatal(e)
	}
	stale, _, _ := peer.store.personByID(old.Person)
	if len(stale.roster.Devices) != 1 {
		t.Fatal("fixture not stale")
	}
	if e := peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e := peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email}); e != nil {
		t.Fatal(e)
	}
	fresh := mel736OneJoin(t, w, "new")
	next, _, _ := fresh.Person()
	faults := injectFaults(peer)
	faults.add("GET", "/v1/agents/", 1, false)
	if _, e := peer.refreshPerson(w.ctx, next.Person, false); e == nil {
		t.Fatal("directory failure accepted")
	}
	if to, e := peer.store.logicalPerson(old.Person); e != nil || to != old.Person {
		t.Fatal("failed refresh aliased", to, e)
	}
	raws, e := peer.fetchChain(w.ctx, next.Person, -1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = peer.store.pinChain(next.Person, raws, peer.Self(), false); !errors.Is(e, errPersonConflict) {
		t.Fatal("fixture email claim not frozen", e)
	}
	members, e := peer.Members(w.ctx)
	if e != nil {
		t.Fatal(e)
	}
	peer.checkPersons(w.ctx, members)
	if p, ok, e := peer.store.personByID(next.Person); e != nil || !ok || p.info.State != personPinned {
		t.Fatal("member push did not retry frozen claim", p.info, e)
	}

	kept, _, e := peer.store.personByID(old.Person)
	if e != nil || len(kept.roster.Devices) != 2 || kept.roster.Seq <= stale.roster.Seq {
		t.Fatal("stale chain not refreshed", kept.info, e)
	}
	for _, d := range kept.roster.Devices {
		if known, e := revokedDeviceKnown(peer.store.db, d.Address, d.Fingerprint()); e != nil || !known {
			t.Fatal("unknown old device", d.Address, e)
		}
	}
	if to, e := peer.store.logicalPerson(old.Person); e != nil || to != next.Person {
		t.Fatal("complete evidence did not alias", to, e)
	}
	// The recovered installation can verify the revoked old identity from
	// genesis, even though its own current roster holds the same email.
	if _, e := fresh.refreshPerson(w.ctx, old.Person, false); e != nil {
		t.Fatal("recovered device cannot pin old membership", e)
	}
	if to, e := fresh.store.logicalPerson(old.Person); e != nil || to != next.Person {
		t.Fatal("own recovered membership", to, e)
	}
}

func TestMEL736OneStartupEmailClaimRecovery(t *testing.T) {
	for _, removed := range []bool{false, true} {
		name := "live-holder"
		if removed {
			name = "revoked-holder-unknown-locally"
		}
		t.Run(name, func(t *testing.T) {
			w := newMEL736OneWorld(t)
			oldDevice := mel736OneJoin(t, w, "lost")
			peer := w.admin // No daemon/discovery: exercise startup work directly.
			oldRecord, ok, e := oldDevice.store.selfPerson(oldDevice.Address)
			if e != nil || !ok {
				t.Fatal("old roster", e)
			}
			old := oldRecord.roster
			if _, e := peer.refreshPerson(w.ctx, old.Person, false); e != nil {
				t.Fatal(e)
			}
			var next protocol.PersonRoster
			var raws [][]byte
			if removed {
				if e := peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email, Remove: true}); e != nil {
					t.Fatal(e)
				}
				if e := peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email}); e != nil {
					t.Fatal(e)
				}
				fresh := mel736OneJoin(t, w, "new")
				person, _, _ := fresh.Person()
				raws, e = peer.fetchChain(w.ctx, person.Person, -1)
				if e != nil {
					t.Fatal(e)
				}
				next, e = protocol.ParsePersonRoster(raws[len(raws)-1])
				if e != nil {
					t.Fatal(e)
				}
			} else {
				// A same-email claim cannot displace the live Hub identity.
				k, e := identity.Generate()
				if e != nil {
					t.Fatal(e)
				}
				next = protocol.PersonRoster{Person: protocol.NewID(), Label: old.Label, Email: old.Email, Devices: []identity.Public{k.Public("claim/new")}}
				next.Sign(k.Sign)
				raw, _ := json.Marshal(next)
				raws = [][]byte{raw}
			}
			if known, e := revokedDeviceKnown(peer.store.db, oldDevice.Address, oldDevice.Self().Fingerprint()); e != nil || known {
				t.Fatal("fixture already knows revocation", known, e)
			}
			if _, e := peer.store.pinChain(next.Person, raws, peer.Self(), false); !errors.Is(e, errPersonConflict) {
				t.Fatal("fixture did not store email claim hold", e)
			}
			if claim, e := peer.store.emailClaimOnly(next.Person); e != nil || !claim {
				t.Fatal("fixture is not conflict with NULL conflicting step", claim, e)
			}
			me := protocol.NewID()
			for _, p := range []protocol.PersonRoster{old, next} {
				r := protocol.ConvRoot{V: 1, Kind: protocol.ConvKindDM, Created: 1, Nonce: protocol.NewID(), Members: []protocol.ConvMember{{Person: me, Roster: old.Hash()}, {Person: p.Person, Roster: p.Hash()}}}
				raw, _ := json.Marshal(r)
				if e := peer.store.addConversation(r, raw, p.Person); e != nil {
					t.Fatal(e)
				}
			}
			for range 2 { // Startup/repeated work needs no new roster push.
				if e := peer.refreshContinuity(w.ctx); e != nil {
					t.Fatal(e)
				}
				known, e := revokedDeviceKnown(peer.store.db, oldDevice.Address, oldDevice.Self().Fingerprint())
				if e != nil || known != removed {
					t.Fatal("revocation evidence", known, e)
				}
				wantPerson, wantState, wantRows := old.Person, personConflict, 2
				if removed {
					wantPerson, wantState, wantRows = next.Person, personPinned, 1
				}
				p, ok, e := peer.store.personByID(next.Person)
				if e != nil || !ok || p.info.State != wantState {
					t.Fatal("claim state", p.info, e)
				}
				if to, e := peer.store.logicalPerson(old.Person); e != nil || to != wantPerson {
					t.Fatal("logical person", to, e)
				}
				if rows, e := peer.MessagingConversations(); e != nil || len(rows) != wantRows {
					t.Fatal("DM rows", rows, e)
				}
			}
		})
	}
}

func TestMEL736OneGroupContinuesAfterRecovery(t *testing.T) {
	w := newMEL736OneWorld(t)
	oldDevice := mel736OneJoin(t, w, "lost")
	peer := w.admin
	if _, e := peer.CreatePerson(w.ctx, "Colleague"); e != nil {
		t.Fatal(e)
	}
	stopOld := runAgent(t, oldDevice)
	runAgent(t, peer)
	waitNamedAgentCaps(t, oldDevice)
	waitNamedAgentCaps(t, peer)
	old, _, _ := oldDevice.store.selfPerson(oldDevice.Address)
	me, _, _ := peer.store.selfPerson(peer.Address)
	realm, _ := peer.RealmID()
	root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: protocol.ConvCreator{Person: me.roster.Person, Roster: me.roster.Hash(), Address: peer.Address, Fingerprint: peer.Self().Fingerprint()}, Members: []protocol.ConvMember{{Person: old.roster.Person, Roster: old.roster.Hash()}, {Person: me.roster.Person, Roster: me.roster.Hash()}}, Nonce: protocol.NewID(), Created: time.Now().Unix(), Realm: realm, Title: "Recovery group", Admins: []string{me.roster.Person}}
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
	packet, e := peer.SignGroupState(w.ctx, GroupContext{Root: root, State: state})
	if e != nil {
		t.Fatal(e)
	}
	commit, e := peer.BuildGroupCommit(w.ctx, packet)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = peer.PublishGroup(w.ctx, commit, packet); e != nil {
		t.Fatal(e)
	}
	eventually(t, "old group context", func() bool { _, e := oldDevice.GroupContext(root.ID()); return e == nil })
	phone := mel736OneJoin(t, w, "phone")
	request := pendingLink(t, oldDevice)
	if e = oldDevice.DecideLink(w.ctx, request.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, e = phone.finishLink(w.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = peer.refreshPerson(w.ctx, old.roster.Person, false); e != nil {
		t.Fatal(e)
	}
	members, e := peer.GroupMembers(root.ID())
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range members {
		if m.Person == old.roster.Person && m.Roster != old.roster.Hash() {
			t.Fatal("ordinary linked roster rewrote signed group admission", m)
		}
	}
	stopOld()
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.roster.Email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e = peer.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.roster.Email}); e != nil {
		t.Fatal(e)
	}
	fresh := mel736OneJoin(t, w, "new")
	runAgent(t, fresh)
	waitNamedAgentCaps(t, fresh)
	next, _, _ := fresh.Person()
	if _, e = peer.refreshPerson(w.ctx, next.Person, false); e != nil {
		t.Fatal(e)
	}
	sent, e := peer.SendConv(w.ctx, root.ID(), ConvOutgoing{Body: "Group member continues"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "recovered group member receives", func() bool {
		ms, _ := fresh.ConversationMessages(root.ID())
		for _, m := range ms {
			if m.LID == sent.LID {
				return true
			}
		}
		return false
	})
	reply, e := fresh.SendConv(w.ctx, root.ID(), ConvOutgoing{Body: "Recovered member replies", ReplyTo: sent.LID})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "recovered author admitted", func() bool {
		ms, _ := peer.ConversationMessages(root.ID())
		for _, m := range ms {
			if m.LID == reply.LID {
				return true
			}
		}
		return false
	})
}

func mel736OneJoin(t *testing.T, w *googleReviewWorld, name string) *Agent {
	t.Helper()
	home := t.TempDir()
	seedFixtureStore(t, home)
	pub, e := GoogleDevice(home, name)
	if e != nil {
		t.Fatal(e)
	}
	a, e := JoinGoogle(w.ctx, home, w.opts, w.issuer.IDToken(t, pub, "person@example.com", nil), name)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// Schema creation is already covered by the owning store tests. Reuse their
// empty current-schema image before starting the unchanged network deadline.
func newMEL736OneWorld(t *testing.T) *googleReviewWorld {
	t.Helper()
	issuer := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleHTTP: issuer.Client}, "127.0.0.1:0")
	home := t.TempDir()
	seedFixtureStore(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, e := protocol.DecodeInvite(code)
	if e != nil {
		t.Fatal(e)
	}
	admin, e := Join(ctx, home, code, "admin")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { admin.Close() })
	if e = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Domain: "example.com"}); e != nil {
		t.Fatal(e)
	}
	return &googleReviewWorld{ctx: ctx, issuer: issuer, admin: admin, opts: GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}}
}
