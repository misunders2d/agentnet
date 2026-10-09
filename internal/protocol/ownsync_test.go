package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInvitationSyncStrictView(t *testing.T) {
	root, state, _ := groupFixture(t)
	p := GroupInvitation{V: 1, Root: root, State: state, Target: NewID(), Roster: state.ActorRoster, Seq: 1, Prev: state.Hash(), Nonce: NewID()}
	r := InvitationSync{V: 1, Person: root.Creator.Person, Roster: root.Creator.Roster, ID: p.ID(), Revision: 1, Status: "pending", Proposal: p}
	raw, _ := json.Marshal(r)
	if _, err := ParseInvitationSync(raw); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*InvitationSync){
		func(r *InvitationSync) { r.Revision = 0 },
		func(r *InvitationSync) { r.Revision = 9007199254740992 },
		func(r *InvitationSync) { r.ID = strings.Repeat("f", 64) },
		func(r *InvitationSync) { r.Status = "execute" },
		func(r *InvitationSync) { r.Proposal.Target = NewID() },
		func(r *InvitationSync) { r.Roster = "unverified" },
	} {
		bad := r
		change(&bad)
		data, _ := json.Marshal(bad)
		if _, err := ParseInvitationSync(data); err == nil {
			t.Fatal("invalid view accepted")
		}
	}
	extra := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"consent":true}`)...)
	if _, err := ParseInvitationSync(extra); err == nil {
		t.Fatal("unknown authority field accepted")
	}
	if _, err := ParseInvitationSync([]byte(strings.Repeat(" ", MaxGroupState+1025) + string(raw))); err == nil {
		t.Fatal("unbounded record accepted")
	}
}
