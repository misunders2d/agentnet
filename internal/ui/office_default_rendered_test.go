package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// MEL-821: Office, built in, opens on a profile that never chose a skin,
// offline; saved choices (Comic too) are kept; a gallery Office moves to the
// built-in one with its theme; nothing is recorded for a profile that never
// chose.
func TestOfficeDefaultRendered(t *testing.T) {
	if testing.Short() {
		t.Skip("rendered")
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(officeFixture(), ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	t.Setenv("AGENTNET_TEST_FRESH_SKIN", "1") // the real first open (testdata/comic_preload.cjs)
	t.Setenv("AGENTNET_SCREENSHOTS", privateScreenshots(t))
	out := browserCheck(t, "testdata/office_default_rendered.cjs", ts.URL+"/?t="+testToken)
	if !strings.Contains(out, "office default rendered PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Logf("%s", out)
}

// The same first open over WorkspaceHandler, which the desktop and phone
// apps serve: Office, offline, nothing recorded, the column filling the pane.
func TestOfficeDefaultWorkspaceRendered(t *testing.T) {
	if testing.Short() {
		t.Skip("rendered")
	}
	set := NewWorkspaceProviders()
	f := officeFixture()
	if _, err := set.Bind(client.Workspace{ID: "default", Name: "This computer", Realm: strings.Repeat("a", 32), Address: f.me.Address, State: "enrolled"}, f); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).WorkspaceHandler(set)
	ts.Start()
	t.Cleanup(ts.Close)
	t.Setenv("AGENTNET_TEST_FRESH_SKIN", "1")
	t.Setenv("OFFICE_FRESH_ONLY", "1")
	t.Setenv("AGENTNET_SCREENSHOTS", privateScreenshots(t))
	out := browserCheck(t, "testdata/office_default_rendered.cjs", ts.URL+"/?t="+testToken)
	if !strings.Contains(out, "office default rendered PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Logf("%s", out)
}

// Seed the same long incoming and outgoing messages for the width checks.
func officeFixture() *Fixture {
	f := NewFixture(time.Now)
	for _, thread := range f.threads {
		if thread.peer == "vitalii/phone" {
			for _, direction := range []string{"in", "out"} {
				f.add(thread, &Message{Dir: direction, Kind: KindMessage, At: f.now(), Body: strings.Repeat("Это длинное сообщение для проверки ширины переписки. ", 7)})
			}
			f.add(thread, &Message{Dir: "out", Kind: KindMessage, At: f.now(), Body: "Got it."})
			break
		}
	}
	return f
}
