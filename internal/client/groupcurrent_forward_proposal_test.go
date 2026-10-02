package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupCurrentForwardProofFetchRetry(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	faults := injectFaults(w.bob)
	faults.add("GET", "/v1/groups/", 1, false)
	if err := w.bob.AcceptGroupContext(tctx(t), p); err == nil {
		t.Fatal("missing proof fetch did not fail")
	}
	if _, err := w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("partial proof installed context: %v", err)
	}
	root, err := w.bob.groupProofRoot(p.State.Conv)
	if err != nil || root.ID() != p.Root.ID() {
		t.Fatalf("verified root progress not retained: %v", err)
	}
	if err = w.bob.AcceptGroupContext(tctx(t), p); err != nil {
		t.Fatal(err)
	}
	current, err := w.bob.GroupContext(p.State.Conv)
	if err != nil || len(current.Proof) != 0 || current.State.Hash() != p.State.Hash() {
		t.Fatalf("current-only recovery failed: %v", err)
	}
	// Already durable original proof requires no new relay proof permission.
	faults.add("GET", "/v1/groups/", 1, false)
	if err = w.bob.AcceptGroupContext(tctx(t), p); err != nil {
		t.Fatalf("cached exact proof replay failed: %v", err)
	}
	if _, err = w.bob.GroupMembers(p.State.Conv); err != nil {
		t.Fatal(err)
	}
}

func TestGroupCurrentForwardSyncFromDurableRoot(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("root alone granted context: %v", err)
	}
	home := w.bob.home
	if err := w.bob.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	// The daemon owns this callback; this reopened method fixture records
	// the same wake without starting a competing stream worker.
	wakes := 0
	reopened.kick = func() { wakes++ }
	hint := protocol.GroupHead{Conv: p.State.Conv, Bootstrap: p.Root.Creator.Fingerprint, Seq: 0, Hash: p.State.Hash()}
	raw, _ := json.Marshal([]protocol.GroupHead{hint})
	reopened.onGroupHeads(raw)
	reopened.groupWork.mu.Lock()
	due := reopened.groupWork.due[p.State.Conv]
	reopened.groupWork.mu.Unlock()
	if !due || wakes != 1 {
		t.Fatal("durable-root-only reader ignored authenticated current-head wake")
	}
	foreign := hint
	foreign.Bootstrap = strings.Repeat("e", 64)
	if err = reopened.NoteGroupHead(foreign); err == nil {
		t.Fatal("pending reader accepted foreign bootstrap hint")
	}
	if err = reopened.SyncGroup(tctx(t), p.State.Conv); err != nil {
		t.Fatal(err)
	}
	current, err := reopened.GroupContext(p.State.Conv)
	if err != nil || current.State.Hash() != p.State.Hash() {
		t.Fatalf("durable verified root did not recover current context: %v", err)
	}
	if err = reopened.SyncGroup(tctx(t), strings.Repeat("f", 64)); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("unknown root inferred authority: %v", err)
	}
}

func TestGroupCurrentForwardCurrentHeadsAndStateBinding(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.AcceptGroupContext(tctx(t), p); err != nil {
		t.Fatal(err)
	}
	next := p
	next.Proof = nil
	next.State.Seq = 1
	next.State.Prev = p.State.Hash()
	next.State.Title = "Current"
	var err error
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	record, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), record, next); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.NoteGroupHead(protocol.GroupHead{Conv: p.State.Conv, Bootstrap: p.Root.Creator.Fingerprint, Seq: 1, Hash: next.State.Hash()}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.AcceptGroupContext(tctx(t), p); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("newer observed head accepted stale forwarded state: %v", err)
	}
	if err = w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{record}}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.AcceptGroupContext(tctx(t), p); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("newer durable proof accepted stale forwarded state: %v", err)
	}
	if err = w.bob.AcceptGroupContext(tctx(t), next); err != nil {
		t.Fatal(err)
	}
	bad := next
	bad.State.Title = "Signed but never committed"
	bad.State.Sign(w.alice.id.Sign)
	if err = w.bob.AcceptGroupContext(tctx(t), bad); err == nil {
		t.Fatal("state differing from exact original record accepted")
	}
	bad = next
	bad.Proof = []protocol.GroupState{p.State}
	if err = w.bob.AcceptGroupContext(tctx(t), bad); err == nil {
		t.Fatal("forwarder supplied plaintext prior membership proof")
	}
	foreign := next
	foreign.Root.Realm = protocol.NewID()
	foreign.Root.Sign(w.alice.id.Sign)
	if err = w.bob.AcceptGroupContext(tctx(t), foreign); err == nil {
		t.Fatal("foreign/root substitution accepted")
	}
	stored, err := w.bob.GroupContext(p.State.Conv)
	if err != nil || stored.State.Hash() != next.State.Hash() {
		t.Fatalf("refusals changed current state: %v", err)
	}
}

