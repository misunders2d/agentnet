package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTeamTagsExactTargetsAndAuthority(t *testing.T) {
	owner, other := teamPerson(t, "owner"), teamPerson(t, "other")
	step := teamStepFor(nil, owner, TeamCreate, "", "Reviewers")
	step.V = 2
	step.Sign(owner.id.Sign)
	state, err := step.Apply(nil, owner.r)
	if err != nil || state.Version != 2 || !state.Manager(owner.r.Person) || len(state.Members) != 0 {
		t.Fatalf("an agent-only tag must not automatically address its manager: %+v %v", state, err)
	}
	change := func(by teamTestPerson, op, target string, agent *TeamAgent, allowed bool) {
		t.Helper()
		s := teamStepFor(&state, by, op, target, "")
		s.V, s.Agent = 2, agent
		s.Sign(by.id.Sign)
		raw, _ := json.Marshal(s)
		parsed, e := ParseTeamStep(raw)
		if e == nil {
			var next TeamState
			next, e = parsed.Apply(&state, by.r)
			if e == nil {
				state = next
			}
		}
		if (e == nil) != allowed {
			t.Fatalf("%s allowed=%v: %v", op, allowed, e)
		}
	}
	a := TeamAgent{ID: NewID(), Host: other.r.Devices[0].Address, HostKey: other.r.Devices[0].Fingerprint()}
	change(other, TeamAgentAdd, "", &a, false)
	change(owner, TeamAgentAdd, "", &a, true)
	change(owner, TeamAgentAdd, "", &a, true)
	if len(state.Agents) != 1 {
		t.Fatal("duplicate agent target")
	}
	change(other, TeamJoin, "", nil, false)
	change(owner, TeamAdd, other.r.Person, nil, true)
	change(owner, TeamAdd, owner.r.Person, nil, true)
	change(owner, TeamRemove, owner.r.Person, nil, true)
	if !state.Manager(owner.r.Person) || state.Member(owner.r.Person) {
		t.Fatal("target membership changed management")
	}
	changedKey := a
	changedKey.HostKey = owner.r.Devices[0].Fingerprint()
	change(owner, TeamAgentRemove, "", &changedKey, true)
	if len(state.Agents) != 1 {
		t.Fatal("wrong host key removed target")
	}
	change(owner, TeamAgentRemove, "", &a, true)
	if len(state.Agents) != 0 {
		t.Fatal("exact target not removed")
	}
	bad := a
	bad.ID = "not-an-agent"
	change(owner, TeamAgentAdd, "", &bad, false)
	legacy := teamStepFor(&state, owner, TeamRename, "", "Other")
	if _, err := legacy.Apply(&state, owner.r); err == nil {
		t.Fatal("v1 operation changed v2 semantics")
	}
	for len(state.Managers) < MaxTeamMembers {
		state.Managers = append(state.Managers, NewID())
	}
	change(owner, TeamManagerAdd, other.r.Person, nil, false)
}

func TestTeamTagsCanonicalVector(t *testing.T) {
	var s TeamStep
	if err := json.Unmarshal([]byte(strings.TrimPrefix(TeamVectorCanonical, TeamDomain)), &s); err != nil {
		t.Fatal(err)
	}
	s.V, s.Seq, s.Prev, s.Op, s.Name = 2, 1, TeamVectorHash, TeamAgentAdd, ""
	s.Agent = &TeamAgent{ID: vecOther, Host: s.Author.Address, HostKey: s.Author.Fingerprint}
	want := TeamDomain + `{"v":2,"realm_id":"00112233445566778899aabbccddeeff","team":"fedcba9876543210fedcba9876543210","seq":1,"prev":"3e062daa30eb7a21dfae4b1b9a705fd791ef46f5086732ba6fbba7ddb4f2d89b","author":{"person":"0123456789abcdef0123456789abcdef","roster":"f78b94d0e4bf9f75df8a53076cb188f257acf1b660df845f0da670fdeef9f755","address":"vitalii/desk","fingerprint":"19c77bce-aca933c7-80e1c0e9-e46fc988"},"op":"agent-add","agent":{"id":"fedcba9876543210fedcba9876543210","host":"vitalii/desk","host_key":"19c77bce-aca933c7-80e1c0e9-e46fc988"},"ts":1790000000}`
	if err := s.Validate(); err != nil || string(s.Canonical()) != want {
		t.Fatalf("v2 browser vector mismatch: %v\n%s", err, s.Canonical())
	}
}
