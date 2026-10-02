package protocol

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
)

const TeamVectorCanonical = `agentnet-team-v1
{"v":1,"realm_id":"00112233445566778899aabbccddeeff","team":"fedcba9876543210fedcba9876543210","seq":0,"prev":"","author":{"person":"0123456789abcdef0123456789abcdef","roster":"f78b94d0e4bf9f75df8a53076cb188f257acf1b660df845f0da670fdeef9f755","address":"vitalii/desk","fingerprint":"19c77bce-aca933c7-80e1c0e9-e46fc988"},"op":"create","name":"Ops \u003c\u0026\u003e Ю","ts":1790000000}`
const TeamVectorHash = "3e062daa30eb7a21dfae4b1b9a705fd791ef46f5086732ba6fbba7ddb4f2d89b"
const TeamVectorSignature = "a2ae5e29bc0f093af6a4ad0db44b90e6454934ea380371e367d567a872c60b8560508274b14af80c8bd5cf51e4f2a727517eed5e5734821c50b3503afc1ec808"

func TestTeamCanonicalVector(t *testing.T) {
	r := vecRoster()
	id, pub := vecDesk()
	s := TeamStep{V: 1, RealmID: vecSession, Team: vecOther, Author: EventAuthor{Person: r.Person, Roster: r.Hash(), Address: pub.Address, Fingerprint: pub.Fingerprint()}, Op: TeamCreate, Name: "Ops <&> Ю", TS: 1790000000}
	s.Sign(id.Sign)
	if string(s.Canonical()) != TeamVectorCanonical || s.Hash() != TeamVectorHash || hex.EncodeToString(s.Sig) != TeamVectorSignature {
		t.Fatalf("team browser vector mismatch: %s\n%s\n%x", s.Canonical(), s.Hash(), s.Sig)
	}
	state, err := s.Apply(nil, r)
	if err != nil || !state.Manager(r.Person) || !state.Member(r.Person) {
		t.Fatalf("create author not manager/member: %+v %v", state, err)
	}
	raw, _ := json.Marshal(s)
	if _, err := ParseTeamStep(raw); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(raw), `"v":1`, `"v":1,"members":[]`, 1)
	if _, err := ParseTeamStep([]byte(bad)); err == nil {
		t.Fatal("accepted unsigned membership snapshot field")
	}
	s.RealmID = NewID()
	if _, err := s.Apply(nil, r); err == nil {
		t.Fatal("realm was not signature bound")
	}
}

type teamTestPerson struct {
	id *identity.Identity
	r  PersonRoster
}

func teamPerson(t *testing.T, label string) teamTestPerson {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	r := PersonRoster{Person: NewID(), Label: label, Devices: []identity.Public{id.Public(label + "/desk")}}
	r.Sign(id.Sign)
	return teamTestPerson{id, r}
}
func teamStepFor(prev *TeamState, by teamTestPerson, op, target, name string) TeamStep {
	s := TeamStep{V: 1, RealmID: vecSession, Team: vecOther, Author: EventAuthor{Person: by.r.Person, Roster: by.r.Hash(), Address: by.r.Devices[0].Address, Fingerprint: by.r.Devices[0].Fingerprint()}, Op: op, Target: target, Name: name, TS: 1790000000}
	if prev != nil {
		s.Seq = prev.Seq + 1
		s.Prev = prev.Hash
	}
	s.Sign(by.id.Sign)
	return s
}

func TestTeamAuthorityAndExplicitManagerLifecycle(t *testing.T) {
	a, b := teamPerson(t, "alice"), teamPerson(t, "bob")
	create := teamStepFor(nil, a, TeamCreate, "", "Support")
	state, err := create.Apply(nil, a.r)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(by teamTestPerson, op, target, name string, want error) {
		t.Helper()
		s := teamStepFor(&state, by, op, target, name)
		next, err := s.Apply(&state, by.r)
		if want != nil {
			if err == nil || ((want == ErrTeamLastManager || want == ErrTeamArchived || want == ErrTeamManager) && !errors.Is(err, want)) {
				t.Fatalf("%s should refuse: %v", op, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		state = next
	}
	apply(a, TeamLeave, "", "", ErrTeamLastManager)
	apply(b, TeamRename, "", "Spoof", ErrTeamManager)
	apply(a, TeamManagerAdd, b.r.Person, "", errors.New("not member"))
	apply(b, TeamJoin, "", "", nil)
	apply(a, TeamManagerAdd, b.r.Person, "", nil)
	if !state.Manager(b.r.Person) {
		t.Fatal("explicit replacement manager absent")
	}
	apply(a, TeamManagerRemove, a.r.Person, "", nil)
	apply(a, TeamLeave, "", "", nil)
	apply(b, TeamArchive, "", "", nil)
	apply(a, TeamJoin, "", "", ErrTeamArchived)
	apply(b, TeamManagerRemove, b.r.Person, "", ErrTeamLastManager)
	apply(b, TeamRestore, "", "", nil)
	apply(a, TeamJoin, "", "", nil)
	apply(b, TeamRemove, a.r.Person, "", nil)
	apply(a, TeamJoin, "", "", nil) // removal is not a ban
	if !state.Member(a.r.Person) || len(state.Managers) != 1 {
		t.Fatal("self-service or retained manager broken")
	}
	s := teamStepFor(&state, b, TeamRename, "", "Other")
	s.Author.Person = a.r.Person
	s.Sign(b.id.Sign)
	if _, err := s.Apply(&state, b.r); err == nil {
		t.Fatal("forged author person accepted")
	}
}
