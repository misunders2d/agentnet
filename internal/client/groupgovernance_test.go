package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// No stream/timing race: publish the actual removal, then deliver its retained
// encrypted terminal carrier through the real ingress function explicitly.
func TestGroupGovernanceTerminalRemoval(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, initial); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.SyncGroupFromRoot(tctx(t), initial.Root); err != nil {
		t.Fatal(err)
	}
	person, _, err := w.bob.Person()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "retained.txt")
	if err = os.WriteFile(file, []byte("synthetic retained encrypted attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	queued, err := w.bob.SendConv(tctx(t), initial.State.Conv, ConvOutgoing{Body: "queued before removal", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	queuedEnv := groupTurnEnvelope(t, w.bob, queued.ID)
	if len(queuedEnv.Blobs) == 0 {
		t.Fatal("ordinary queued attachment was not retained")
	}
	var uncertain []string
	for _, flags := range []struct {
		started any
		proven  int
	}{{1, 1}, {nil, 1}, {0, 0}} {
		result, e := w.bob.SendConv(tctx(t), initial.State.Conv, ConvOutgoing{Body: "previous handover is unconfirmed"})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.bob.store.db.Exec("UPDATE outbox SET handover_started=?,handover_proven=? WHERE id=?", flags.started, flags.proven, result.ID); e != nil {
			t.Fatal(e)
		}
		uncertain = append(uncertain, result.ID)
	}
	delivered, err := w.bob.SendConv(tctx(t), initial.State.Conv, ConvOutgoing{Body: "already delivered"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.store.db.Exec("UPDATE outbox SET state='delivered',handover_started=1 WHERE id=?", delivered.ID); err != nil {
		t.Fatal(err)
	}
	next, err := w.alice.RemoveGroupMember(tctx(t), initial.State.Conv, person.Person)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := w.alice.store.db.Query(`SELECT envelope FROM outbox WHERE conv=? AND recipient=? AND coalesce(group_admission,'')<>'' ORDER BY rowid`, next.State.Conv, w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &env)
		}
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	rows.Close()
	if len(envs) < 2 {
		t.Fatalf("missing atomic terminal proof/context copies: %d", len(envs))
	}
	for _, env := range envs {
		groupGovernanceDeliver(t, w.alice, w.bob, env)
	}
	stored, err := w.bob.GroupContext(next.State.Conv)
	if err != nil || stored.State.Hash() != next.State.Hash() {
		t.Fatalf("terminal context not installed: %v", err)
	}
	var state, detail string
	var stopped int
	if err = w.bob.store.db.QueryRow("SELECT state,error,send_stopped FROM outbox WHERE id=?", queued.ID).Scan(&state, &detail, &stopped); err != nil || state != stateNotDelivered || stopped != 1 || !strings.Contains(detail, "removed you") {
		t.Fatalf("ordinary queue not visibly stopped: %s %q %d %v", state, detail, stopped, err)
	}
	for _, id := range uncertain {
		if err = w.bob.store.db.QueryRow("SELECT state,error,send_stopped FROM outbox WHERE id=?", id).Scan(&state, &detail, &stopped); err != nil || state != stateFailed || stopped != 1 || !strings.Contains(detail, "previous delivery unconfirmed") {
			t.Fatalf("uncertain previous handover claimed not delivered: %s %q %d %v", state, detail, stopped, err)
		}
	}
	visible, err := w.bob.ConversationMessages(initial.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range uncertain {
		found := false
		for _, message := range visible {
			if message.ID == id {
				found = message.State == stateFailed && message.DeliveryUncertain && message.SendStopped
			}
		}
		if !found {
			t.Fatal("previous handover uncertainty missing from readable projection", id)
		}
	}
	if err = w.bob.store.db.QueryRow("SELECT state,send_stopped FROM outbox WHERE id=?", delivered.ID).Scan(&state, &stopped); err != nil || state != protocol.StateDelivered || stopped != 0 {
		t.Fatalf("proven delivered copy changed: %s %d %v", state, stopped, err)
	}
	for _, blob := range queuedEnv.Blobs {
		if _, err = os.Stat(w.bob.spoolPath(blob.ID)); err != nil {
			t.Fatalf("stopped attachment lost: %v", err)
		}
	}
	if _, err = w.bob.SendConv(tctx(t), next.State.Conv, ConvOutgoing{Body: "must remain read-only"}); err == nil {
		t.Fatal("removed member can send")
	}
	views, err := w.bob.Conversations()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.ID == next.State.Conv && (v.Frozen == "" || v.Role == "member") {
			t.Fatalf("removed composer active: %+v", v)
		}
	}
	for _, env := range envs {
		groupGovernanceDeliver(t, w.alice, w.bob, env)
	}
	var n int
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND recipient=? AND coalesce(group_admission,'')<>''`, next.State.Conv, w.bob.Address).Scan(&n); err != nil || n != len(envs) {
		t.Fatalf("duplicate terminal copies: %d %v", n, err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), first, initial); err != nil {
		t.Fatal(err)
	}
}

func TestGroupGovernanceTerminalNegativeAndReadmission(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.SyncGroupFromRoot(tctx(t), p.Root); err != nil {
		t.Fatal(err)
	}
	bob, _, _ := w.bob.Person()
	next, err := w.alice.RemoveGroupMember(tctx(t), p.State.Conv, bob.Person)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = w.alice.store.db.QueryRow("SELECT payload FROM group_publications WHERE conv=? AND seq=?", p.State.Conv, next.State.Seq).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved groupPublicationPayload
	if err = json.Unmarshal(raw, &saved); err != nil || saved.Terminal == nil {
		t.Fatal("terminal witness missing", err)
	}
	terminal := saved.Terminal
	resolve, err := w.alice.groupResolver(tctx(t), terminal.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"rename", "role", "admission", "extra-omission", "nonadmin", "bad-proof-signature", "missing-proof", "oversized-proof", "own-withdrawal", "guest-payload"} {
		t.Run(kind, func(t *testing.T) {
			encoded, _ := json.Marshal(terminal.Context)
			var bad GroupContext
			json.Unmarshal(encoded, &bad)
			switch kind {
			case "rename":
				bad.State.Title += " changed"
			case "role":
				bad.State.Members[0].Admin = !bad.State.Members[0].Admin
			case "admission":
				bad.State.Members[0].Admission.History = []protocol.GroupHistoryRef{{LID: protocol.NewID(), Author: w.alice.Self().Fingerprint(), Hash: strings.Repeat("e", 64)}}
			case "extra-omission":
				bad.State.Members = nil
			case "nonadmin":
				bad.State.Actor = bob.Person
			case "bad-proof-signature":
				bad.Proof[0].Sig[0] ^= 1
			case "missing-proof":
				bad.Proof = nil
			case "oversized-proof":
				bad.Proof = make([]protocol.GroupState, 4097)
			case "own-withdrawal":
				bad.Withdrawals = []protocol.GroupWithdrawal{{Conv: p.State.Conv, Realm: p.State.Realm, Person: bob.Person, Admission: terminal.Admission}}
			case "guest-payload":
				bad.Memberships = []protocol.ParticipationEvent{{}}
			}
			if _, err := w.alice.verifyGroupTerminal(bad, resolve); err == nil {
				t.Fatal("invalid terminal proof accepted")
			}
		})
	}
	if err = groupTerminalRecipient(w.alice.store.db, terminal, w.bob.Address, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec("UPDATE peers SET pending=public WHERE address=?", w.bob.Address); err != nil {
		t.Fatal(err)
	}
	if err = groupTerminalRecipient(w.alice.store.db, terminal, w.bob.Address, w.bob.Self().Fingerprint()); err == nil {
		t.Fatal("changed key accepted")
	}
	w.alice.store.db.Exec("UPDATE peers SET pending=NULL WHERE address=?", w.bob.Address)
	w.alice.store.db.Exec("UPDATE persons SET state=? WHERE person=?", personConflict, bob.Person)
	if err = groupTerminalRecipient(w.alice.store.db, terminal, w.bob.Address, w.bob.Self().Fingerprint()); err == nil {
		t.Fatal("frozen person accepted")
	}
	w.alice.store.db.Exec("UPDATE persons SET state=? WHERE person=?", personPinned, bob.Person)
	for _, env := range terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv) {
		var sub, body, state string
		if err = w.alice.store.db.QueryRow("SELECT sub,body,state FROM outbox WHERE id=?", env.ID).Scan(&sub, &body, &state); err != nil {
			t.Fatal(err)
		}
		for _, actor := range []bool{false, true} {
			person, restore := bob.Person, personPinned
			if actor {
				person, restore = next.State.Actor, personSelf
			}
			w.alice.store.db.Exec("UPDATE persons SET state=? WHERE person=?", personConflict, person)
			handled, allowed, e := w.alice.mayDeliverGroupTerminal(env, sub, body, "terminal:1:"+terminal.Admission)
			if !handled || allowed || e != nil {
				t.Fatalf("temporary conflict did not wait: %v %v %v", handled, allowed, e)
			}
			var after string
			w.alice.store.db.QueryRow("SELECT state FROM outbox WHERE id=?", env.ID).Scan(&after)
			if after != state {
				t.Fatal("temporary conflict permanently stopped retained terminal")
			}
			w.alice.store.db.Exec("UPDATE persons SET state=? WHERE person=?", restore, person)
			handled, allowed, e = w.alice.mayDeliverGroupTerminal(env, sub, body, "terminal:1:"+terminal.Admission)
			if !handled || !allowed || e != nil {
				t.Fatalf("same pinned key did not resume: %v %v %v", handled, allowed, e)
			}
		}
	}
	// A later, explicitly signed fresh admission supersedes this terminal.
	admission, err := w.bob.SignGroupAdmission(p.Root, next.State.Seq+1, next.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rejoined := next
	rejoined.State.Seq++
	rejoined.State.Prev = next.State.Hash()
	rejoined.State.Members = append(slices.Clone(next.State.Members), protocol.GroupMember{ConvMember: protocol.ConvMember{Person: bob.Person, Roster: bob.Roster}, Admission: admission})
	slices.SortFunc(rejoined.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	rejoined, err = w.alice.SignGroupState(tctx(t), rejoined)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), rejoined)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, rejoined); err != nil {
		t.Fatal(err)
	}
	if err = groupTerminalRecipient(w.alice.store.db, terminal, w.bob.Address, w.bob.Self().Fingerprint()); err == nil {
		t.Fatal("readmission did not supersede delayed terminal")
	}
	var original protocol.GroupCommit
	if err = w.alice.store.db.QueryRow("SELECT record FROM group_proof_records WHERE conv=? AND seq=?", next.State.Conv, next.State.Seq).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{original, commit}}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.AcceptGroupCommit(tctx(t), commit); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.acceptGroupContextFrom(tctx(t), terminal.Context, w.alice.Self()); err != nil {
		t.Fatal(err)
	}
	current, err := w.bob.GroupContext(next.State.Conv)
	if err != nil || current.State.Hash() != rejoined.State.Hash() {
		t.Fatalf("delayed terminal revoked readmission: %v", err)
	}
	if err = w.alice.AcceptGroupContext(tctx(t), terminal.Context); err == nil {
		t.Fatal("normal current-reader API accepted former-member terminal class")
	}
}

func terminalFixtureEnvelopes(t *testing.T, a, to *Agent, conv string) []envelope.Envelope {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE conv=? AND recipient=? AND group_admission LIKE 'terminal:%' ORDER BY rowid`, conv, to.Address)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &env)
		}
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	return envs
}

