package itest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/ui"
)

// page is a browser stand-in for one daemon's messenger page.
type page struct {
	t    *testing.T
	base string
	cl   *http.Client
}

// openPage reads the address `agentnet ui` prints and trades its token for
// the page cookie, as a browser would. A daemon just started may still be on
// its way to serving the page, so this waits until the address answers.
func (c *cli) openPage(t *testing.T, home string) *page {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	p := &page{t: t, cl: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	waitFor(t, home+"'s page", func() bool {
		out, err := c.try("--home", home, "ui")
		url, ok := strings.CutPrefix(out, "Open: ")
		if err != nil || !ok {
			return false
		}
		resp, err := p.cl.Get(url)
		if err != nil {
			return false
		}
		resp.Body.Close()
		p.base = url[:strings.Index(url, "/?t=")]
		return resp.StatusCode == 200
	})
	return p
}

func (p *page) get(path string, v any) int {
	p.t.Helper()
	resp, err := p.cl.Get(p.base + path)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 && v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			p.t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func (p *page) post(path string, body any, v any) (int, string) {
	p.t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", p.base+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", p.base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := p.cl.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && v != nil {
		if err := json.Unmarshal(raw, v); err != nil {
			p.t.Fatal(err)
		}
	}
	return resp.StatusCode, string(raw)
}

func (p *page) overview() ui.Overview {
	p.t.Helper()
	var o ui.Overview
	if code := p.get("/api/overview", &o); code != 200 {
		p.t.Fatalf("overview: %d", code)
	}
	return o
}

func (p *page) thread(id string) ui.Thread {
	p.t.Helper()
	var th ui.Thread
	if code := p.get("/api/thread?id="+id, &th); code != 200 {
		p.t.Fatalf("thread %s: %d", id, code)
	}
	return th
}

// events follows the page's event stream and delivers each change counter.
func (p *page) events(ctx context.Context) <-chan uint64 {
	p.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", p.base+"/events", nil)
	cl := &http.Client{Jar: p.cl.Jar} // no timeout: the stream stays open
	resp, err := cl.Do(req)
	if err != nil || resp.StatusCode != 200 {
		p.t.Fatalf("events: %v", err)
	}
	ch := make(chan uint64, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				n, _ := strconv.ParseUint(v, 10, 64)
				ch <- n
			}
		}
	}()
	return ch
}

// nextEvent waits for a counter above after.
func nextEvent(t *testing.T, ch <-chan uint64, after uint64, what string) uint64 {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case n, ok := <-ch:
			if !ok {
				t.Fatalf("event stream closed waiting for %s", what)
			}
			if n > after {
				return n
			}
		case <-deadline:
			t.Fatalf("no event for %s", what)
		}
	}
}

// oldCookieRefused checks that the cookie of an earlier daemon's page does
// not open the page of the daemon now running.
func oldCookieRefused(t *testing.T, old, now *page) {
	t.Helper()
	oldURL, _ := url.Parse(old.base)
	req, _ := http.NewRequest("GET", now.base+"/api/overview", nil)
	for _, ck := range old.cl.Jar.Cookies(oldURL) {
		req.AddCookie(ck)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an earlier daemon's cookie: %d", resp.StatusCode)
	}
}

func threadOf(o ui.Overview, id string) (ui.ThreadSummary, bool) {
	for _, s := range o.Threads {
		if s.ID == id {
			return s, true
		}
	}
	return ui.ThreadSummary{}, false
}

