package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func deleteTeamHTTP(t *testing.T, h *Hub, who member, id string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := signed(t, who.id, who.addr, http.MethodDelete, "/v1/teams/"+id, nil)
	r.SetPathValue("id", id)
	h.handleDeleteTeam(w, r)
	return w
}

func TestTeamsHubDeleteManagerAdminAndTombstone(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(map[bool]string{false: "manager", true: "workspace-admin"}[admin], func(t *testing.T) {
			h, a, b := hubTeamFixture(t)
			create := hubTeamStep(h, nil, a, protocol.TeamCreate, "", "Ops")
			if w := putTeamHTTP(t, h, a.member, create); w.Code != 204 {
				t.Fatal(w.Code, w.Body)
			}
			if w := deleteTeamHTTP(t, h, b.member, create.Team); w.Code != 403 {
				t.Fatalf("non-manager: %d %s", w.Code, w.Body)
			}
			who := a.member
			if admin {
				if _, err := h.store.db.Exec(`UPDATE agents SET admin=1 WHERE address=?`, b.member.addr); err != nil {
					t.Fatal(err)
				}
				who = b.member
			}
			if w := deleteTeamHTTP(t, h, who, create.Team); w.Code != 204 {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			if _, found, err := teamHeadIn(h.store.db, h.RealmID(), create.Team); err != nil || found {
				t.Fatalf("head retained: %v %v", found, err)
			}
			d, err := h.teamDirectory()
			if err != nil || len(d.Teams) != 0 {
				t.Fatalf("directory: %+v %v", d, err)
			}
			var n int
			if err := h.store.db.QueryRow(`SELECT count(*) FROM team_chain WHERE team=?`, create.Team).Scan(&n); err != nil || n != 1 {
				t.Fatalf("chain tombstone: %d %v", n, err)
			}
			// A replay can acknowledge stored history, but can never restore its head.
			putTeamHTTP(t, h, a.member, create)
			if _, found, _ := teamHeadIn(h.store.db, h.RealmID(), create.Team); found {
				t.Fatal("replayed create resurrected list")
			}
		})
	}
}