func TestGroupGovernanceTerminalRecoveryAtomic(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.SyncGroupFromRoot(tctx(t), p.Root); err != nil {
		t.Fatal(err)
	}
	bob, _, _ := w.bob.Person()
	previous := beforeOutbox
	defer func() { beforeOutbox = previous }()
	beforeOutbox = func() { panic("synthetic terminal interruption") }
	interrupted := false
	func() {
		defer func() {
			if got := recover(); got != nil {
				if got != "synthetic terminal interruption" {
					panic(got)
				}
				interrupted = true
			}
		}()
		_, err := w.alice.RemoveGroupMember(tctx(t), p.State.Conv, bob.Person)
		if err != nil {
			t.Fatal(err)
		}
	}()
	beforeOutbox = previous
	if !interrupted {
		t.Fatal("publication barrier not reached")
	}
	var pending int
	var raw []byte
	if err := w.alice.store.db.QueryRow("SELECT published,payload FROM group_publications WHERE conv=? AND seq=1", p.State.Conv).Scan(&pending, &raw); err != nil {
		t.Fatal(err)
	}
	var saved groupPublicationPayload
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Terminal == nil || pending != 0 {
		t.Fatal("immutable terminal intent not retained", err)
	}
	if len(terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv)) != 0 {
		t.Fatal("partial terminal outbox survived interruption")
	}
	home := w.alice.home
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	w.alice = reopened
	if err = w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	envs := terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv)
	if len(envs) != 2 {
		t.Fatalf("atomic terminal recovery copies %d", len(envs))
	}
	before, _ := json.Marshal(envs)
	if err = w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv))
	if !bytes.Equal(before, after) {
		t.Fatal("recovery changed exact ciphertext/logical IDs")
	}
	for _, env := range envs {
		handled, allowed, e := w.alice.mayDeliverGroup(env)
		if e != nil || !handled || !allowed {
			t.Fatalf("terminal recovery retry refused %v %v %v", handled, allowed, e)
		}
		groupGovernanceDeliver(t, w.alice, w.bob, env)
	}
	readHome := w.bob.home
	w.bob.Close()
	reader, err := Open(readHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	w.bob = reader
	views, err := reader.Conversations()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.ID == p.State.Conv && !strings.Contains(v.Frozen, "removed you") {
			t.Fatalf("removal not retained through restart: %q", v.Frozen)
		}
	}
}

