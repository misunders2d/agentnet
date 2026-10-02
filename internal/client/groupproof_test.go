package client

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func proofReader(t *testing.T, w *world, name string) *Agent {
	t.Helper()
	reader := mustJoin(t, filepath.Join(t.TempDir(), name), w.aliceInvites(name), name)
	if _, err := reader.CreatePerson(tctx(t), name); err != nil {
		t.Fatal(err)
	}
	return reader
}

func sealedCurrentProof(t *testing.T, writer *Agent, reader *Agent, packet GroupContext) protocol.GroupCommit {
	t.Helper()
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	output, err := age.Encrypt(&encrypted, writer.id.Box.Recipient(), reader.id.Box.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = output.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	s := packet.State
	record := protocol.GroupCommit{V: 1, Bootstrap: packet.Root.Creator.Fingerprint, Conv: s.Conv, Realm: s.Realm, Seq: s.Seq, Prev: s.Prev, Hash: s.Hash(), Admins: s.Admins(), Actor: s.Actor, ActorRoster: s.ActorRoster, Writer: writer.Address, Ciphertext: encrypted.Bytes()}
	record.Sign(writer.id.Sign)
	return record
}

func TestGroupOriginalProofPagesResumeAndFreshCurrent(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	reader := proofReader(t, w, "fresh-reader")
	const steps = 1024
	records := []protocol.GroupCommit{first}
	for i := 1; i < steps; i++ {
		record := first
		record.Seq = int64(i)
		record.Prev = records[i-1].Hash
		record.Hash = fmt.Sprintf("%064x", i+4096)
		// Historical ciphertext is opaque original signed data. Fresh readers
		// verify authority, not its hidden membership plaintext.
		record.Ciphertext = append([]byte{}, first.Ciphertext...)
		record.Sign(w.alice.id.Sign)
		records = append(records, record)
	}
	ref := protocol.GroupHistoryRef{LID: protocol.NewID(), Author: w.alice.id.Public(w.alice.Address).Fingerprint(), Hash: strings.Repeat("e", 64)}
	admission, err := reader.SignGroupAdmission(initial.Root, steps, records[len(records)-1].Hash, []protocol.GroupHistoryRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	person, ok, err := reader.store.selfPerson(reader.Address)
	if err != nil || !ok {
		t.Fatal(err)
	}
	current := initial.State
	current.Members = append([]protocol.GroupMember{}, initial.State.Members...)
	current.Seq = steps
	current.Prev = records[len(records)-1].Hash
	current.Title = "Current only"
	current.Members = append(current.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.roster.Person, Roster: person.roster.Hash()}, Admission: admission})
	slices.SortFunc(current.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	current.Sign(w.alice.id.Sign)
	packet := GroupContext{Root: initial.Root, State: current}
	final := sealedCurrentProof(t, w.alice, reader, packet)
	records = append(records, final)
	if err = reader.AcceptGroupCommit(tctx(t), final); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("missing authority accepted: %v", err)
	}
	for start := 0; start < len(records); start += 16 {
		end := min(start+16, len(records))
		page := protocol.GroupJournalPage{Records: records[start:end], More: end < len(records)}
		if err = reader.IngestGroupProofPage(tctx(t), initial.Root, page); err != nil {
			t.Fatalf("page %d: %v", start, err)
		}
		if err = reader.IngestGroupProofPage(tctx(t), initial.Root, page); err != nil {
			t.Fatalf("duplicate page %d: %v", start, err)
		}
		if start == 256 {
			if _, err = reader.GroupMembers(initial.State.Conv); !errors.Is(err, ErrGroupContextPending) {
				t.Fatalf("partial proof granted membership: %v", err)
			}
			home := reader.home
			if err = reader.Close(); err != nil {
				t.Fatal(err)
			}
			reader, err = Open(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reader.Close() })
		}
	}
	if err = reader.AcceptGroupCommit(tctx(t), final); err != nil {
		t.Fatal(err)
	}
	stored, err := reader.GroupContext(initial.State.Conv)
	if err != nil || len(stored.Proof) != 0 || stored.State.Hash() != current.Hash() {
		t.Fatalf("fresh snapshot %v", err)
	}
	members, err := reader.GroupMembers(initial.State.Conv)
	if err != nil || len(members) != 3 {
		t.Fatalf("fresh membership %d %v", len(members), err)
	}
	allowed, err := reader.GroupHistoryAllows(current.Conv, person.roster.Person, ref)
	if err != nil || !allowed {
		t.Fatalf("exact selected-history grant %v", err)
	}
	changed := ref
	changed.Hash = strings.Repeat("f", 64)
	if allowed, err = reader.GroupHistoryAllows(current.Conv, person.roster.Person, changed); err != nil || allowed {
		t.Fatalf("history hash substitution accepted: %v", err)
	}
	if err = reader.NoteGroupHead(protocol.GroupHead{Conv: current.Conv, Bootstrap: initial.Root.Creator.Fingerprint, Seq: current.Seq + 1, Hash: strings.Repeat("f", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.GroupMembers(current.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("newer head allowed current membership: %v", err)
	}
	var count int
	if err = reader.store.db.QueryRow("SELECT count(*) FROM group_proof_records WHERE conv=?", current.Conv).Scan(&count); err != nil || count != steps+1 {
		t.Fatalf("durable records %d %v", count, err)
	}
	mutated := packet
	mutated.State.Members = append([]protocol.GroupMember{}, packet.State.Members...)
	for i := range mutated.State.Members {
		if mutated.State.Members[i].Person == person.roster.Person {
			m := &mutated.State.Members[i]
			m.Admission.Prev = records[0].Hash
			m.Admission.Sign(reader.id.Sign)
		}
	}
	mutated.State.Sign(w.alice.id.Sign)
	// Direct current verifier checks exact admission slot, not just its signature.
	resolve, err := reader.groupResolver(tctx(t), mutated)
	if err != nil {
		t.Fatal(err)
	}
	authority := final
	authority.Hash = mutated.State.Hash()
	if err = mutated.State.VerifyCurrent(initial.Root, authority, resolve, func(seq int64) (protocol.GroupCommit, bool) {
		if seq < 0 || seq >= int64(len(records)) {
			return protocol.GroupCommit{}, false
		}
		return records[seq], true
	}, nil); err == nil {
		t.Fatal("admission predecessor substitution accepted")
	}
}

func TestGroupOriginalProofRejectsTamperForkAndBounds(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	reader := proofReader(t, w, "proof-checks")
	if err := reader.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{first}}); err != nil {
		t.Fatal(err)
	}
	legacyCanonical := first
	legacyCanonical.Sig = nil
	legacyJSON, _ := json.Marshal(legacyCanonical)
	if !ed25519.Verify(w.alice.id.Public(w.alice.Address).SignKey, append([]byte("agentnet-group-commit-v1\n"), legacyJSON...), first.Sig) {
		t.Fatal("original full-ciphertext signature bytes changed")
	}
	next := first
	next.Seq = 1
	next.Prev = first.Hash
	next.Hash = strings.Repeat("a", 64)
	next.Sign(w.alice.id.Sign)
	for _, kind := range []string{"stripped", "tampered", "creator", "realm", "sequence", "predecessor", "admin", "fork"} {
		t.Run(kind, func(t *testing.T) {
			bad := next
			switch kind {
			case "stripped":
				bad.Ciphertext = nil
			case "tampered":
				bad.Ciphertext = append([]byte{}, bad.Ciphertext...)
				bad.Ciphertext[0] ^= 1
			case "creator":
				bad.Bootstrap = strings.Repeat("d", 64)
				bad.Sign(w.alice.id.Sign)
			case "realm":
				bad.Realm = protocol.NewID()
				bad.Sign(w.alice.id.Sign)
			case "sequence":
				bad.Seq = 2
				bad.Sign(w.alice.id.Sign)
			case "predecessor":
				bad.Prev = strings.Repeat("f", 64)
				bad.Sign(w.alice.id.Sign)
			case "admin":
				person, _, _ := reader.store.selfPerson(reader.Address)
				bad.Actor = person.roster.Person
				bad.ActorRoster = person.roster.Hash()
				bad.Writer = reader.Address
				bad.Sign(reader.id.Sign)
			case "fork":
				bad = first
				bad.Hash = strings.Repeat("a", 64)
				bad.Sign(w.alice.id.Sign)
			}
			if err := reader.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{bad}}); err == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
	large := next
	large.Ciphertext = bytes.Repeat([]byte{1}, protocol.MaxGroupCiphertext)
	large.Sign(w.alice.id.Sign)
	later := large
	later.Seq++
	later.Prev = large.Hash
	later.Hash = strings.Repeat("b", 64)
	later.Sign(w.alice.id.Sign)
	if err := reader.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{large, later}}); err == nil {
		t.Fatal("oversized encoded page accepted")
	}
	if err := reader.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: make([]protocol.GroupCommit, 17)}); err == nil {
		t.Fatal("record count bound ignored")
	}
	if err := reader.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{More: true}); err == nil {
		t.Fatal("empty continuation accepted")
	}
	var count int
	if err := reader.store.db.QueryRow("SELECT count(*) FROM group_proof_records").Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid proof changed durable progress %d %v", count, err)
	}
}

