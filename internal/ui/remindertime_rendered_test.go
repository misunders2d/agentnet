package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestComicReminderDateTimeRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: installed Playwright required")
	}
	alice, bob, live := liveWorld(t)
	sent, err := alice.SendMessage(t.Context(), client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: "Please review the fictional sample order"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "received fictional message", func() bool { thread, err := live.Thread(sent.ID); return err == nil && len(thread.Messages) > 0 })
	var server *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	server = New(live, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.CommandContext(t.Context(), "node", "testdata/reminder_time_rendered.cjs")
	cmd.Env = append(os.Environ(), "PARITY_URL="+ts.URL+"/?t="+testToken, "PARITY_FIRST="+sent.ID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