func TestGroupGovernanceTerminalFoldedWithdrawal(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	for _, stop := range stops {
		stop()
	}
	departure, err := carol.SignGroupWithdrawal(tctx(t), p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AcceptGroupWithdrawal(tctx(t), departure); err != nil {
		t.Fatal(err)
	}
	bob, _, _ := w.bob.Person()
	next, err := w.alice.RemoveGroupMember(tctx(t), p.State.Conv, bob.Person)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.State.Members) != 1 {
		t.Fatal("folded withdrawal not reflected in signed state")
	}
	envs := terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv)
	if len(envs) != 2 {
		t.Fatalf("folded terminal copies %d", len(envs))
	}
	for _, env := range envs {
		groupGovernanceDeliver(t, w.alice, w.bob, env)
	}
	stored, err := w.bob.GroupContext(p.State.Conv)
	if err != nil || stored.State.Hash() != next.State.Hash() || !groupWithdrawalPinned(stored.Withdrawals, departure) {
		t.Fatalf("signed folded departure not retained: %v", err)
	}
}

func TestGroupGovernanceTerminalProofBound(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	prev := p
	const steps = 180
	for seq := int64(1); seq <= steps; seq++ {
		next := prev
		next.State.Seq = seq
		next.State.Prev = prev.State.Hash()
		next.State.Title = "Entitled previous title"
		next.State.Sign(w.alice.id.Sign)
		record := sealedCurrentProof(t, w.alice, w.bob, next)
		if err := w.alice.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{record}}); err != nil {
			t.Fatal(err)
		}
		prev = next
	}
	bob, _, _ := w.bob.Person()
	next := prev
	next.State.Seq++
	next.State.Prev = prev.State.Hash()
	next.State.Members = slices.DeleteFunc(slices.Clone(prev.State.Members), func(m protocol.GroupMember) bool { return m.Person == bob.Person })
	next.State.Sign(w.alice.id.Sign)
	terminal, err := w.alice.prepareGroupTerminal(tctx(t), prev, next, bob.Person, nil)
	if err != nil || terminal == nil || !strings.Contains(terminal.Pending, "bound") {
		if terminal == nil {
			t.Fatalf("terminal unavailable: %v", err)
		}
		t.Fatalf("oversized entitled proof not explicit pending: %v %q", err, terminal.Pending)
	}
	if len(terminal.Context.Proof) != 1 || terminal.Context.Proof[0].Hash() != prev.State.Hash() {
		t.Fatal("bounded fallback silently truncated a wire terminal")
	}
}

