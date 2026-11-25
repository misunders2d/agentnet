package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// MEL-819: a gallery update of the skin on screen opens in place, with the
// same skin selected, once nothing would be interrupted: the open chat, its
// draft text and attached file stay; a write under way, a focused field and
// an open dialog defer it; bad bytes, an incompatible or untrusted version
// and a version that fails to mount leave the running one usable.
func TestGallerySkinUpdateActivatesRendered(t *testing.T) {
	if testing.Short() {
		t.Skip("rendered")
	}
	set := NewWorkspaceProviders()
	f := NewFixture(time.Now)
	conv := strings.Repeat("b", 64)
	f.person = &PersonView{Person: strings.Repeat("1", 32), Label: "Alice", Address: f.me.Address, State: PersonSelf, Published: true}
	f.role = RolePerson
	f.dms = []*fxDM{{id: conv, peer: PersonView{Person: strings.Repeat("2", 32), Label: "Bob", Address: "bob/desk", State: PersonPinned, Published: true}, created: time.Now(), msgs: []DMMessage{
		{ID: strings.Repeat("3", 32), Dir: "in", From: "bob/desk", Kind: KindMessage, Body: "Hello from Bob", At: time.Now(), State: "received"},
	}}}
	p := &galleryActivateFixture{&filesFixture{Fixture: f, staged: map[string][]byte{}}}
	if _, err := set.Bind(client.Workspace{ID: "default", Name: "This computer", Realm: strings.Repeat("a", 32), Address: f.me.Address, State: "enrolled"}, p); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(p, ts.Listener.Addr().String(), testToken).WorkspaceHandler(set)
	ts.Start()
	t.Cleanup(ts.Close)
	t.Setenv("AGENTNET_SCREENSHOTS", privateScreenshots(t))
	out := browserCheck(t, "testdata/skin_gallery_activate_rendered.cjs", ts.URL+"/?t="+testToken)
	if !strings.Contains(out, "gallery skin activate rendered PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Logf("%s", out)
}

// galleryActivateFixture lets the open chat take attached files.
type galleryActivateFixture struct{ *filesFixture }

func (f *galleryActivateFixture) Overview() (Overview, error) {
	v, err := f.Fixture.Overview()
	v.Files = &FileLimits{MaxFile: 1 << 20, MaxCount: 8}
	return v, err
}