func TestGroupNewPacketOmitsPriorPlaintextAndExistingReaderNeedsSteps(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, initial); err != nil {
		t.Fatal(err)
	}
	next := initial
	next.Proof = []protocol.GroupState{initial.State}
	next.State.Seq = 1
	next.State.Prev = initial.State.Hash()
	next.State.Title = "Fresh"
	next, err := w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	record, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := w.alice.decodeGroupCommit(tctx(t), record, &next)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Proof) != 0 {
		t.Fatal("future packet includes prior plaintext snapshots")
	}
	if _, err = w.alice.PublishGroup(tctx(t), record, next); err != nil {
		t.Fatal(err)
	}
	gap := decoded
	gap.State.Seq = 3
	gap.State.Prev = strings.Repeat("a", 64)
	gap.State.Sign(w.alice.id.Sign)
	resolve, err := w.alice.groupResolver(tctx(t), gap)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.verifyGroupPacket(tctx(t), gap, resolve); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("existing reader skipped missing transition: %v", err)
	}
	// Larger verified proof head must block previous local membership.
	later := record
	later.Seq = 2
	later.Prev = record.Hash
	later.Hash = strings.Repeat("b", 64)
	later.Sign(w.alice.id.Sign)
	if err = w.alice.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{later}}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.GroupMembers(initial.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("newer durable proof allowed stale membership: %v", err)
	}
}

