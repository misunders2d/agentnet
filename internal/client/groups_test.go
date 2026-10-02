package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupContextOrderedAuthorityProof(t *testing.T) {
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := id.Public("group/admin")
	roster := protocol.PersonRoster{Person: protocol.NewID(), Label: "Admin", Devices: []identity.Public{pub}}
	roster.Sign(id.Sign)
	root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: protocol.ConvCreator{Person: roster.Person, Roster: roster.Hash(), Address: pub.Address, Fingerprint: pub.Fingerprint()}, Members: []protocol.ConvMember{{Person: roster.Person, Roster: roster.Hash()}}, Nonce: protocol.NewID(), Created: 1790000000, Realm: protocol.NewID(), Title: "Team", Admins: []string{roster.Person}}
	root.Sign(id.Sign)
	admission := protocol.GroupAdmission{Conv: root.ID(), Realm: root.Realm, Person: roster.Person, Roster: roster.Hash(), By: pub.Fingerprint()}
	admission.Sign(id.Sign)
	state := protocol.GroupState{V: 1, Conv: root.ID(), Realm: root.Realm, Title: root.Title, Actor: roster.Person, ActorRoster: roster.Hash(), By: pub.Fingerprint(), Members: []protocol.GroupMember{{ConvMember: root.Members[0], Admin: true, Admission: admission}}}
	state.Sign(id.Sign)
	resolve := func(person, hash string) (protocol.PersonRoster, bool) {
		return roster, person == roster.Person && hash == roster.Hash()
	}
	next := state
	next.Seq = 1
	next.Prev = state.Hash()
	next.Title = "Renamed"
	next.Sign(id.Sign)
	packet := GroupContext{Root: root, Proof: []protocol.GroupState{state}, State: next}
	if err := VerifyGroupContext(packet, resolve); err != nil {
		t.Fatal(err)
	}
	packet.Proof = nil
	if VerifyGroupContext(packet, resolve) == nil {
		t.Fatal("new linked admin accepted missing original authority")
	}
	packet.Proof = []protocol.GroupState{state}
	packet.State.Seq = 2
	packet.State.Sign(id.Sign)
	if VerifyGroupContext(packet, resolve) == nil {
		t.Fatal("skipped admin transition accepted")
	}
	packet.State = next
	packet.Withdrawals = []protocol.GroupWithdrawal{{Conv: state.Conv, Realm: state.Realm, Person: roster.Person, Admission: admission.Hash(), Roster: roster.Hash(), By: pub.Fingerprint()}}
	if VerifyGroupContext(packet, resolve) == nil {
		t.Fatal("unsigned admin withdrawal accepted")
	}
}