func TestGroupGovernanceTerminalUnusableDevice(t *testing.T) {
	for _, mode := range []string{"revoked", "pending-key"} {
		t.Run(mode, func(t *testing.T) {
			w, carol, p, stops := groupTurnsFixture(t)
			phone, awaited, _ := linkPhone(t, w.bob, "terminal-prior-phone")
			if err := w.bob.DecideLink(tctx(t), pendingLink(t, w.bob).ID, true); err != nil {
				t.Fatal(err)
			}
			if out := <-awaited; out.err != nil {
				t.Fatal(out.err)
			}
			bob, _, _ := w.bob.Person()
			if _, err := w.alice.refreshPerson(tctx(t), bob.Person, true); err != nil {
				t.Fatal(err)
			}
			if _, err := w.alice.sendKey(tctx(t), phone.Address); err != nil {
				t.Fatal(err)
			}
			for _, stop := range stops {
				stop()
			}
			if mode == "revoked" {
				if err := w.alice.Revoke(tctx(t), phone.Address); err != nil {
					t.Fatal(err)
				}
			} else if _, err := w.alice.store.db.Exec("UPDATE peers SET pending=public WHERE address=?", phone.Address); err != nil {
				t.Fatal(err)
			}
			next, err := w.alice.RemoveGroupMember(tctx(t), p.State.Conv, bob.Person)
			if err != nil {
				t.Fatalf("one unusable former device blocked ordinary removal publication: %v", err)
			}
			var published, ordinary int
			if err = w.alice.store.db.QueryRow("SELECT published FROM group_publications WHERE conv=? AND seq=?", p.State.Conv, next.State.Seq).Scan(&published); err != nil || published != 1 {
				t.Fatal("ordinary removal not durably published", published, err)
			}
			w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND recipient=? AND sub='group-context' AND json_extract(body,'$.seq')=?`, p.State.Conv, carol.Address, next.State.Seq).Scan(&ordinary)
			if ordinary != 1 || len(terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv)) < 2 || len(terminalFixtureEnvelopes(t, w.alice, phone, p.State.Conv)) != 0 {
				t.Fatal("unusable device affected another remaining/prior human recipient", ordinary)
			}
		})
	}
}

func TestGroupGovernanceTerminalOptionalPreparation(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	bob, _, _ := w.bob.Person()
	next := p
	next.State.Seq++
	next.State.Prev = p.State.Hash()
	next.State.Title = "Removal with another authorized change"
	next.State.Members = slices.DeleteFunc(slices.Clone(p.State.Members), func(m protocol.GroupMember) bool { return m.Person == bob.Person })
	next, err := w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := w.alice.prepareGroupTerminal(tctx(t), p, next, bob.Person, nil)
	if err != nil || terminal != nil {
		t.Fatalf("optional exact-diff notice blocked an ordinary authorized removal: %v", err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.publishGroup(tctx(t), commit, next, terminal); err != nil {
		t.Fatal(err)
	}
	var published int
	w.alice.store.db.QueryRow("SELECT published FROM group_publications WHERE conv=? AND seq=?", p.State.Conv, next.State.Seq).Scan(&published)
	if published != 1 || len(terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv)) != 0 {
		t.Fatal("withheld notice changed ordinary publication")
	}
}

func TestGroupGovernanceTerminalSupersededInvariant(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	for _, stop := range stops {
		stop()
	}
	cp, _, _ := carol.Person()
	var err error
	p, err = w.alice.PromoteGroupMember(tctx(t), p.State.Conv, cp.Person)
	if err != nil {
		t.Fatal(err)
	}
	if err = carol.SyncGroupFromRoot(tctx(t), p.Root); err != nil {
		t.Fatal(err)
	}
	bob, _, _ := w.bob.Person()
	injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
	next, err := w.alice.RemoveGroupMember(tctx(t), p.State.Conv, bob.Person)
	if err == nil {
		t.Fatal("lost custody response was hidden")
	}
	if err = carol.SyncGroupFromRoot(tctx(t), p.Root); err != nil {
		t.Fatal(err)
	}
	remote, err := carol.RenameGroup(tctx(t), p.State.Conv, "Remote newer current context")
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.SyncGroupFromRoot(tctx(t), p.Root); err != nil {
		t.Fatal(err)
	}
	err = w.alice.RecoverGroupPublications(tctx(t))
	var published int
	w.alice.store.db.QueryRow("SELECT published FROM group_publications WHERE conv=? AND seq=?", p.State.Conv, next.State.Seq).Scan(&published)
	if err == nil || published != 0 {
		t.Fatalf("remote newer context completed an unpublished old fanout: %d %v", published, err)
	}
	newer, err := w.alice.RenameGroup(tctx(t), p.State.Conv, "Own newer atomically published context")
	if err != nil || newer.State.Seq != remote.State.Seq+1 {
		t.Fatal(err)
	}
	if err = w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	w.alice.store.db.QueryRow("SELECT published FROM group_publications WHERE conv=? AND seq=?", p.State.Conv, next.State.Seq).Scan(&published)
	current, err := w.alice.GroupContext(p.State.Conv)
	if err != nil || published != 1 || current.State.Hash() != newer.State.Hash() || len(terminalFixtureEnvelopes(t, w.alice, w.bob, p.State.Conv)) < 2 {
		t.Fatal("own newer published batch did not allow original terminal-only recovery", err)
	}
}

func groupGovernanceAwait(t *testing.T, packet GroupContext, agents ...*Agent) {
	t.Helper()
	for _, a := range agents {
		eventually(t, "exact governance context", func() bool {
			p, e := a.GroupContext(packet.State.Conv)
			return e == nil && p.State.Hash() == packet.State.Hash()
		})
	}
}
func groupGovernanceDeparture(t *testing.T, from, to *Agent) envelope.Envelope {
	t.Helper()
	var raw []byte
	var env envelope.Envelope
	if err := from.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub=? ORDER BY rowid DESC LIMIT 1`, to.Address, envelope.SubGroupWithdrawal).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env
}
func groupGovernanceDeliver(t *testing.T, from, to *Agent, env envelope.Envelope) {
	t.Helper()
	if err := from.uploadAll(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if err := from.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
		t.Fatal(err)
	}
	if err := to.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
}

func TestGroupGovernanceLinkedAdminAndTransfer(t *testing.T) {
	// This mesh runs its own concurrent daemons; avoid competing fixture meshes.
	w, carol, p, stops := groupTurnsFixture(t)
	phone, await, _ := linkPhone(t, w.alice, "governance-phone")
	if err := w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	stopPhone := runAgent(t, phone)
	defer stopPhone()
	publishGroupFixtureCaps(t, phone, true)
	copies, err := w.alice.groupDeliveryCopies(tctx(t), p)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	// Match publication's enqueue wake; readiness is the exact signed context
	// below, not a synchronous drain of every unrelated outbox lane.
	w.alice.kickNow()
	groupGovernanceAwait(t, p, phone)
	alice, _, _ := w.alice.store.selfPerson(w.alice.Address)
	bob, _, _ := w.bob.store.selfPerson(w.bob.Address)
	cp, _, _ := carol.store.selfPerson(carol.Address)
	if _, err = carol.RenameGroup(tctx(t), p.State.Conv, "unauthorized"); err == nil {
		t.Fatal("ordinary member renamed")
	}
	if _, err = carol.PromoteGroupMember(tctx(t), p.State.Conv, cp.roster.Person); err == nil {
		t.Fatal("ordinary member promoted itself")
	}
	original := p.Root
	p, err = phone.RenameGroup(tctx(t), p.State.Conv, "Linked administrator rename")
	if err != nil {
		t.Fatal(err)
	}
	if !sameGroupRoot(original, p.Root) || p.State.By != phone.Self().Fingerprint() {
		t.Fatal("rename replaced original root or did not use linked key")
	}
	groupGovernanceAwait(t, p, w.alice, w.bob, carol)
	p, err = phone.PromoteGroupMember(tctx(t), p.State.Conv, bob.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.alice, w.bob, carol)
	p, err = phone.PromoteGroupMember(tctx(t), p.State.Conv, cp.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.bob, carol)
	p, err = w.bob.DemoteGroupMember(tctx(t), p.State.Conv, cp.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, phone, w.alice, carol)
	p, err = phone.DemoteGroupMember(tctx(t), p.State.Conv, alice.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.alice, w.bob, carol)
	if _, err = phone.RenameGroup(tctx(t), p.State.Conv, "stale administrator"); err == nil {
		t.Fatal("demoted linked device retained administrator authority")
	}
	for _, action := range []func() error{
		func() error { _, e := w.bob.DemoteGroupMember(tctx(t), p.State.Conv, bob.roster.Person); return e },
		func() error { _, e := w.bob.RemoveGroupMember(tctx(t), p.State.Conv, bob.roster.Person); return e },
		func() error { _, e := w.bob.LeaveGroup(tctx(t), p.State.Conv); return e },
	} {
		if e := action(); !errors.Is(e, ErrGroupLastAdmin) {
			t.Fatalf("last administrator refusal: %v", e)
		}
	}
	p, err = w.bob.PromoteGroupMember(tctx(t), p.State.Conv, alice.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, p, w.alice, phone, carol)
	result, err := w.alice.LeaveGroup(tctx(t), p.State.Conv)
	if err != nil || result.Context == nil || result.Withdrawal != nil || result.Queued {
		t.Fatalf("administrator departure not exact CAS: %+v %v", result, err)
	}
	p = *result.Context
	groupGovernanceAwait(t, p, w.bob, carol)
	if _, ok := p.State.Member(alice.roster.Person); ok {
		t.Fatal("administrator self-removal did not remove whole person")
	}
	stops[w.bob]()
	stops[carol]()
	path, _ := writeFile(t, t.TempDir(), "queued-after-transfer.bin", 4096)
	sent, err := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "queued only for then-member Carol", Files: []OutgoingFile{{Path: path, Name: "queued-after-transfer.bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	old := groupTurnEnvelope(t, w.bob, sent.ID)
	p, err = w.bob.RemoveGroupMember(tctx(t), p.State.Conv, cp.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, e := w.bob.mayDeliver(old); allowed || e != nil {
		t.Fatalf("removed ordinary member regained queued file: %v %v", allowed, e)
	}

}

func TestGroupGovernanceOfflineLeaveRestartExactFanout(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	stops[w.bob]()
	path, _ := writeFile(t, t.TempDir(), "pre-leave-queued.bin", 2048)
	// SendConv attempts direct delivery even when its daemon is stopped. Stop
	// custody only after the required roster reads, before the queued write.
	beforeOutbox = func() { w.hub.Stop() }
	t.Cleanup(func() { beforeOutbox = func() {} })
	pending, err := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "unsent before own leave", Files: []OutgoingFile{{Path: path, Name: "pre-leave-queued.bin"}}})
	beforeOutbox = func() {}
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != stateQueued {
		t.Fatalf("pre-leave turn reached custody: %s", pending.State)
	}
	oldTurn := groupTurnEnvelope(t, w.bob, pending.ID)
	result, err := w.bob.LeaveGroup(tctx(t), p.State.Conv)
	if err != nil || !result.Queued || result.Withdrawal == nil {
		t.Fatalf("offline atomic leave: %+v %v", result, err)
	}
	original := *result.Withdrawal
	if allowed, e := w.bob.mayDeliver(oldTurn); allowed || e != nil {
		t.Fatalf("departure exception allowed ordinary queued file: %v %v", allowed, e)
	}
	var n int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n); err != nil || n != 2 {
		t.Fatalf("offline fanout not atomic: %d %v", n, err)
	}
	pins, err := w.bob.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 1 || !sameGroupWithdrawal(pins[0], original) {
		t.Fatal("offline pin not persisted with fanout")
	}
	if _, err = w.bob.GroupMembers(p.State.Conv); err == nil {
		t.Fatal("local departure not immediate")
	}
	home := w.bob.home
	w.bob.Close()
	b, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	w.bob = b
	again, err := b.LeaveGroup(tctx(t), p.State.Conv)
	if err != nil || again.Withdrawal == nil || !sameGroupWithdrawal(*again.Withdrawal, original) {
		t.Fatalf("restart changed departure signature: %v", err)
	}
	b.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n)
	if n != 2 {
		t.Fatal("restart duplicated semantic recipient fanout")
	}
	w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
	runAgent(t, b)
	// All three daemons reconnect to a fresh fixture Hub; only their current
	// sessions receive fixture grp1, while production capability remains absent.
	for _, a := range []*Agent{w.alice, b, carol} {
		publishGroupFixtureCaps(t, a, true)
	}
	eventually(t, "both peers persist departure overlay", func() bool {
		for _, a := range []*Agent{w.alice, carol} {
			pins, e := a.groupWithdrawals(p.State.Conv)
			if e != nil || !groupWithdrawalPinned(pins, original) {
				return false
			}
		}
		return true
	})
	for _, a := range []*Agent{w.alice, carol} {
		rows, e := a.ConversationMessages(p.State.Conv)
		if e != nil || len(rows) != 0 {
			t.Fatalf("quiet leave appeared as ordinary turn %v", e)
		}
	}
	next, err := w.alice.RenameGroup(tctx(t), p.State.Conv, "Leave folded into CAS")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.State.Member(original.Person); ok {
		t.Fatal("next CAS did not fold ordinary withdrawal")
	}
	groupGovernanceAwait(t, next, carol)
	sent, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "after departure"})
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range sent.Copies {
		if copy.To == b.Address {
			t.Fatal("departed person receives future turn")
		}
	}
	if _, err = b.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "silent rejoin"}); err == nil {
		t.Fatal("departure revived without fresh consent")
	}
	if err = b.RecoverGroupWithdrawals(tctx(t)); err != nil {
		t.Fatal(err)
	}
	b.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n)
	if n != 2 {
		t.Fatal("recovery rebroadcast duplicate departure")
	}
}

