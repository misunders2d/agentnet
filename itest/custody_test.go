package itest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui"
)

// Rows of background replication, as internal/client's syncSubs names them, still
// waiting on the sender. A live message reaching custody ahead of them overtook them.
const custodySyncWaiting = `SELECT count(*) FROM outbox WHERE sub IN ('history-archive','history','device-history','read-sync','root-sync','topic-sync','topic-state-sync','reminder-sync','direct-topic-sync','model-sync','invitation-sync') AND replication_live=0 AND state IN ('queued','waiting','receiver_waiting','archive_staged','archive_uploading')`

type custodyHome struct {
	name string
	page *page
	db   *sql.DB
	peer string // the address prefix of the other person's devices
}

type custodySample struct {
	Phase     string  `json:"phase"`
	From      string  `json:"from"`
	AcceptMs  float64 `json:"accept_ms"`
	CustodyMs float64 `json:"custody_ms"`
	ArrivalMs float64 `json:"arrival_ms"`
	Backlog   int     `json:"sync_waiting_at_custody"`
}

type custodyWorld struct {
	t       *testing.T
	c       *cli
	conv    string
	homes   map[string]*custodyHome
	mu      sync.Mutex
	samples []custodySample
}

// TestSendCustodyDuringCatchUp is the send requirement with real programs: accepted
// locally at once, in the relay's custody and at the peer while a linked device catches
// up and while a peer drains what it missed, through a relay outage and a restart.
// Opt-in, since it takes minutes: AGENTNET_CUSTODY=1. A skip here is not a pass.
func TestSendCustodyDuringCatchUp(t *testing.T) {
	if os.Getenv("AGENTNET_CUSTODY") == "" {
		t.Skip("opt-in real-binary journey: set AGENTNET_CUSTODY=1; a skip is not a pass")
	}
	history, samples := custodyCount(t, "AGENTNET_CUSTODY_HISTORY", 3000), custodyCount(t, "AGENTNET_CUSTODY_SAMPLES", 30)
	c := buildCLI(t)
	c.env = append(c.env, "AGENTNET_NOTIFY=off")
	addr, stopHub := c.setup(t, "hub")
	hubRunning := true
	defer func() {
		if hubRunning {
			stopHub()
		}
	}()
	conv := strings.TrimSpace(c.dmReady())
	stopAlice := c.start("alice.log", "--home", "alice", "daemon", "--ui", "127.0.0.1:0")
	aliceRunning := true
	defer func() {
		if aliceRunning {
			stopAlice()
		}
	}()
	stopBob := c.start("bob.log", "--home", "bob", "daemon", "--ui", "127.0.0.1:0")
	bobRunning := true
	defer func() {
		if bobRunning {
			stopBob()
		}
	}()
	w := &custodyWorld{t: t, c: c, conv: conv, homes: map[string]*custodyHome{}}
	w.home("alice", "bob/")
	w.home("bob", "admin/")
	// The DM reaches bob with its first message.
	if _, _, err := w.send("alice", "hello"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the DM on bob's device", 60*time.Second, func() bool {
		out, err := c.try("--home", "bob", "dm", "list")
		return err == nil && strings.Contains(out, conv)
	})

	started := time.Now()
	w.seed(history)
	t.Logf("seeded %d messages both ways in %s", history, time.Since(started).Round(time.Second))

	// A linked device catches up on all of that from the laptop.
	code := ""
	for _, l := range strings.Split(c.run("--home", "alice", "person", "link"), "\n") {
		if strings.HasPrefix(l, "agentnet-link-v2:") {
			code = l
		}
	}
	if code == "" {
		t.Fatal("person link printed no link code")
	}
	c.run("--home", "phone", "join", "--agent", "phone", code)
	stopPhone := c.start("phone.log", "--home", "phone", "daemon", "--ui", "127.0.0.1:0")
	defer stopPhone()
	var linkID string
	waitUntil(t, "the phone's link request", 60*time.Second, func() bool {
		for _, l := range strings.Split(c.run("--home", "alice", "person", "links"), "\n") {
			if f := strings.Fields(l); len(f) >= 3 && f[1] == "pending" && f[2] == "admin/phone" {
				linkID = f[0]
				return true
			}
		}
		return false
	})
	c.run("--home", "alice", "person", "approve", linkID)
	w.home("phone", "bob/")
	// Catch-up starts now: the laptop and bob talk through it, then the phone joins in.
	trace := w.traceCatchUp()
	w.round("linked-device-catchup", []string{"alice", "bob"}, samples)
	waitUntil(t, "the DM on the phone", 120*time.Second, func() bool {
		out, err := c.try("--home", "phone", "dm", "list")
		return err == nil && strings.Contains(out, conv)
	})
	w.round("sending-from-the-device-catching-up", []string{"phone"}, max(samples/3, 1))
	catchUp := trace()
	t.Logf("CATCHUP_TRACE %s", catchUp)
	if !slices.ContainsFunc(w.samples, func(s custodySample) bool { return s.From == "alice" && s.Backlog > 0 }) {
		t.Errorf("no sample from the laptop saw waiting replication rows: overtaking is not shown")
	}

	// A peer comes back to what it missed, while both sides keep talking.
	stopBob()
	bobRunning = false
	missed := max(history/3, 1)
	for i := range missed {
		if _, _, err := w.send("alice", fmt.Sprintf("missed %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "the messages bob missed in custody", 5*time.Minute, func() bool {
		var waiting int
		err := w.homes["alice"].db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient LIKE 'bob/%' AND state IN ('queued','waiting')`).Scan(&waiting)
		return err == nil && waiting == 0
	})
	stopBob = c.start("bob2.log", "--home", "bob", "daemon", "--ui", "127.0.0.1:0")
	bobRunning = true
	defer func() {
		if bobRunning {
			stopBob()
		}
	}()
	w.homes["bob"].page = c.openPage(t, "bob")
	w.round("peer-drains-backlog", []string{"alice", "bob"}, samples)

	// The relay goes away: sending still answers at once, and the message waits, through a
	// restart of the sender, until the relay returns. Exactly one copy arrives.
	stopHub()
	hubRunning = false
	lid, acceptMs, err := w.send("alice", "through the outage")
	if err != nil {
		t.Fatal(err)
	}
	if acceptMs >= 1000 {
		t.Errorf("accepted in %.0f ms with the relay away, want under 1000", acceptMs)
	}
	time.Sleep(40 * time.Second)
	var state string
	if err := w.homes["alice"].db.QueryRow(`SELECT state FROM scheduled_sends WHERE id=?`, lid).Scan(&state); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	} else if err == nil && state == "needs_review" {
		t.Errorf("the message stopped during the outage: %s", state)
	}
	stopAlice()
	aliceRunning = false
	stopAlice = c.start("alice2.log", "--home", "alice", "daemon", "--ui", "127.0.0.1:0")
	aliceRunning = true
	w.homes["alice"].page = c.openPage(t, "alice")
	time.Sleep(5 * time.Second)
	stopHub = c.start("hub2.log", "hub", "serve", "--data", "hub", "--listen", addr)
	hubRunning = true
	back := time.Now()
	waitUntil(t, "the outage message in custody", 3*time.Minute, func() bool {
		var n int
		err := w.homes["alice"].db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient LIKE 'bob/%' AND state IN ('custody','delivered')`, lid).Scan(&n)
		return err == nil && n > 0
	})
	outageCustodyMs := float64(time.Since(back).Milliseconds())
	waitUntil(t, "the outage message at bob", 3*time.Minute, func() bool {
		var n int
		err := w.homes["bob"].db.QueryRow(`SELECT count(*) FROM inbox WHERE lid=?`, lid).Scan(&n)
		return err == nil && n > 0
	})
	outageMs := float64(time.Since(back).Milliseconds())
	time.Sleep(10 * time.Second)
	var copies int
	if err := w.homes["bob"].db.QueryRow(`SELECT count(*) FROM inbox WHERE lid=?`, lid).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Errorf("bob holds %d copies of the outage message, want 1", copies)
	}

	summary := map[string]any{"history": history, "samples": samples, "outage": map[string]any{"accept_ms": acceptMs, "custody_after_relay_returned_ms": outageCustodyMs, "arrival_after_relay_returned_ms": outageMs, "copies": copies}, "phases": w.summary()}
	out, _ := json.Marshal(summary)
	t.Logf("CUSTODY_SUMMARY %s", out)
	for _, s := range w.samples {
		if s.AcceptMs >= 1000 {
			t.Errorf("%s from %s: accepted in %.0f ms, want under 1000", s.Phase, s.From, s.AcceptMs)
		}
	}
	for _, p := range w.summary() {
		if p["arrival_p50_ms"].(float64) > 3000 {
			t.Errorf("%v: live arrival p50 %.0f ms, baseline at most 3000", p["phase"], p["arrival_p50_ms"])
		}
	}
}

