package ui

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type groupedListFixture struct {
	*galleryActivateFixture
	agent protocol.AgentRecord
}

func (f groupedListFixture) Overview() (Overview, error) {
	view, err := f.galleryActivateFixture.Overview()
	view.DMs = append(view.DMs, DMSummary{
		ID: strings.Repeat("c", 64), Kind: "group", Title: "Ops group",
		Last: "Group update", LastAt: f.now(), Count: 3, Unread: 2,
	})
	view.Threads = append(view.Threads, ThreadSummary{
		ID: strings.Repeat("e", 32), Peer: f.agent.Host, AgentHost: f.agent.Host,
		AgentID: f.agent.ID, AgentKey: f.agent.HostKey, LastAt: f.now().Add(-time.Hour),
	})
	return view, err
}

func (f groupedListFixture) AgentCatalog(_ context.Context, host string) (AgentCatalogView, error) {
	if host == f.agent.Host {
		return AgentCatalogView{Host: host, Agents: []CatalogAgent{{Record: f.agent, Enabled: true}}}, nil
	}
	return AgentCatalogView{Host: f.me.Address, Local: true}, nil
}

func (groupedListFixture) ChangeAgent(context.Context, AgentCatalogChange) (AgentCatalogChangeResult, error) {
	return AgentCatalogChangeResult{}, Refuse("grouped list fixture cannot change agent configuration")
}

// MEL-822/824: the desktop list column hides and comes back from its active
// rail item, and its width follows the mouse and the keyboard within bounds,
// without remounting the open conversation, its draft or attached file;
// Agents keeps its full-width workspace; the phone layout is unchanged.
func TestListPanelCollapseResizeRendered(t *testing.T) {
	if testing.Short() {
		t.Skip("rendered")
	}
	f := NewFixture(time.Now)
	f.person = &PersonView{Person: strings.Repeat("1", 32), Label: "Alice", Address: f.me.Address, State: PersonSelf, Published: true}
	f.role = RolePerson
	var msgs []DMMessage
	for i := 0; i < 40; i++ {
		msgs = append(msgs, DMMessage{ID: strings.Repeat("d", 30) + string(rune('a'+i/26)) + string(rune('a'+i%26)), Dir: []string{"in", "out"}[i%2], From: "bob/desk", Kind: KindMessage, Body: "Fixture message " + string(rune('A'+i%26)), At: time.Now().Add(-time.Duration(40-i) * time.Hour), State: "received"})
	}
	f.dms = []*fxDM{{id: strings.Repeat("b", 64), peer: PersonView{Person: strings.Repeat("2", 32), Label: "Bob", Address: "bob/desk", State: PersonPinned, Published: true}, created: time.Now().Add(-50 * time.Hour), msgs: msgs}}
	p := groupedListFixture{
		galleryActivateFixture: &galleryActivateFixture{&filesFixture{Fixture: f, staged: map[string][]byte{}}},
		agent: protocol.AgentRecord{V: 1, ID: strings.Repeat("a", 32), Host: "carol/ci",
			HostKey: f.peers["carol/ci"].key.Pinned, Label: "Build assistant", Harness: "claude", TS: 1},
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(p, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	t.Setenv("AGENTNET_SCREENSHOTS", privateScreenshots(t))
	out := browserCheck(t, "testdata/listpanel_rendered.cjs", ts.URL+"/?t="+testToken)
	if !strings.Contains(out, "list panel rendered PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Logf("%s", out)
}
