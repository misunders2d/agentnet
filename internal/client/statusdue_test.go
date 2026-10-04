package client

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// runTestCLI is the test binary run as a short-lived CLI process
// (TestMain, AGENTNET_TEST_CLI="ACTION HOME ID"): it opens HOME, takes one
// local decision on request ID and exits at once, as agentnet accept and
// agentnet resolve do. Its own requests to the Hub never finish before it
// exits (they hang until then), so nothing it started in the background
// can be what tells the requester.
func runTestCLI(cmd string) int {
	f := strings.Fields(cmd)
	if len(f) != 3 {
		fmt.Fprintln(os.Stderr, "usage: ACTION HOME ID")
		return 2
	}
	wrapTransport = func(http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	}
	a, err := Open(f[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch f[0] {
	case "accept":
		err = a.Accept(f[2])
	case "resolve":
		err = a.Resolve(f[2])
	default:
		err = fmt.Errorf("unknown action %s", f[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0 // no wait for anything this process started
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// cli runs action on request id as a separate, short-lived process on home.
func cli(t *testing.T, home, action, id string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "AGENTNET_TEST_CLI="+action+" "+home+" "+id)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", action, id, err, out)
	}
}

// BUG-08: a request a crashed daemon left running is interrupted when the
// daemon starts again, and its requester is told so; before, the requester
// kept seeing the last state it was told ("queued" or "running") for good.
func TestRequesterToldOfInterruptAtStart(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	stopBob := runAgent(t, w.bob)
	// No responder at bob: the approved question waits, queued.
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "how long is the queue?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice told it is queued", func() bool { e := execOf(t, w.alice, q.ID); return e != nil && e.State == "queued" })
	// As a crashed daemon leaves a request it ran.
	stopBob()
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateRunning, q.ID); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	waitState(t, w.bob, q.ID, stateInterrupt)
	eventually(t, "alice told it was interrupted", func() bool {
		e := execOf(t, w.alice, q.ID)
		return e != nil && e.State == "interrupted"
	})
}

// BUG-08: a decision taken in a short-lived CLI process (agentnet resolve,
// agentnet accept) reaches the requester: the process exits at once, and
// the daemon tells the status it noted. Before, the status was sent from
// a goroutine of the CLI process and was lost when it exited.
func TestCLIDecisionStatusOutlivesItsProcess(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stubhuman", st.dir, time.Minute) // its answer: the person must decide
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "which budget?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateNeedHuman)
	eventually(t, "alice told it needs a person", func() bool { e := execOf(t, w.alice, q.ID); return e != nil && e.State == "needs_human" })
	cli(t, w.bobHome, "resolve", q.ID)
	waitState(t, w.bob, q.ID, stateResolved)
	eventually(t, "alice told it was resolved", func() bool { e := execOf(t, w.alice, q.ID); return e != nil && e.State == "resolved" })
	if n := inboxCount(t, w.bob, `id = ? AND status_due > 0`, q.ID); n != 0 {
		t.Fatalf("a told status is still due (%d)", n)
	}
}
