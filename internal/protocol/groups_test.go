package protocol

import (
	"github.com/misunders2d/agentnet/internal/identity"
	"slices"
	"testing"
)

func groupFixture(t *testing.T) (ConvRoot, GroupState, GroupRosterResolver) {
	t.Helper()
	desk, dp := vecDesk()
	phone, pp := vecPhone()
	r0 := vecRoster()
	r1 := PersonRoster{Person: vecOther, Label: "Phone", Devices: []identity.Public{pp}}
	r1.Sign(phone.Sign)
	root := ConvRoot{V: GroupRootVersion, Kind: ConvKindGroup, Creator: ConvCreator{Person: r0.Person, Roster: r0.Hash(), Address: dp.Address, Fingerprint: dp.Fingerprint()}, Members: []ConvMember{{Person: r0.Person, Roster: r0.Hash()}, {Person: r1.Person, Roster: r1.Hash()}}, Nonce: vecSession, Created: 1790000000, Realm: vecSession, Title: "Team", Admins: []string{r0.Person}}
	root.Sign(desk.Sign)
	state := GroupState{V: 1, Conv: root.ID(), Realm: root.Realm, Title: root.Title, Actor: r0.Person, ActorRoster: r0.Hash(), By: dp.Fingerprint()}
	for _, r := range []PersonRoster{r0, r1} {
		a := GroupAdmission{Conv: state.Conv, Realm: state.Realm, Person: r.Person, Roster: r.Hash(), By: r.Devices[0].Fingerprint()}
		if r.Person == r0.Person {
			a.Sign(desk.Sign)
		} else {
			a.Sign(phone.Sign)
		}
		state.Members = append(state.Members, GroupMember{ConvMember: ConvMember{Person: r.Person, Roster: r.Hash()}, Admin: r.Person == r0.Person, Admission: a})
	}
	state.Sign(desk.Sign)
	resolve := func(p, h string) (PersonRoster, bool) {
		for _, r := range []PersonRoster{r0, r1} {
			if r.Person == p && r.Hash() == h {
				return r, true
			}
		}
		return PersonRoster{}, false
	}
	return root, state, resolve
}

func TestGroupSignedConsentAndAuthority(t *testing.T) {
	root, s, resolve := groupFixture(t)
	if err := s.Verify(root, nil, resolve, nil); err != nil {
		t.Fatal(err)
	}
	desk, _ := vecDesk()
	phone, pp := vecPhone()
	next := s
	next.Members = slices.Clone(s.Members)
	next.Seq = 1
	next.Prev = s.Hash()
	next.Title = "Changed"
	next.Sign(desk.Sign)
	if err := next.Verify(root, &s, resolve, nil); err != nil {
		t.Fatal(err)
	}
	forged := next
	forged.Actor = vecOther
	forged.ActorRoster = s.Members[1].Roster
	forged.By = pp.Fingerprint()
	forged.Sign(phone.Sign)
	if forged.Verify(root, &s, resolve, nil) == nil {
		t.Fatal("nonadmin transition accepted")
	}
	forged = next
	forged.Members = slices.Clone(next.Members)
	forged.Members[1].Admission.History = []GroupHistoryRef{{LID: vecSession, Author: pp.Fingerprint(), Hash: s.Hash()}}
	forged.Sign(desk.Sign)
	if forged.Verify(root, &s, resolve, nil) == nil {
		t.Fatal("admin forged member history consent")
	}
	noadmin := next
	noadmin.Members = slices.Clone(next.Members)
	noadmin.Members[0].Admin = false
	noadmin.Sign(desk.Sign)
	if noadmin.Verify(root, &s, resolve, nil) == nil {
		t.Fatal("last admin removed")
	}
}

func TestGroupWithdrawalAdmissionReplay(t *testing.T) {
	root, s, resolve := groupFixture(t)
	desk, _ := vecDesk()
	phone, pp := vecPhone()
	m := s.Members[1]
	w := GroupWithdrawal{Conv: s.Conv, Realm: s.Realm, Person: m.Person, Admission: m.Admission.Hash(), Roster: m.Roster, By: pp.Fingerprint()}
	w.Sign(phone.Sign)
	if err := w.Verify(s, resolve); err != nil {
		t.Fatal(err)
	}
	if len(s.EffectiveMembers([]GroupWithdrawal{w})) != 1 {
		t.Fatal("leave ignored")
	}
	forged := w
	forged.Person = s.Members[0].Person
	forged.Sign(phone.Sign)
	if forged.Verify(s, resolve) == nil {
		t.Fatal("withdrawal affected someone else")
	}
	next := s
	next.Members = slices.Clone(s.Members)
	next.Seq = 1
	next.Prev = s.Hash()
	next.Sign(desk.Sign)
	if err := next.Verify(root, &s, resolve, []GroupWithdrawal{w}); err != nil {
		t.Fatal(err)
	}
	if len(next.EffectiveMembers([]GroupWithdrawal{w})) != 1 {
		t.Fatal("higher seq resurrected admission")
	}
	next.Members[1].Admission.Seq = 1
	next.Members[1].Admission.Prev = next.Prev
	next.Members[1].Admission.Sign(phone.Sign)
	next.Sign(desk.Sign)
	if err := next.Verify(root, &s, resolve, []GroupWithdrawal{w}); err != nil {
		t.Fatal(err)
	}
	if len(next.EffectiveMembers([]GroupWithdrawal{w})) != 2 {
		t.Fatal("fresh consent did not rejoin")
	}
}

func TestGroupHeaderCiphertextBinding(t *testing.T) {
	root, s, resolve := groupFixture(t)
	desk, dp := vecDesk()
	c := GroupCommit{Bootstrap: root.Creator.Fingerprint, V: 1, Conv: s.Conv, Realm: s.Realm, Hash: s.Hash(), Admins: s.Admins(), Writer: dp.Address, Actor: s.Actor, ActorRoster: s.ActorRoster, Ciphertext: []byte("synthetic encrypted payload")}
	c.Sign(desk.Sign)
	if err := c.VerifyChain(root, nil, resolve); err != nil {
		t.Fatal(err)
	}
	c.Ciphertext[0] ^= 1
	if c.Verify(dp) == nil {
		t.Fatal("changed ciphertext accepted")
	}
}

func TestGroupSelectedHistoryExactBoundary(t *testing.T) {
	_, s, _ := groupFixture(t)
	a := s.Members[1].Admission
	ref := GroupHistoryRef{LID: vecSession, Author: a.By, Hash: s.Hash()}
	if a.AllowsHistory(ref) {
		t.Fatal("empty history grant exposed transcript")
	}
	a.History = []GroupHistoryRef{ref}
	if !a.AllowsHistory(ref) {
		t.Fatal("selected history missing")
	}
	wrong := ref
	wrong.Hash = vecRoster().Hash()
	if a.AllowsHistory(wrong) {
		t.Fatal("different body hash granted")
	}
	wrong = ref
	wrong.Author = s.Members[0].Admission.By
	if a.AllowsHistory(wrong) {
		t.Fatal("different author granted")
	}
	wrong = ref
	wrong.LID = vecPerson
	if a.AllowsHistory(wrong) {
		t.Fatal("different logical turn granted")
	}
}