func custodyCount(t *testing.T, name string, fallback int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("%s=%q: want a positive count", name, v)
	}
	return n
}

func (w *custodyWorld) home(name, peer string) {
	w.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(w.c.dir, name, "agent.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { db.Close() })
	w.homes[name] = &custodyHome{name: name, page: w.c.openPage(w.t, name), db: db, peer: peer}
}

// send asks the sender's own page to send, as the composer does, and reports how long
// that page took to accept it. It keeps to errors so it may run outside the test goroutine.
func (w *custodyWorld) send(from, body string) (string, float64, error) {
	h := w.homes[from]
	id := protocol.NewID()
	data, _ := json.Marshal(ui.DMDraft{ID: id, Conv: w.conv, Body: body})
	req, _ := http.NewRequest("POST", h.page.base+"/api/dm/send", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.page.base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	start := time.Now()
	resp, err := h.page.cl.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	ms := float64(time.Since(start).Microseconds()) / 1000
	if resp.StatusCode != 200 {
		return "", ms, fmt.Errorf("%s send: status %d", from, resp.StatusCode)
	}
	var sent ui.Sent
	if err := json.Unmarshal(raw, &sent); err != nil {
		return "", ms, err
	}
	if sent.LID == "" {
		return "", ms, fmt.Errorf("%s send: no LID in the answer", from)
	}
	return sent.LID, ms, nil
}

// seed makes real history by sending both ways, then waits until each side stored it.
func (w *custodyWorld) seed(n int) {
	w.t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := range 8 {
		from := []string{"alice", "bob"}[g%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range n / 8 {
				if _, _, err := w.send(from, fmt.Sprintf("history %d %d", g, i)); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		w.t.Fatal(err)
	}
	waitUntil(w.t, "the history at both sides", 10*time.Minute, func() bool {
		for _, h := range w.homes {
			var got int
			if err := h.db.QueryRow(`SELECT count(*) FROM inbox`).Scan(&got); err != nil || got < (n/8)*4 {
				return false
			}
		}
		return true
	})
}

// round sends one message at a time from each sender in turn, and follows each to the
// relay's custody and to the peer without waiting for it before sending the next.
func (w *custodyWorld) round(phase string, senders []string, n int) {
	w.t.Helper()
	var wg sync.WaitGroup
	for i := range n {
		from := senders[i%len(senders)]
		start := time.Now()
		lid, accept, err := w.send(from, fmt.Sprintf("%s %d", phase, i))
		if err != nil {
			w.t.Errorf("%s from %s: %v", phase, from, err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := custodySample{Phase: phase, From: from, AcceptMs: accept}
			h := w.homes[from]
			for {
				if time.Since(start) > 3*time.Minute {
					w.t.Errorf("%s from %s: no custody within 3 minutes", phase, from)
					return
				}
				var got int
				err := h.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient LIKE ? AND state IN ('custody','delivered')`, lid, h.peer+"%").Scan(&got)
				if err == nil && got > 0 {
					s.CustodyMs = float64(time.Since(start).Milliseconds())
					h.db.QueryRow(custodySyncWaiting).Scan(&s.Backlog)
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			peer := "bob"
			if h.peer != "bob/" {
				peer = "alice"
			}
			for {
				if time.Since(start) > 3*time.Minute {
					w.t.Errorf("%s from %s: not at %s within 3 minutes", phase, from, peer)
					return
				}
				var got int
				err := w.homes[peer].db.QueryRow(`SELECT count(*) FROM inbox WHERE lid=?`, lid).Scan(&got)
				if err == nil && got > 0 {
					s.ArrivalMs = float64(time.Since(start).Milliseconds())
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			w.mu.Lock()
			w.samples = append(w.samples, s)
			w.mu.Unlock()
		}()
		time.Sleep(time.Second)
	}
	wg.Wait()
}

// traceCatchUp records, every 2 seconds, replication waiting on the laptop by kind and
// what the phone holds so far. Counts only. The returned func stops it and gives the trace.
func (w *custodyWorld) traceCatchUp() func() string {
	stop, done := make(chan struct{}), make(chan string)
	go func() {
		var lines []string
		start := time.Now()
		for {
			kinds := map[string]int{}
			rows, err := w.homes["alice"].db.Query(`SELECT coalesce(sub,''), state, count(*) FROM outbox WHERE state IN ('queued','waiting','receiver_waiting','archive_staged','archive_uploading') GROUP BY 1, 2`)
			if err == nil {
				for rows.Next() {
					var sub, state string
					var n int
					if rows.Scan(&sub, &state, &n) == nil {
						kinds[sub+"/"+state] = n
					}
				}
				rows.Close()
			}
			var phone int
			w.homes["phone"].db.QueryRow(`SELECT count(*) FROM inbox`).Scan(&phone)
			raw, _ := json.Marshal(kinds)
			lines = append(lines, fmt.Sprintf("%.0fs phone=%d %s", time.Since(start).Seconds(), phone, raw))
			select {
			case <-stop:
				done <- strings.Join(lines, " ; ")
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	return func() string { close(stop); return <-done }
}

func custodyPercentile(values []float64, p int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Sorted(slices.Values(values))
	return sorted[(len(sorted)-1)*p/100]
}

func (w *custodyWorld) summary() []map[string]any {
	var phases []string
	for _, s := range w.samples {
		if !slices.Contains(phases, s.Phase) {
			phases = append(phases, s.Phase)
		}
	}
	var out []map[string]any
	for _, phase := range phases {
		var accept, custody, arrival []float64
		backlogged, most := 0, 0
		for _, s := range w.samples {
			if s.Phase != phase {
				continue
			}
			accept, custody, arrival = append(accept, s.AcceptMs), append(custody, s.CustodyMs), append(arrival, s.ArrivalMs)
			if s.Backlog > 0 {
				backlogged++
			}
			most = max(most, s.Backlog)
		}
		out = append(out, map[string]any{
			"phase": phase, "samples": len(accept),
			"accept_p50_ms": custodyPercentile(accept, 50), "accept_max_ms": custodyPercentile(accept, 100),
			"custody_p50_ms": custodyPercentile(custody, 50), "custody_max_ms": custodyPercentile(custody, 100),
			"arrival_p50_ms": custodyPercentile(arrival, 50), "arrival_max_ms": custodyPercentile(arrival, 100),
			"samples_with_sync_waiting": backlogged, "most_sync_waiting": most,
		})
	}
	return out
}
