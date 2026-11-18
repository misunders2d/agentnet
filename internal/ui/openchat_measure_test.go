package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// TestOpenChatMeasure times what opening a chat costs the host, as the page
// does it: the chat, its read mark, the list after that write, the chat again
// and the open's refresh. AGENTNET_OPEN_MEASURE=1 runs it; "busy" keeps
// messages arriving in another chat throughout. Opt-in: timings are evidence
// for docs/INTERACTION_COVERAGE.md, not a pass/fail bound.
func TestOpenChatMeasure(t *testing.T) {
	if os.Getenv("AGENTNET_OPEN_MEASURE") == "" {
		t.Skip("opt-in latency measurement (AGENTNET_OPEN_MEASURE=1)")
	}
	home := filepath.Join(t.TempDir(), "bob")
	alice, bob, live, _ := liveWorldHubAt(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	runDaemon(t, alice)
	var conv string
	for deadline := time.Now().Add(20 * time.Second); conv == ""; time.Sleep(50 * time.Millisecond) {
		c, err := alice.CreateDM(ctx, bob.Address)
		if err == nil {
			conv = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	group := liveTwoMemberGroup(t, alice, bob)
	const base = 64
	for i := range base {
		if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: fmt.Sprintf("message %d with some ordinary words in it", i)}); err != nil {
			t.Fatal(err)
		}
	}
	waitOpen(t, func() bool {
		th, err := live.DM(conv)
		return err == nil && len(th.Messages) >= base
	})
	side, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer side.Close()
	if os.Getenv("AGENTNET_OPEN_COVER") != "" { // as with the scoped-keys step appended
		if _, err := side.Exec(client.ViewCoverSchema); err != nil {
			t.Fatal(err)
		}
	}
	cols := tableColumns(t, side, "inbox")
	made := 0
	grow := func(copies int) { // received rows only: what a long chat is made of
		for range copies {
			k := made
			made++
			sel := make([]string, len(cols))
			for i, c := range cols {
				switch c {
				case "id", "lid":
					sel[i] = fmt.Sprintf("%s||'-m%d'", c, k)
				case "arrival":
					sel[i] = "NULL"
				default:
					sel[i] = c
				}
			}
			list := strings.Join(cols, ",")
			if _, err := side.Exec(`INSERT INTO inbox(`+list+`) SELECT `+strings.Join(sel, ",")+` FROM inbox WHERE conv=? AND local=0 AND id NOT LIKE '%-m%'`, conv); err != nil {
				t.Fatal(err)
			}
		}
		bob.NoteChange() // a write the store did not make: say so, as a second process's wake does
	}
	var stopBusy atomic.Bool
	busy := func() func() {
		stopBusy.Store(false)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; !stopBusy.Load(); i++ {
				alice.SendConv(ctx, group, client.ConvOutgoing{Body: fmt.Sprintf("busy %d", i)})
			}
		}()
		return func() { stopBusy.Store(true); <-done }
	}
	var dms atomic.Int64
	dmProjected = func(c string) {
		if c == conv {
			dms.Add(1)
		}
	}
	oracle, dmCheck := overviewOracle, dmOracle
	overviewOracle, dmOracle = nil, nil // their fresh rebuilds are not the page's cost
	defer func() { dmProjected, overviewOracle, dmOracle = nil, oracle, dmCheck }()
	runs := 5 // opens per cell; AGENTNET_OPEN_RUNS for a longer tail
	if n, err := strconv.Atoi(os.Getenv("AGENTNET_OPEN_RUNS")); err == nil && n > 0 {
		runs = n
	}
	stacks := os.Getenv("AGENTNET_OPEN_STACKS") // a goroutine dump of the first open phase past 1 s
	var phases [5][]time.Duration
	var trace []string // what moved in each phase of the last open
	where := ""
	open := func() (steps []time.Duration, builds int) {
		before := dms.Load()
		start := time.Now()
		last, phase := start, 0
		mark := func() { steps = append(steps, time.Since(start)) }
		v0, _ := bob.ViewChanges()
		_, m0, _ := bob.ChatViewMark(conv)
		g0, _ := bob.ViewMark()
		b0 := dms.Load()
		trace = trace[:0]
		lap := func() {
			d := time.Since(last)
			phases[phase] = append(phases[phase], d)
			v, _ := bob.ViewChanges()
			_, m, _ := bob.ChatViewMark(conv)
			keys, g, _ := bob.ViewDirtySince(g0)
			b := dms.Load()
			trace = append(trace, fmt.Sprintf("%d: built %d content+%d unscoped+%d chatmark %v keys %v", phase, b-b0, v.Content-v0.Content, v.Unscoped-v0.Unscoped, m != m0, keys))
			v0, m0, g0, b0 = v, m, g, b
			last, phase = time.Now(), phase+1
		}
		if stacks != "" {
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-done:
				case <-time.After(time.Second):
					if f, err := os.OpenFile(stacks+"-"+where, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
						fmt.Fprintf(f, "phase %d at %v\n", phase, time.Since(start))
						pprof.Lookup("goroutine").WriteTo(f, 2)
						f.Close()
					}
				}
			}()
		}
		th, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		mark()
		lap()
		var ids []string
		for _, m := range th.Messages {
			if m.Unread {
				ids = append(ids, m.ID)
			}
		}
		if len(ids) > 0 {
			if _, err := live.Act(Action{Do: DoRead, IDs: ids}); err != nil {
				t.Fatal(err)
			}
		}
		lap()
		if _, err := live.Overview(); err != nil {
			t.Fatal(err)
		}
		lap()
		if _, err := live.DM(conv); err != nil {
			t.Fatal(err)
		}
		lap()
		if _, err := live.Refresh(conv); err != nil {
			t.Fatal(err)
		}
		lap()
		mark()
		return steps, int(dms.Load() - before)
	}
	unread := func() { // makes the next open an unread one again
		if _, err := side.Exec(`UPDATE inbox SET read_at=NULL WHERE id IN (SELECT id FROM inbox WHERE conv=? AND local=0 ORDER BY received_ms DESC, id DESC LIMIT 3)`, conv); err != nil {
			t.Fatal(err)
		}
		bob.NoteChange()
	}
	dump := func(size string) { // the page's payloads, for timing them in a browser engine
		dir := os.Getenv("AGENTNET_OPEN_DUMP")
		if dir == "" {
			return
		}
		for name, p := range map[string]DMPage{"whole": {GroupRefs: true}, "page50": {Limit: 50}} {
			th, err := live.DMPage(conv, p)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(th)
			if err := os.WriteFile(filepath.Join(dir, size+"-"+name+".json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	report := func(size string) {
		dump(size)
		for _, load := range []string{"idle", "busy"} {
			var stop func()
			if load == "busy" {
				stop = busy()
				time.Sleep(300 * time.Millisecond)
			}
			var first, all []time.Duration
			var builds []int
			phases, where = [5][]time.Duration{}, size+"-"+load
			for range runs {
				unread()
				steps, n := open()
				first, all, builds = append(first, steps[0]), append(all, steps[1]), append(builds, n)
			}
			// Change events meanwhile, as GET /events tells them: those that spare
			// the open chat (scoped, naming only other chats) of all, and why not.
			prev, _ := live.ViewChanges()
			_, mark, _ := live.ViewScope(-1)
			events, spared, unscoped := 0, 0, 0
			seen := map[string]bool{}
			for range 20 {
				time.Sleep(50 * time.Millisecond)
				v, _ := live.ViewChanges()
				if v.Content == prev.Content {
					continue
				}
				events++
				keys, next, ok := live.ViewScope(mark)
				mark = next
				if v.Unscoped > prev.Seq {
					unscoped++
				}
				mine := !ok || v.Unscoped > prev.Seq || len(keys) == 0
				for _, k := range keys {
					seen[k[:min(len(k), 6)]] = true
					mine = mine || k != "h" && (!strings.HasPrefix(k, "c:") || k == "c:"+conv) // as the page (store.ts scopedRefetch)
				}
				if !mine {
					spared++
				}
				prev = v
			}
			anew := fmt.Sprintf("%d of %d events spare the open chat (%d unscoped; keys %v)", spared, events, unscoped, slices.Sorted(maps.Keys(seen)))
			if stop != nil {
				stop()
			}
			slices.Sort(first)
			slices.Sort(all)
			var reads []time.Duration
			for range 3 {
				start := time.Now()
				if _, err := bob.MessagingConversationMessages(conv); err != nil {
					t.Fatal(err)
				}
				reads = append(reads, time.Since(start))
			}
			slices.Sort(reads)
			var pages []time.Duration
			for range 3 {
				unread()
				start := time.Now()
				if _, err := live.DMPage(conv, DMPage{Limit: 50}); err != nil {
					t.Fatal(err)
				}
				pages = append(pages, time.Since(start))
			}
			slices.Sort(pages)
			var per []string
			for i, name := range []string{"dm", "read", "list", "dm", "refresh"} {
				p := slices.Sorted(slices.Values(phases[i]))
				per = append(per, fmt.Sprintf("%s %v/%v", name, p[len(p)/2].Round(time.Millisecond), p[len(p)-1].Round(time.Millisecond)))
			}
			t.Logf("PHASES %s %s (p50/max): %s", size, load, strings.Join(per, ", "))
			t.Logf("TRACE %s %s last open: %s", size, load, strings.Join(trace, " | "))
			th, _ := live.DM(conv)
			t.Logf("MEASURE %s %s msgs=%d: chat shown p50 %v max %v; whole open p50 %v max %v; chat projections per open %v; messages read alone p50 %v; first page of 50 p50 %v; %s",
				size, load, len(th.Messages), first[runs/2].Round(time.Millisecond), first[runs-1].Round(time.Millisecond), all[runs/2].Round(time.Millisecond), all[runs-1].Round(time.Millisecond), builds, reads[1].Round(time.Millisecond), pages[1].Round(time.Millisecond), anew)
		}
	}
	grow(9) // 640 rows: the owner's biggest chat
	report("normal")
	grow(90) // 6400 rows: 10x
	if path := os.Getenv("AGENTNET_OPEN_PROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		for range 5 {
			live.DM(conv)
		}
		pprof.StopCPUProfile()
		f.Close()
	}
	report("10x")
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

func waitOpen(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
	}
}
