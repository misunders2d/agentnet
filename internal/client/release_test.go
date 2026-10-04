package client

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func recommend(t *testing.T, admin *Agent, version, url string) {
	t.Helper()
	if _, err := admin.SetRelease(tctx(t), protocol.Release{Version: version, URL: url, Note: "NOTE-FOR-PEOPLE"}); err != nil {
		t.Fatal(err)
	}
}

// running sets this build's version for a test (before its daemons start).
func running(t *testing.T, version string) {
	old := protocol.Version
	protocol.Version = version
	t.Cleanup(func() { protocol.Version = old })
}

func waitRelease(t *testing.T, a *Agent, url string) {
	t.Helper()
	eventually(t, "release "+url, func() bool { r, ok := a.Release(); return ok && r.URL == url || !ok && url == "" })
}

// The recommendation reaches a running daemon, is told once per distinct
// recommendation (not again when re-announced or after a restart), and each
// harness session gets one line without the operator's note.
func TestReleaseAnnouncement(t *testing.T) {
	running(t, "v0.3.0")
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	recommend(t, w.alice, "v9.9.0", "https://example.test/a")
	waitRelease(t, w.bob, "https://example.test/a")
	eventually(t, "notice", func() bool { return n.count() == 1 })
	recommend(t, w.alice, "v9.9.0", "https://example.test/a") // the same again
	recommend(t, w.alice, "v9.9.0", "https://example.test/b") // changed within the second
	waitRelease(t, w.bob, "https://example.test/b")
	eventually(t, "second notice", func() bool { return n.count() == 2 })
	quiet(t, w.bob, n, 2)
	if strings.Contains(n.last(), "v9.9.0") || strings.Contains(n.last(), "example.test") {
		t.Fatalf("notice carries details: %q", n.last())
	}
	stop()
	runWith(t, w, w.bob, RunOptions{})
	quiet(t, w.bob, n, 2)

	line := shown(t, w.bob, "A", "PostToolUse")
	if !strings.Contains(line, "recommends AgentNet v9.9.0") || !strings.Contains(line, "https://example.test/b") ||
		!strings.Contains(line, "unless they have already authorized it") || strings.Contains(line, "NOTE-FOR-PEOPLE") {
		t.Fatalf("hook line: %q", line)
	}
	if again := shown(t, w.bob, "A", "PostToolUse"); again != "" {
		t.Fatalf("told twice: %q", again)
	}
	if stop := shown(t, w.bob, "B", "Stop"); stop != "" {
		t.Fatalf("Stop mentions the update: %q", stop)
	}
	if b := shown(t, w.bob, "B", "UserPromptSubmit"); !strings.Contains(b, "v9.9.0") {
		t.Fatalf("session B: %q", b)
	}
	if r, ok := LocalRelease(w.bob.home); !ok || r.Version != "v9.9.0" {
		t.Fatalf("local read: %+v %v", r, ok)
	}
	recommend(t, w.alice, "", "")
	waitRelease(t, w.bob, "")
}

// A build equal to the recommendation is not nudged.
func TestReleaseSameBuildSilent(t *testing.T) {
	running(t, "v-same")
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	runWith(t, w, w.bob, RunOptions{})
	recommend(t, w.alice, "v-same", "https://example.test/")
	waitRelease(t, w.bob, "https://example.test/")
	quiet(t, w.bob, n, 0)
	if line := shown(t, w.bob, "S", "UserPromptSubmit"); strings.Contains(line, "recommends") {
		t.Fatalf("nudged the recommended build: %q", line)
	}
}

// A build ahead of the recommendation (a preview newer than the stable
// release) is not told to go back; a newer release is told.
func TestReleaseOlderNotRecommended(t *testing.T) {
	running(t, "v0.4.0")
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	runWith(t, w, w.bob, RunOptions{})
	recommend(t, w.alice, "v0.3.0", "https://example.test/stable")
	waitRelease(t, w.bob, "https://example.test/stable")
	quiet(t, w.bob, n, 0)
	if line := shown(t, w.bob, "P", "UserPromptSubmit"); strings.Contains(line, "recommends") {
		t.Fatalf("told to downgrade: %q", line)
	}
	for _, c := range w.bob.Doctor(tctx(t)) {
		if c.Name == "update" && !strings.Contains(c.Result, "no comparable newer recommendation") {
			t.Fatalf("doctor: %q", c.Result)
		}
	}
	recommend(t, w.alice, "v0.5.0", "https://example.test/next")
	waitRelease(t, w.bob, "https://example.test/next")
	eventually(t, "notice", func() bool { return n.count() == 1 })
	if line := shown(t, w.bob, "Q", "UserPromptSubmit"); !strings.Contains(line, "recommends AgentNet v0.5.0") {
		t.Fatalf("newer release not told: %q", line)
	}
}

