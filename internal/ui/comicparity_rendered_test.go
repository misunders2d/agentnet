package ui

import (
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

// MEL-528 parity as the default messenger renders it over a real
// installation (testdata/comic_parity_check.cjs): a reminder already due
// leads the list at the top of Chats and opens its message; a reminder is
// set from a message's menu (desktop) and its action sheet (phone), moved,
// done and cancelled; "Remind me about this chat" is in the header menu;
// automatic answers are turned on before they ask and off again; the
// typing preferences survive a reload; and no screen shows an address or a
// terminal command. At 1440x900 light and 390x844 dark. Opt-in: needs an
// installed Playwright (AGENTNET_PLAYWRIGHT) and Chromium
// (AGENTNET_CHROMIUM or /usr/bin/chromium).
func TestComicParityRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	alice, bob, live := liveWorld(t)
	ctx := t.Context()
	send := func(body, replyTo string) string {
		r, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: body, ReplyTo: replyTo})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	// One conversation (topic) with alice's device: the second follows the first.
	first := send("Please check the Savannah order before Friday", "")
	due := send("The Denver pallets arrive at dock 2", first)
	waitFor(t, "bob has both messages", func() bool {
		th, err := live.Thread(due)
		return err == nil && len(th.Messages) >= 2
	})
	// A reminder that is already due when the page opens.
	if _, err := bob.SetReminder(due, time.Now().Add(1500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reminder is due", func() bool {
		o, err := live.Overview()
		return err == nil && len(o.Reminders) == 1 && o.Reminders[0].Overdue
	})
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(live, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.Command(node, "testdata/comic_parity_check.cjs")
	cmd.Env = append(os.Environ(), "PARITY_URL="+ts.URL+"/?t="+testToken, "PARITY_FIRST="+first, "PARITY_DUE="+due,
		"PARITY_ADDRESSES="+alice.Address+","+bob.Address)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "comic parity check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
