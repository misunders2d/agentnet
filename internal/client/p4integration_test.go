package client

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestProposalOutcomeBeforeTrailersNeverCloses(t *testing.T) {
	for _, trailers := range []string{"\ntopic: done\nreaction: 👍", "\nreaction: 👍\ntopic: done"} {
		outcome, rest := outcomeOf(proposeMarker + "\nUpdate the changelog." + trailers)
		if outcome != outcomeProposal {
			t.Fatal("proposal marker lost before trailer parsing")
		}
		body, reaction, done := splitTrailers(rest, envelope.StatusProposal)
		if body != "Update the changelog." || reaction == nil || reaction.emoji != "👍" || done {
			t.Fatalf("proposal trailers: %q %+v done=%v", body, reaction, done)
		}
		body, _, done = splitTrailers(rest, envelope.StatusDone)
		if body != "Update the changelog." || !done {
			t.Fatalf("successful answer no longer closes: %q %v", body, done)
		}
	}
}

// A harmless roster update must preserve both standing person questions and
// trusted-own-device tasks. Losing native trust demotes only automatic tasks.
func TestPersonReconciliationPreservesOwnTaskAndPersonQuestion(t *testing.T) {
	s := securityStore(t)
	owner, _ := identity.Generate()
	desk, _ := identity.Generate()
	self, sender := owner.Public("owner/laptop"), desk.Public("owner/desk")
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Owner", Devices: []identity.Public{self}}
	r.Sign(owner.Sign)
	pin := func(record protocol.PersonRoster, adopt bool) {
		t.Helper()
		raw, _ := json.Marshal(record)
		if _, err := s.pinChain(record.Person, [][]byte{raw}, self, adopt); err != nil {
			t.Fatal(err)
		}
	}
	pin(r, true)
	joined := protocol.PersonRoster{Person: r.Person, Label: r.Label, Seq: 1, Prev: r.Hash(), Devices: []identity.Public{self, sender}, HumanKeys: r.Humans(), By: self.Fingerprint()}
	joined.Join = ed25519.Sign(desk.Sign, protocol.JoinBytes(joined.Person, joined.Seq, joined.Prev, sender))
	joined.Sign(owner.Sign)
	pin(joined, false)
	if err := s.pin(sender); err != nil {
		t.Fatal(err)
	}
	trust, _ := json.Marshal([]map[string]string{{"address": sender.Address, "fingerprint": sender.Fingerprint()}})
	if err := s.setConfig(map[string]string{"self_consent": string(trust)}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = setPersonGrant(tx, r.Person, "questions", true); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	add := func(kind string) string {
		t.Helper()
		in := taskFrom(sender.Address, self.Address)
		in.Kind = kind
		if err := s.addInbox(in, sender.Fingerprint()); err != nil {
			t.Fatal(err)
		}
		if state, _ := s.jobState(in.ID); state != statePending {
			t.Fatalf("new %s: %s", kind, state)
		}
		return in.ID
	}
	task, question := add(envelope.KindTask), add(envelope.KindQuestion)
	renamed := joined
	renamed.Seq, renamed.Prev, renamed.Join = 2, joined.Hash(), nil
	renamed.Label = "Renamed owner"
	renamed.Sign(owner.Sign)
	pin(renamed, false) // runs both source-transition reconciliation paths
	want := map[string]bool{task: true, question: true}
	for i := 0; i < 2; i++ {
		j, ok, err := s.claimJob("stub")
		if err != nil || !ok || !want[j.ID] {
			t.Fatalf("combined claim: %+v %v %v", j, ok, err)
		}
		delete(want, j.ID)
	}
	pending := add(envelope.KindTask)
	if err := s.setConfig(map[string]string{"self_consent": "[]"}); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = demotePersonJobs(tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.jobState(pending); state != stateAwaiting {
		t.Fatalf("lost trust still pending: %s", state)
	}
	if j, ok, err := s.claimJob("stub"); err != nil || ok {
		t.Fatalf("untrusted task claimed: %+v %v %v", j, ok, err)
	}
}
