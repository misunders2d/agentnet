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

// The topic bar, the open topic's menu, All topics and the end of a done
// topic as the default messenger renders them over a real installation
// with 45 topics with one agent, at 1800x960 and 390x844, light and dark:
// the bar never scrolls and holds at most six chips, All topics lists,
// filters and searches, and every control has a name. Opt-in: needs an
// installed Playwright (AGENTNET_PLAYWRIGHT) and Chromium
// (AGENTNET_CHROMIUM or /usr/bin/chromium).
func TestTopicsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	alice, bob, live := liveWorld(t)
	ctx := t.Context()
	send := func(kind, body string) string {
		r, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: kind, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	for i := range 40 {
		send(envelope.KindMessage, fmt.Sprintf("Shipment note %d: pallets for dock %d are labelled", i, i%4+1))
	}
	ids := []string{send(envelope.KindQuestion, "May I move the Savannah order to Friday?")}
	for aisle := 1; aisle <= 4; aisle++ { // one finished task per rendering: each run reopens its own
		ids = append(ids, send(envelope.KindTask, fmt.Sprintf("Count the bath sets in aisle %d", aisle)))
	}
	for _, id := range ids {
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := bob.TopicOf(id); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for i, task := range ids[1:] {
		if _, err := bob.Reply(ctx, task, fmt.Sprintf("%d bath sets\nall dry", 410+i+1)); err != nil {
			t.Fatal(err)
		}
	}
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(live, ts.Listener.Addr().String(), testToken) // before the server starts: the browser is another process
	ts.Start()
	cmd := exec.Command(node, "testdata/topics_ui_check.cjs")
	cmd.Env = append(os.Environ(), "TOPICS_URL="+ts.URL+"/?t="+testToken, "TOPICS_TOTAL=45")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "topics ui check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