func TestGroupLegacyPlaintextProofStillReadable(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	legacy := initial
	legacy.Proof = []protocol.GroupState{initial.State}
	legacy.State.Seq = 1
	legacy.State.Prev = initial.State.Hash()
	legacy.State.Title = "Legacy"
	legacy.State.Sign(w.alice.id.Sign)
	record := sealedCurrentProof(t, w.alice, w.bob, legacy)
	if err := w.bob.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{first, record}}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.AcceptGroupCommit(tctx(t), record); err != nil {
		t.Fatal(err)
	}
	current, err := w.bob.GroupContext(initial.State.Conv)
	if err != nil || len(current.Proof) != 1 {
		t.Fatalf("legacy context unreadable %v", err)
	}
}

func TestGroupStaleBootstrapRequiresFreshRootConsent(t *testing.T) {
	w, initial, _ := newGroupPublicationFixture(t)
	own, ok, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil || !ok {
		t.Fatal(err)
	}
	roster := own.roster
	roster.Seq++
	roster.Prev = own.roster.Hash()
	roster.Label = "Changed"
	roster.By = w.alice.id.Public(w.alice.Address).Fingerprint()
	roster.Sign(w.alice.id.Sign)
	raw, _ := json.Marshal(roster)
	if _, err = w.alice.store.pinChain(roster.Person, [][]byte{raw}, w.alice.Self(), true); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.publishPerson(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.SignGroupState(tctx(t), initial); err == nil || !strings.Contains(err.Error(), "rebuild root") {
		t.Fatalf("stale root silently signed: %v", err)
	}
	if _, err = w.alice.BuildGroupCommit(tctx(t), initial); err == nil || !strings.Contains(err.Error(), "rebuild root") {
		t.Fatalf("stale first publication accepted: %v", err)
	}
	if _, err = w.alice.SignGroupAdmission(initial.Root, 0, "", nil); err == nil || !strings.Contains(err.Error(), "fresh consent") {
		t.Fatalf("stale creator admission accepted: %v", err)
	}
}

func TestGroupProofRejectsConflictingPinnedPerson(t *testing.T) {
	w, initial, first := newGroupPublicationFixture(t)
	reader := proofReader(t, w, "person-conflict")
	if err := reader.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{first}}); err != nil {
		t.Fatal(err)
	}
	freeze(t, reader, w.alice)
	if err := reader.IngestGroupProofPage(tctx(t), initial.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{first}}); !errors.Is(err, errPersonConflict) {
		t.Fatalf("conflicting pinned authority accepted: %v", err)
	}
}
