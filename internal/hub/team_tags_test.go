package hub

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTeamTagsDirectoryKeepsOldClientsCompatible(t *testing.T) {
	h, a, _ := hubTeamFixture(t)
	for _, version := range []int{1, 2} {
		s := hubTeamStep(h, nil, a, protocol.TeamCreate, "", "Reviewers")
		s.V = version
		s.Sign(a.member.id.Sign)
		if w := putTeamHTTP(t, h, a.member, s); w.Code != 204 {
			t.Fatalf("create v%d: %d %s", version, w.Code, w.Body)
		}
	}
	old, err := h.teamDirectory()
	if err != nil || len(old.Teams) != 1 || old.Version != 0 {
		t.Fatalf("old client exposed to unsupported chains: %+v %v", old, err)
	}
	current, err := h.teamDirectoryVersion(true)
	if err != nil || len(current.Teams) != 2 || current.Version != 2 {
		t.Fatalf("new directory missing tags: %+v %v", current, err)
	}
}
