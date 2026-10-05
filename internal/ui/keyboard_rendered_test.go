package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// MEL-519 as the default messenger renders it on a 390x844 touch screen,
// over a real installation with a long device conversation. Android (the
// page shrinks with the keyboard): the message box stays above it and the
// newest message stays in view. iOS (only the visual viewport shrinks, a
// stand-in visualViewport here): the host sets --an-viewport-h and
// --an-keyboard, the page and the message box fit above the keyboard, and
// a sheet sits on it; Classic and Zoom fit too. No up-front execution
// toggle appears. Reply from a
// message's sheet, or on the card of a task waiting for the person's OK,
// puts the cursor in the field inside the tap itself; the phone emoji
// picker and the host's own sheets sit on the iOS keyboard too. On a
// desktop the composer focuses its field. Opt-in: needs an
// installed Playwright (AGENTNET_PLAYWRIGHT) and Chromium (AGENTNET_CHROMIUM
// or /usr/bin/chromium).
func TestKeyboardViewportRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	alice, bob, live := liveWorld(t)
	const total = 30 // one topic, taller than the screen
	root := ""
	for i := range total {
		// The topic starts with a task that waits for bob's OK: its card has a Reply of its own.
		kind, body := envelope.KindMessage, fmt.Sprintf("Pallet count %d: dock %d is labelled and dry", i, i%4+1)
		if i == 0 {
			kind, body = envelope.KindTask, "Count the pallets on every dock"
		}
		r, err := alice.SendMessage(t.Context(), client.Outgoing{To: bob.Address, Kind: kind, ReplyTo: root, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		if root == "" { // the topic exists on bob's side before its replies arrive
			root = r.ID
			waitTopic(t, bob, root)
		}
	}
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(live, ts.Listener.Addr().String(), testToken) // before the server starts: the browser is another process
	ts.Start()
	cmd := exec.Command(node, "testdata/keyboard_viewport_check.cjs")
	cmd.Env = append(os.Environ(), "KEYBOARD_URL="+ts.URL+"/?t="+testToken, fmt.Sprintf("KEYBOARD_TOTAL=%d", total))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "keyboard viewport check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// waitTopic waits until agent a has filed message id under a topic.
func waitTopic(t *testing.T, a *client.Agent, id string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := a.TopicOf(id); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("message %s never reached a topic: %v", id, err)
		}
	}
}