func TestGroupCurrentForwardPendingMissingCurrentAuthority(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	gap := p
	gap.State.Seq = 2
	gap.State.Prev = first.Hash
	gap.State.Sign(w.alice.id.Sign)
	if err := w.bob.AcceptGroupContext(tctx(t), gap); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("missing authority did not stay pending: %v", err)
	}
	if _, err := w.bob.GroupMembers(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("partial prefix granted membership: %v", err)
	}
	outsider := proofReader(t, w, "outside-current")
	if err := outsider.AcceptGroupContext(tctx(t), p); err == nil {
		t.Fatal("snapshot recipient absent from current membership accepted")
	}
	if _, err := outsider.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("outside target installed membership: %v", err)
	}
}

func TestGroupCurrentForwardOrdinaryProofACLUnchanged(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	next := p
	next.Proof = nil
	next.State.Seq = 1
	next.State.Prev = p.State.Hash()
	next.State.Members = append([]protocol.GroupMember{}, p.State.Members...)
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	for i := range next.State.Members {
		if next.State.Members[i].Person == bob.roster.Person {
			next.State.Members[i].Admin = false
		}
	}
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	record, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), record, next); err != nil {
		t.Fatal(err)
	}
	err = w.bob.AcceptGroupContext(tctx(t), next)
	var denied *HubError
	if !errors.As(err, &denied) || denied.Status != 403 {
		t.Fatalf("ordinary proof read changed relay authority: %v", err)
	}
	if _, err = w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("denied proof installed context: %v", err)
	}
	// Existing proof ingress remains sufficient; this is a synthetic method
	// boundary, not an implemented sender/runtime encrypted transfer flow.
	if err = w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{first, record}}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.AcceptGroupContext(tctx(t), next); err != nil {
		t.Fatal(err)
	}
	if members, e := w.bob.GroupMembers(p.State.Conv); e != nil || len(members) != 2 {
		t.Fatalf("verified ordinary member unusable: %v", e)
	}
}

func TestGroupCurrentForwardWithdrawalOverlayAndLinkedTarget(t *testing.T) {
	w, p, withdrawal := ordinaryWithdrawalFixture(t)
	with := p
	with.Proof = nil
	with.Withdrawals = []protocol.GroupWithdrawal{withdrawal}
	if err := w.alice.AcceptGroupContext(tctx(t), with); err != nil {
		t.Fatal(err)
	}
	pins, err := w.alice.groupWithdrawals(p.State.Conv)
	if err != nil || len(pins) != 1 || !sameGroupWithdrawal(pins[0], withdrawal) {
		t.Fatalf("accepted withdrawal not atomically pinned: %v", err)
	}
	before, err := w.bob.GroupContext(p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.AcceptGroupContext(tctx(t), with); err == nil {
		t.Fatal("withdrawn target accepted current forwarded context")
	}
	after, err := w.bob.GroupContext(p.State.Conv)
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if err != nil || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("withdrawn rejection changed context: %v", err)
	}
}

func TestGroupCurrentForwardNewLinkedAdminGetsNoHistoricalKeys(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	phone, awaited, _ := linkPhone(t, w.alice, "current-forward-phone")
	request := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-awaited:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("linked admin approval timeout")
	}
	if err := phone.SyncGroupFromRoot(tctx(t), p.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("original ciphertext disclosed context to new key: %v", err)
	}
	if _, err := phone.DecodeGroupCommit(tctx(t), first); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("linked key decrypted historical ciphertext: %v", err)
	}
	if err := phone.AcceptGroupContext(tctx(t), p); err != nil {
		t.Fatal(err)
	}
	current, err := phone.GroupContext(p.State.Conv)
	if err != nil || len(current.Proof) != 0 || current.State.Hash() != p.State.Hash() {
		t.Fatalf("linked current-only context failed: %v", err)
	}
	if _, err = phone.DecodeGroupCommit(tctx(t), first); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("forwarding supplied historical decryption key: %v", err)
	}
	if err = w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = phone.AcceptGroupContext(tctx(t), p); err == nil {
		t.Fatal("removed linked device accepted forwarded state")
	}
}
