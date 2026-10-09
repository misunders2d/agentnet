package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestHeldPersonTurnActions(t *testing.T) {
	for _, kind := range []string{"question", "task"} {
		item := convItem(client.ConvReview{ID: "held", Reason: client.ReviewHeldTurn, Kind: kind, State: "conv_held", Body: "Synthetic person turn"}, func(s string) string { return s })
		if !slices.Equal(item.Actions, []string{DoResolve}) {
			t.Fatalf("%s: %+v", kind, item)
		}
	}
}

func TestHeldPersonTurnRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	f := &needsYouFixture{Fixture: NewFixture(time.Now), phone: true}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	out, err := exec.Command("node", "testdata/heldturn_rendered_check.cjs", ts.URL+"/?t="+testToken).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Held person turn rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