// TestUIDaemonJourney: two synthetic homes, each daemon serving its page
// over its real inbox. Messages sent from the command line and from the page
// arrive, replies join their thread, decisions go through the same gates,
// writes by other processes are pushed to the page, and a restart keeps
// everything while a new token replaces the old one.
func TestUIDaemonJourney(t *testing.T) {
	c := buildCLI(t)
	c.env = []string{"AGENTNET_NOTIFY=off"}
	c.setup(t, "hub")
	alice, bob := "admin/laptop", "bob/desk"
	c.start("alice.log", "--home", "alice", "daemon", "--ui", "127.0.0.1:0")
	stopBob := c.start("bob.log", "--home", "bob", "daemon", "--ui", "127.0.0.1:0")
	ap, bp := c.openPage(t, "alice"), c.openPage(t, "bob")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := bp.events(ctx)
	seq := nextEvent(t, ev, 0, "first event") - 1 // the first event is the current counter
	if o := bp.overview(); o.Demo || o.Me.Address != bob || len(o.Threads) != 0 {
		t.Fatalf("bob's page: %+v", o)
	}

	// One daemon owns a home: a second one is refused and leaves the page alone.
	before, _ := os.ReadFile(filepath.Join(c.dir, "bob", "ui-url"))
	if _, err := c.try("--home", "bob", "daemon", "--ui", "127.0.0.1:0"); err == nil {
		t.Fatal("second daemon started")
	}
	if after, _ := os.ReadFile(filepath.Join(c.dir, "bob", "ui-url")); !bytes.Equal(before, after) {
		t.Fatal("second daemon replaced the page address")
	}

	// A question from the command line reaches bob's page, held for him.
	q := strings.Fields(c.run("--home", "alice", "ask", "--answer-wait", "0", bob, "which port does auth use?"))[0]
	seq = nextEvent(t, ev, seq, "question arrived")
	var o ui.Overview
	waitFor(t, "question on bob's page", func() bool { o = bp.overview(); _, ok := threadOf(o, q); return ok })
	if s, _ := threadOf(o, q); s.Peer != alice || s.Review != 1 || len(o.Review) != 1 || o.Review[0].ID != q {
		t.Fatalf("held question: %+v review %+v", s, o.Review)
	}
	th := bp.thread(q)
	if m := th.Messages[0]; m.State != "held" || strings.Join(m.Actions, ",") != "reply,accept,approve,decline" || th.Approved {
		t.Fatalf("held view %+v approved %v", m, th.Approved)
	}

	// Approving from another process is pushed to the page; it covers later
	// questions only, as agentnet approve does.
	c.run("--home", "bob", "approve", alice)
	seq = nextEvent(t, ev, seq, "approve from the command line")
	if th := bp.thread(q); !th.Approved || th.Messages[0].State != "held" {
		t.Fatalf("after approve: approved %v state %s", th.Approved, th.Messages[0].State)
	}
	if code, _ := bp.post("/api/act", ui.Action{Do: ui.DoUnapprove, ID: alice}, nil); code != 200 {
		t.Fatalf("unapprove: %d", code)
	}
	if th := bp.thread(q); th.Approved {
		t.Fatal("still approved")
	}

	// Bob answers on the page; the answer joins the thread on both sides,
	// and the question cannot be answered twice.
	var note struct{ Note string }
	if code, body := bp.post("/api/act", ui.Action{Do: ui.DoReply, ID: q, Body: "8443 behind the ingress"}, &note); code != 200 {
		t.Fatalf("reply: %d %s", code, body)
	}
	if code, _ := bp.post("/api/act", ui.Action{Do: ui.DoReply, ID: q, Body: "again"}, nil); code != http.StatusConflict {
		t.Fatalf("second reply: %d", code)
	}
	waitFor(t, "answer on alice's page", func() bool { s, _ := threadOf(ap.overview(), q); return s.Count == 2 })
	ath := ap.thread(q)
	if a := ath.Messages[1]; a.Dir != "in" || a.Kind != "answer" || a.ReplyTo != q || a.Body != "8443 behind the ingress" {
		t.Fatalf("alice sees %+v", a)
	}
	if s, _ := threadOf(ap.overview(), q); s.Waiting {
		t.Fatal("answered question still waiting")
	}

	// A new conversation from bob's page is a separate thread; alice's reply
	// from the command line joins it.
	var sent ui.Sent
	if code, body := bp.post("/api/send", ui.Draft{To: alice, Kind: "message", Body: "deploy is green"}, &sent); code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	waitFor(t, "alice receives", func() bool {
		for _, m := range c.inbox("alice") {
			if m.ID == sent.ID {
				return true
			}
		}
		return false
	})
	// The page does not wait for the receipt; it arrives and is pushed.
	waitFor(t, "page send delivered", func() bool { return bp.thread(sent.ID).Messages[0].State == "delivered" })
	c.run("--home", "alice", "reply", sent.ID, "thanks")
	waitFor(t, "reply joins bob's thread", func() bool { s, _ := threadOf(bp.overview(), sent.ID); return s.Count == 2 })
	if o := bp.overview(); len(o.Threads) != 2 {
		t.Fatalf("threads %+v", o.Threads)
	}
	// Linking to a message of another conversation is refused.
	if code, _ := bp.post("/api/send", ui.Draft{To: "bob/elsewhere", Kind: "message", Body: "x", ReplyTo: q}, nil); code != http.StatusConflict {
		t.Fatalf("cross link: %d", code)
	}

	// A task waits for bob's decision; accepting runs nothing without a
	// responder, so bob declines it on the page.
	task := strings.Fields(c.run("--home", "alice", "task", bob, "please tidy the repo"))[0]
	waitFor(t, "task waiting", func() bool { o = bp.overview(); return len(o.Review) == 1 && o.Review[0].ID == task })
	if m := bp.thread(task).Messages[0]; m.State != "awaiting" || strings.Join(m.Actions, ",") != "accept,accept_always,reply,decline" {
		t.Fatalf("task view %+v", m)
	}
	if code, body := bp.post("/api/act", ui.Action{Do: ui.DoDecline, ID: task, Reason: "not today"}, nil); code != 200 {
		t.Fatalf("decline: %d %s", code, body)
	}
	if code, _ := bp.post("/api/act", ui.Action{Do: ui.DoDecline, ID: task}, nil); code != http.StatusConflict {
		t.Fatalf("second decline: %d", code)
	}

	// The token never reaches the log. Restart: the page address changes,
	// the old cookie stops working and the conversations are all there.
	token := strings.TrimSpace(string(before))[strings.Index(string(before), "?t=")+3:]
	stopBob()
	logData, _ := os.ReadFile(filepath.Join(c.dir, "bob.log"))
	if strings.Contains(string(logData), token) || !strings.Contains(string(logData), "messenger page on http://127.0.0.1:") {
		t.Fatalf("bob's log:\n%s", logData)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "bob", "ui-url")); gracefulStop && !os.IsNotExist(err) {
		t.Fatalf("address file left after a graceful stop (%v)", err)
	}
	if out, err := c.try("--home", "bob", "ui"); err == nil || !strings.Contains(out, "daemon --ui") {
		t.Fatalf("ui with no daemon: %q", out)
	}
	_, killBob := c.startProc("bob2.log", "--home", "bob", "daemon", "--ui", "127.0.0.1:0")
	bp2 := c.openPage(t, "bob")
	oldCookieRefused(t, bp, bp2)
	o = bp2.overview()
	if len(o.Threads) != 3 || len(o.Review) != 0 {
		t.Fatalf("after restart: %d threads, review %+v", len(o.Threads), o.Review)
	}
	if m := bp2.thread(q).Messages[0]; m.State != "manual" {
		t.Fatalf("question after restart: %s", m.State)
	}
	if m := bp2.thread(task).Messages[0]; m.State != "declined" {
		t.Fatalf("task after restart: %s", m.State)
	}

	// Killed with no cleanup (a crash, a power cut, a stop on Windows): the
	// old address stays in the file, but agentnet ui does not offer a page
	// nobody serves, and the next daemon serves everything again.
	killBob()
	if out, err := c.try("--home", "bob", "ui"); err == nil || !strings.Contains(out, "daemon --ui") {
		t.Fatalf("ui after the daemon was killed: %q", out)
	}
	c.start("bob3.log", "--home", "bob", "daemon", "--ui", "127.0.0.1:0")
	bp3 := c.openPage(t, "bob")
	oldCookieRefused(t, bp2, bp3)
	if o := bp3.overview(); len(o.Threads) != 3 {
		t.Fatalf("after a kill: %d threads", len(o.Threads))
	}
}
