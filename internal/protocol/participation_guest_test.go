package protocol

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestGuestAgentSignedParentShape(t *testing.T) {
	child := func() ParticipationEvent {
		e := vecInvite()
		topic := strings.Repeat("c", 32)
		e.Topic = &topic
		e.Parent = strings.Repeat("a", 64)
		e.Host.Person = e.Author.Person
		e.Audience = AudienceRoom
		e.TaskKeys = nil
		e.Sign(vecKey())
		return e
	}
	e := child()
	if err := e.Verify(vecKey().Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	scope := ScopeOf(e, e.TS)
	if scope.Parent != e.Parent || scope.Host.Person != e.Author.Person {
		t.Fatal("public scope loses exact parent")
	}
	for name, mutate := range map[string]func(*ParticipationEvent){
		"recursive role":     func(e *ParticipationEvent) { e.Role = RoleHuman },
		"Main":               func(e *ParticipationEvent) { s := ""; e.Topic = &s },
		"whole conversation": func(e *ParticipationEvent) { e.Topic = nil },
		"other owner":        func(e *ParticipationEvent) { e.Host.Person = vecOther },
		"task grant":         func(e *ParticipationEvent) { e.TaskKeys = []string{vecFP} },
		"member role": func(e *ParticipationEvent) {
			e.Group = &ParticipationGroup{Seq: 0, Hash: strings.Repeat("b", 64), HostRole: "member", HostAdmission: strings.Repeat("c", 64)}
		},
		"membership author": func(e *ParticipationEvent) { e.Author.GroupAdmission = strings.Repeat("b", 64) },
		"decision parent":   func(e *ParticipationEvent) { e.Type = EventAccept },
	} {
		t.Run(name, func(t *testing.T) {
			e := child()
			mutate(&e)
			if e.Validate() == nil {
				t.Fatal("expanded guest child accepted")
			}
		})
	}
}