func TestGroupGovernanceLeaveReorderAndPromotionConflict(t *testing.T) {
	w, p, wdraw := ordinaryWithdrawalFixture(t)
	// Existing SignGroupWithdrawal now persists exact carrier bytes as well as pin.
	env := groupGovernanceDeparture(t, w.bob, w.alice)
	if _, err := w.alice.store.db.Exec(`DELETE FROM group_context WHERE conv=?`, p.State.Conv); err != nil {
		t.Fatal(err)
	}
	groupGovernanceDeliver(t, w.bob, w.alice, env)
	var reason string
	if err := w.alice.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, env.ID).Scan(&reason); err != nil || reason != reasonProof {
		t.Fatalf("missing context departure not pending: %q %v", reason, err)
	}
	c, err := groupProofRecord(w.alice.store.db, p.State.Conv, p.Root.Creator.Fingerprint, p.State.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AcceptGroupCommit(tctx(t), c); err != nil {
		t.Fatal(err)
	}
	w.alice.retryProof(tctx(t))
	pins, err := w.alice.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 1 || !sameGroupWithdrawal(pins[0], wdraw) {
		t.Fatal("reordered departure not admitted exactly once")
	}
	if err = w.alice.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE sub=?`, envelope.SubGroupWithdrawal).Scan(&n)
	if n != 1 {
		t.Fatal("quiet carrier duplicate timeline/receipt")
	}
	// Separate valid world: promotion wins the public CAS before ordinary leave
	// arrives. The old departure cannot remove or demote that current admin.
	w2, p2, w2draw := ordinaryWithdrawalFixture(t)
	promoted, err := w2.alice.PromoteGroupMember(tctx(t), p2.State.Conv, w2draw.Person)
	if err != nil {
		t.Fatal(err)
	}
	old := groupGovernanceDeparture(t, w2.bob, w2.alice)
	groupGovernanceDeliver(t, w2.bob, w2.alice, old)
	current, err := w2.alice.GroupContext(p2.State.Conv)
	if err != nil || current.State.Hash() != promoted.State.Hash() {
		t.Fatal("departure overwrote promotion CAS")
	}
	m, ok := current.State.Member(w2draw.Person)
	if !ok || !m.Admin {
		t.Fatal("ordinary departure removed promoted administrator")
	}
	pins, err = w2.alice.groupWithdrawals(p2.State.Conv)
	if err != nil || len(pins) != 0 {
		t.Fatal("promotion conflict installed withdrawal")
	}
	pending, err := groupWithdrawalRows(w2.alice.store.db, "group_pending_withdrawals", p2.State.Conv)
	if err != nil || len(pending) != 1 {
		t.Fatal("promotion conflict not retained for explicit resolution")
	}
}

func TestGroupGovernanceCASResponseLossExactRecovery(t *testing.T) {
	w, p := groupOrdinaryPublication(t)
	injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
	_, err := w.alice.RenameGroup(tctx(t), p.State.Conv, "Exact response-loss rename")
	if err == nil {
		t.Fatal("injected lost response hidden")
	}
	var original []byte
	if err = w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=? AND published=0`, p.State.Conv, p.State.Seq+1).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var recovered []byte
	w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=?`, p.State.Conv, p.State.Seq+1).Scan(&recovered)
	if !bytes.Equal(original, recovered) {
		t.Fatal("governance recovery re-signed/re-encrypted")
	}
	current, err := w.alice.GroupContext(p.State.Conv)
	if err != nil || current.State.Title != "Exact response-loss rename" {
		t.Fatal("committed governance not recovered")
	}
	got, _ := json.Marshal(current.State.Members)
	want, _ := json.Marshal(p.State.Members)
	if !bytes.Equal(got, want) {
		t.Fatal("rename mutated existing admissions")
	}
}

func TestGroupGovernanceDepartingAdminResponseLossRestart(t *testing.T) {
	w, p := groupOrdinaryPublication(t)
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	p, err = w.alice.PromoteGroupMember(tctx(t), p.State.Conv, bob.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
	_, err = w.alice.LeaveGroup(tctx(t), p.State.Conv)
	if err == nil {
		t.Fatal("lost departing-administrator custody hidden")
	}
	var original []byte
	if err = w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=? AND published=0`, p.State.Conv, p.State.Seq+1).Scan(&original); err != nil {
		t.Fatal(err)
	}
	home := w.alice.home
	w.alice.Close()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w.alice = a
	if err = a.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatalf("exact departing-administrator POST replay did not recover: %v", err)
	}
	var recovered []byte
	if err = a.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=? AND published=1`, p.State.Conv, p.State.Seq+1).Scan(&recovered); err != nil || !bytes.Equal(original, recovered) {
		t.Fatalf("departure recovery changed original bytes: %v", err)
	}
	current, err := a.GroupContext(p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	own, _, _ := a.store.selfPerson(a.Address)
	if _, ok := current.State.Member(own.roster.Person); ok {
		t.Fatal("exact committed departure did not install")
	}
	if err = a.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND sub='group-context' AND json_extract(body,'$.seq')=?`, p.State.Conv, p.State.Seq+1).Scan(&n); err != nil || n != 1 {
		t.Fatalf("departure restart duplicated/skipped fanout: %d %v", n, err)
	}
}