func TestGroupPublicationResponseLossAndRecovery(t *testing.T) {
	w, packet, commit := newGroupPublicationFixture(t)
	root := packet.Root
	var err error
	f := injectFaults(w.alice)
	f.add("POST", "/v1/groups/", 1, true)
	if _, err = w.alice.PublishGroup(tctx(t), commit, packet); err == nil {
		t.Fatal("fault did not lose response")
	}
	var pending int
	if err = w.alice.store.db.QueryRow("SELECT count(*) FROM group_publications WHERE published=0").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending durable ciphertext %d %v", pending, err)
	}
	if err = w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SyncGroupFromRoot(tctx(t), root); err != nil {
		t.Fatal(err)
	}
	p, err := w.bob.GroupContext(root.ID())
	if err != nil || p.State.Hash() != stateHash(packet) {
		t.Fatalf("peer recovered committed ciphertext %v", err)
	}
	members, err := w.bob.GroupMembers(root.ID())
	if err != nil || len(members) != 2 {
		t.Fatalf("verified membership %v %v", members, err)
	}
	// A later accepted state must survive exact custody replay of the old one.
	next := packet
	next.Proof = []protocol.GroupState{packet.State}
	next.State.Seq++
	next.State.Prev = packet.State.Hash()
	next.State.Title = "Renamed"
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	nextCommit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), nextCommit, next); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec("UPDATE group_publications SET published=0 WHERE conv=? AND seq=0", root.ID()); err != nil {
		t.Fatal(err)
	}
	if result, e := w.alice.PublishGroup(tctx(t), commit, packet); e != nil || !result.Same {
		t.Fatalf("exact replay custody: %+v %v", result, e)
	}
	latest, err := w.alice.GroupContext(root.ID())
	if err != nil || latest.State.Hash() != next.State.Hash() {
		t.Fatalf("old custody replay rewound context: seq=%d err=%v", latest.State.Seq, err)
	}
	if err = w.alice.store.db.QueryRow("SELECT count(*) FROM group_publications WHERE published=0").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("replay custody not recorded: %d %v", pending, err)
	}
	var competitors sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		competitors.Add(2)
		go func() { defer competitors.Done(); _, e := w.alice.PublishGroup(tctx(t), commit, packet); errs <- e }()
		go func() { defer competitors.Done(); errs <- w.alice.SyncGroup(tctx(t), root.ID()) }()
	}
	competitors.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	latest, err = w.alice.GroupContext(root.ID())
	if err != nil || latest.State.Hash() != next.State.Hash() {
		t.Fatalf("competing sync/publication rewound context: seq=%d err=%v", latest.State.Seq, err)
	}
	if err = w.bob.NoteGroupHead(protocol.GroupHead{Bootstrap: root.Creator.Fingerprint, Conv: root.ID(), Seq: 1, Hash: packet.State.Hash()}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.GroupMembers(root.ID()); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("newer hint fell back to root: %v", err)
	}
}
func stateHash(p GroupContext) string { return p.State.Hash() }

func TestGroupContextMonotoneTransaction(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "context.db"), []string{GroupClientSchema})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conv := strings.Repeat("a", 64)
	newer := GroupContext{State: protocol.GroupState{Conv: conv, Seq: 2, Title: "new"}}
	install := func(p GroupContext) error {
		tx, e := db.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = installGroupContext(tx, p); e != nil {
			return e
		}
		return tx.Commit()
	}
	if err = install(newer); err != nil {
		t.Fatal(err)
	}
	assertCurrent := func() {
		t.Helper()
		var raw []byte
		var p GroupContext
		if e := db.QueryRow("SELECT payload FROM group_context WHERE conv=?", conv).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal(raw, &p); e != nil || p.State.Hash() != newer.State.Hash() {
			t.Fatalf("context changed: %+v %v", p.State, e)
		}
	}
	rollbackPacket := newer
	rollbackPacket.State.Seq++
	rollbackTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = installGroupContext(rollbackTx, rollbackPacket); err != nil {
		t.Fatal(err)
	}
	if err = rollbackTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCurrent()
	older := newer
	older.State.Seq = 1
	older.State.Title = "old"
	if err = install(older); err != nil {
		t.Fatal(err)
	}
	assertCurrent()
	if err = install(newer); err != nil {
		t.Fatal(err)
	}
	assertCurrent()
	conflict := newer
	conflict.State.Title = "conflict"
	if err = install(conflict); err == nil {
		t.Fatal("same-sequence conflict accepted")
	}
	assertCurrent()
	// Publication bookkeeping and context refusal share rollback boundary.
	if _, err = db.Exec("INSERT INTO group_publications(conv,bootstrap,seq,record,payload)VALUES(?,?,?,?,?)", conv, "creator", 2, []byte("record"), []byte("payload")); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE group_publications SET published=1 WHERE conv=?", conv); err != nil {
		t.Fatal(err)
	}
	if err = installGroupContext(tx, conflict); err == nil {
		t.Fatal("conflict accepted")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var published int
	if err = db.QueryRow("SELECT published FROM group_publications WHERE conv=?", conv).Scan(&published); err != nil || published != 0 {
		t.Fatalf("bookkeeping escaped rollback: %d %v", published, err)
	}
	assertCurrent()
	// A storage failure cannot leave custody bookkeeping committed either.
	if _, err = db.Exec("CREATE TRIGGER fail_context BEFORE UPDATE ON group_context BEGIN SELECT RAISE(ABORT,'injected context failure'); END"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE group_publications SET published=1 WHERE conv=?", conv); err != nil {
		t.Fatal(err)
	}
	higher := newer
	higher.State.Seq++
	if err = installGroupContext(tx, higher); err == nil {
		t.Fatal("storage fault not observed")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT published FROM group_publications WHERE conv=?", conv).Scan(&published); err != nil || published != 0 {
		t.Fatalf("fault bookkeeping escaped rollback: %d %v", published, err)
	}
	assertCurrent()
	if _, err = db.Exec("DROP TRIGGER fail_context"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE group_context SET payload=? WHERE conv=?", []byte("broken"), conv); err != nil {
		t.Fatal(err)
	}
	if err = install(higher); err == nil {
		t.Fatal("corrupt stored context silently replaced")
	}
}

