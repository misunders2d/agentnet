package protocol

import (
	"fmt"
	"strings"
	"testing"
)

func TestMEL736Consent(t *testing.T) {
	id, _ := vecDesk()
	g := vecRoster()
	g.Email = "bohdan@mellanni.com"
	g.Sign(id.Sign)
	s := PersonSuccessor{Version: 1, Realm: "realm", OldPerson: vecOther, OldHead: strings.Repeat("a", 64), NewPerson: g.Person, NewGenesis: g.Hash(), Email: g.Email, Created: 1, Nonce: vecSession, Signer: g.Devices[0].Fingerprint()}
	s.Sign(id.Sign)
	want := fmt.Sprintf(SuccessorDomain+`{"version":1,"realm":"realm","old_person":"%s","old_head":"%s","new_person":"%s","new_genesis":"%s","email":"bohdan@mellanni.com","created":1,"nonce":"%s","signer":"%s"}`, vecOther, strings.Repeat("a", 64), g.Person, g.Hash(), vecSession, VecDeskFP)
	if string(s.Canonical()) != want {
		t.Fatalf("canonical successor bytes differ: %s", s.Canonical())
	}
	if err := s.Verify("realm", g, g); err != nil {
		t.Fatal(err)
	}
	changes := []func(*PersonSuccessor){func(s *PersonSuccessor) { s.Version++ }, func(s *PersonSuccessor) { s.Realm = "other" }, func(s *PersonSuccessor) { s.OldPerson = vecSession }, func(s *PersonSuccessor) { s.OldHead = strings.Repeat("b", 64) }, func(s *PersonSuccessor) { s.NewPerson = vecOther }, func(s *PersonSuccessor) { s.NewGenesis = strings.Repeat("b", 64) }, func(s *PersonSuccessor) { s.Email = "other@mellanni.com" }, func(s *PersonSuccessor) { s.Created++ }, func(s *PersonSuccessor) { s.Nonce = vecOther }, func(s *PersonSuccessor) { s.Signer = vecFP }}
	for i, change := range changes {
		c := s
		change(&c)
		if c.Verify("realm", g, g) == nil {
			t.Fatalf("tamper %d accepted", i)
		}
	}
	if s.Verify("other", g, g) == nil {
		t.Fatal("wrong realm")
	}
	other, _ := vecPhone()
	s.Sign(other.Sign)
	if s.Verify("realm", g, g) == nil {
		t.Fatal("wrong signer")
	}
}