// A notice that could not be shown is not marked: it is tried again after a
// restart, not on every ping.
func TestReleaseNoticeRetriedAfterRestart(t *testing.T) {
	running(t, "v0.3.0")
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	n.setFail(os.ErrPermission)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	recommend(t, w.alice, "v7.0.0", "https://example.test/")
	eventually(t, "attempt", func() bool { return n.count() == 1 })
	quiet(t, w.bob, n, 1)
	stop()
	n.setFail(nil)
	runWith(t, w, w.bob, RunOptions{})
	eventually(t, "retry", func() bool { return n.count() == 2 })
	if done, _ := w.bob.store.config("release_notified"); done != "v7.0.0 https://example.test/" {
		t.Fatalf("marked %q", done)
	}
}

// A slow desktop notifier runs on the worker and does not hold up the
// stream: messages keep arriving while it is still showing.
func TestSlowReleaseNoticeDoesNotBlockDelivery(t *testing.T) {
	running(t, "v0.3.0")
	w := newWorld(t, "")
	var showing atomic.Bool
	// A notifier that hangs until the test is done: "still showing" then
	// does not depend on delivery beating a fixed hang on a loaded runner.
	hang := make(chan struct{})
	w.bob.notify = func(title, body string, _ []string, _ func()) error {
		showing.Store(true)
		<-hang
		showing.Store(false)
		return nil
	}
	runWith(t, w, w.bob, RunOptions{})
	t.Cleanup(func() { close(hang) }) // runs before the daemon's stop (cleanups run last-in first-out)
	recommend(t, w.alice, "v8.0.0", "https://example.test/")
	eventually(t, "slow notice started", showing.Load)
	w.alice.Send(tctx(t), w.bob.Address, "still flowing", "")
	eventually(t, "message during a slow notice", func() bool { return hasInbox(w.bob, "still flowing") })
	if !showing.Load() {
		t.Fatal("the message arrived only after the notice finished")
	}
}

// This client ignores stream events it does not know. (That only shows the
// current binary's rule; it does not run an older one.)
func TestUnknownStreamEventIgnored(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.dispatch(tctx(t), "future-event", `{"anything":1}`); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := w.bob.Inbox(false, false); len(msgs) != 0 {
		t.Fatal("unknown event stored something")
	}
	if _, ok := w.bob.Release(); ok {
		t.Fatal("unknown event set a release")
	}
	bad := `{"version":"v1","url":"http://plain.example/"}`
	if err := w.bob.dispatch(tctx(t), "release", bad); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.bob.Release(); ok {
		t.Fatal("invalid release saved")
	}
	_ = envelope.KindMessage
}

func TestLocalReleaseCreatesNothing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "none")
	if _, ok := LocalRelease(home); ok {
		t.Fatal("release from nowhere")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("home created")
	}
}

// doctor tells a missing endpoint, an unknown answer and a confirmed "none"
// apart.
func TestDoctorReleaseLine(t *testing.T) {
	w := newWorld(t, "")
	line := func() string {
		for _, c := range w.bob.Doctor(tctx(t)) {
			if c.Name == "update" {
				return c.Result
			}
		}
		return ""
	}
	if got := line(); got != "no client version recommended by the Hub" {
		t.Fatalf("none: %q", got)
	}
	base := w.bob.hub.http.Transport
	w.bob.hub.http.Transport = notFoundRT{base, "/v1/release"}
	if got := line(); !strings.HasPrefix(got, "recommendation endpoint unavailable (an older Hub may not support it)") {
		t.Fatalf("404: %q", got)
	}
	w.bob.hub.http.Transport = base
	injectFaults(w.bob).add("GET", "/v1/release", 1, false)
	if got := line(); !strings.HasPrefix(got, "recommendation unknown or unavailable") {
		t.Fatalf("error: %q", got)
	}
}

type notFoundRT struct {
	base http.RoundTripper
	path string
}

func (rt notFoundRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == rt.path {
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("404 page not found")), Request: r, Header: http.Header{}}, nil
	}
	return rt.base.RoundTrip(r)
}