func newGroupPublicationFixture(t *testing.T) (*world, GroupContext, protocol.GroupCommit) {
	t.Helper()
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	for _, a := range []*Agent{w.alice, w.bob} {
		var n int
		if err := a.store.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='group_context'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			if _, err := a.store.db.Exec(GroupClientSchema); err != nil {
				t.Fatal(err)
			}
		}
	}
	ar, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	br, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	realm, err := w.alice.RealmID()
	if err != nil {
		t.Fatal(err)
	}
	root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: protocol.ConvCreator{Person: ar.roster.Person, Roster: ar.roster.Hash(), Address: w.alice.Address, Fingerprint: w.alice.id.Public(w.alice.Address).Fingerprint()}, Members: []protocol.ConvMember{{Person: ar.roster.Person, Roster: ar.roster.Hash()}, {Person: br.roster.Person, Roster: br.roster.Hash()}}, Nonce: protocol.NewID(), Created: 1790000000, Realm: realm, Title: "Team", Admins: []string{ar.roster.Person, br.roster.Person}}
	slices.SortFunc(root.Members, func(a, b protocol.ConvMember) int { return strings.Compare(a.Person, b.Person) })
	slices.Sort(root.Admins)
	root.Sign(w.alice.id.Sign)
	state := protocol.GroupState{V: 1, Conv: root.ID(), Realm: realm, Title: root.Title}
	for _, m := range root.Members {
		a := w.alice
		if m.Person == br.roster.Person {
			a = w.bob
		}
		admission, err := a.SignGroupAdmission(root, 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		state.Members = append(state.Members, protocol.GroupMember{ConvMember: m, Admin: true, Admission: admission})
	}
	packet, err := w.alice.SignGroupState(tctx(t), GroupContext{Root: root, State: state})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), packet)
	if err != nil {
		t.Fatal(err)
	}
	return w, packet, commit
}

