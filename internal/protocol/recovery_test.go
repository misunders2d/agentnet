package protocol

import (
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