func TestGroupConfirmedLoserRetiredForExplicitRetry(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, initial); err != nil {
		t.Fatal(err)
	}
	// The second administrator must ingest the original signed bootstrap
	// commit before appending; plaintext proposal states are not authority.
	if err := w.bob.SyncGroupFromRoot(tctx(t), initial.Root); err != nil {
		t.Fatal(err)
	}
	proposal := initial
	proposal.Proof = []protocol.GroupState{initial.State}
	proposal.State.Seq = 1
	proposal.State.Prev = initial.State.Hash()
	loser := proposal
	loser.State.Title = "Alice proposal"
	loser, err := w.alice.SignGroupState(tctx(t), loser)
	if err != nil {
		t.Fatal(err)
	}
	loserCommit, err := w.alice.BuildGroupCommit(tctx(t), loser)
	if err != nil {
		t.Fatal(err)
	}
	winner := proposal
	winner.State.Title = "Bob proposal"
	winner, err = w.bob.SignGroupState(tctx(t), winner)
	if err != nil {
		t.Fatal(err)
	}
	winnerCommit, err := w.bob.BuildGroupCommit(tctx(t), winner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.PublishGroup(tctx(t), winnerCommit, winner); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), loserCommit, loser); err == nil {
		t.Fatal("losing operation reported success")
	}
	var pending int
	if err = w.alice.store.db.QueryRow("SELECT count(*) FROM group_publications WHERE conv=? AND seq=1 AND published=0", initial.State.Conv).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("confirmed loser still blocks slot: %d %v", pending, err)
	}
	// Retirement clears only the losing attempt; an explicit retry first
	// fetches the winning original authority and current state.
	if err = w.alice.SyncGroup(tctx(t), initial.State.Conv); err != nil {
		t.Fatal(err)
	}
	current, err := w.alice.GroupContext(initial.State.Conv)
	if err != nil || current.State.Seq != 1 || current.State.Hash() != winner.State.Hash() {
		t.Fatalf("fetched current winner: %+v %v", current.State, err)
	}
	retry := current
	retry.State.Seq = 2
	retry.State.Prev = winner.State.Hash()
	retry.State.Title = "Explicit retry"
	retry, err = w.alice.SignGroupState(tctx(t), retry)
	if err != nil {
		t.Fatal(err)
	}
	retryCommit, err := w.alice.BuildGroupCommit(tctx(t), retry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), retryCommit, retry); err != nil {
		t.Fatalf("explicit fresh retry blocked: %v", err)
	}
}

func TestGroupUnknownTransportRetainsExactPendingBytes(t *testing.T) {
	w, packet, commit := newGroupPublicationFixture(t)
	f := injectFaults(w.alice)
	f.add("POST", "/v1/groups/", 1, false)
	f.add("GET", "/v1/groups/", 1, false)
	if _, err := w.alice.PublishGroup(tctx(t), commit, packet); err == nil {
		t.Fatal("unknown transport reported success")
	}
	var stored []byte
	if err := w.alice.store.db.QueryRow("SELECT record FROM group_publications WHERE conv=? AND published=0", commit.Conv).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(commit)
	if !bytes.Equal(raw, stored) {
		t.Fatal("uncertain bytes discarded or changed")
	}
	if err := w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestGroupRecoveryConflictDoesNotStarveAnotherConversation(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, initial); err != nil {
		t.Fatal(err)
	}
	// The second administrator must ingest the original signed bootstrap
	// commit before appending; plaintext proposal states are not authority.
	if err := w.bob.SyncGroupFromRoot(tctx(t), initial.Root); err != nil {
		t.Fatal(err)
	}
	proposal := initial
	proposal.Proof = []protocol.GroupState{initial.State}
	proposal.State.Seq = 1
	proposal.State.Prev = initial.State.Hash()
	loser := proposal
	loser.State.Title = "loser"
	loser, err := w.alice.SignGroupState(tctx(t), loser)
	if err != nil {
		t.Fatal(err)
	}
	loserCommit, err := w.alice.BuildGroupCommit(tctx(t), loser)
	if err != nil {
		t.Fatal(err)
	}
	f := injectFaults(w.alice)
	f.add("POST", loserCommit.Conv, 1, false)
	f.add("GET", loserCommit.Conv, 1, false)
	if _, err = w.alice.PublishGroup(tctx(t), loserCommit, loser); err == nil {
		t.Fatal("fault missing")
	}
	winner := proposal
	winner.State.Title = "winner"
	winner, err = w.bob.SignGroupState(tctx(t), winner)
	if err != nil {
		t.Fatal(err)
	}
	winnerCommit, err := w.bob.BuildGroupCommit(tctx(t), winner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.PublishGroup(tctx(t), winnerCommit, winner); err != nil {
		t.Fatal(err)
	}
	// Sort second conversation after conflict to prove recovery advances past it.
	root := initial.Root
	for root.ID() <= initial.Root.ID() {
		root.Nonce = protocol.NewID()
	}
	root.Sign(w.alice.id.Sign)
	state := initial.State
	state.Conv = root.ID()
	state.Title = root.Title
	state.Members = append([]protocol.GroupMember{}, initial.State.Members...)
	for i, m := range state.Members {
		member := w.alice
		if m.Person != root.Creator.Person {
			member = w.bob
		}
		admission, e := member.SignGroupAdmission(root, 0, "", nil)
		if e != nil {
			t.Fatal(e)
		}
		state.Members[i].Admission = admission
	}
	other, err := w.alice.SignGroupState(tctx(t), GroupContext{Root: root, State: state})
	if err != nil {
		t.Fatal(err)
	}
	otherCommit, err := w.alice.BuildGroupCommit(tctx(t), other)
	if err != nil {
		t.Fatal(err)
	}
	f.add("POST", otherCommit.Conv, 1, false)
	if _, err = w.alice.PublishGroup(tctx(t), otherCommit, other); err == nil {
		t.Fatal("fault missing")
	}
	if err = w.alice.RecoverGroupPublications(tctx(t)); err == nil || !strings.Contains(err.Error(), "pending attempt retired") {
		t.Fatalf("conflict not reported: %v", err)
	}
	var pending int
	if err = w.alice.store.db.QueryRow("SELECT count(*) FROM group_publications WHERE published=0").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("unrelated recovery starved: %d %v", pending, err)
	}
	recovered, err := w.alice.GroupContext(root.ID())
	if err != nil || recovered.State.Hash() != other.State.Hash() {
		t.Fatalf("other conversation not recovered: %v", err)
	}
}

func TestGroupDeniedWinnerLookupRetainsPending(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, initial); err != nil {
		t.Fatal(err)
	}
	// The second administrator must ingest the original signed bootstrap
	// commit before appending; plaintext proposal states are not authority.
	if err := w.bob.SyncGroupFromRoot(tctx(t), initial.Root); err != nil {
		t.Fatal(err)
	}
	proposal := initial
	proposal.Proof = []protocol.GroupState{initial.State}
	proposal.State.Seq = 1
	proposal.State.Prev = initial.State.Hash()
	loser := proposal
	loser.State.Title = "uncertain loser"
	loser, err := w.alice.SignGroupState(tctx(t), loser)
	if err != nil {
		t.Fatal(err)
	}
	loserCommit, err := w.alice.BuildGroupCommit(tctx(t), loser)
	if err != nil {
		t.Fatal(err)
	}
	f := injectFaults(w.alice)
	f.add("POST", loserCommit.Conv, 1, false)
	f.add("GET", loserCommit.Conv, 1, false)
	if _, err = w.alice.PublishGroup(tctx(t), loserCommit, loser); err == nil {
		t.Fatal("fault missing")
	}
	winner := proposal
	winner.State.Members = nil
	for _, m := range initial.State.Members {
		if m.Person != initial.Root.Creator.Person {
			winner.State.Members = append(winner.State.Members, m)
		}
	}
	winner, err = w.bob.SignGroupState(tctx(t), winner)
	if err != nil {
		t.Fatal(err)
	}
	winnerCommit, err := w.bob.BuildGroupCommit(tctx(t), winner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.PublishGroup(tctx(t), winnerCommit, winner); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), loserCommit, loser); err == nil || !strings.Contains(err.Error(), "custody uncertain") {
		t.Fatalf("denied lookup not uncertain: %v", err)
	}
	var stored []byte
	if err = w.alice.store.db.QueryRow("SELECT record FROM group_publications WHERE conv=? AND seq=1 AND published=0", loserCommit.Conv).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(loserCommit)
	if !bytes.Equal(raw, stored) {
		t.Fatal("denied-read pending bytes changed")
	}
}
