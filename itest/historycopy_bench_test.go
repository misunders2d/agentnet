package itest

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHistoryCopyBenchmark measures the linked-device history copy with real
// programs: a Hub process, the existing device's daemon and a newly linked
// device's daemon, all separate processes. The source holds N synthetic text
// messages in one DM, cloned from one real received row in a single SQL
// statement (as the 10k archive test does), so seeding costs seconds.
//
// Opt-in, never part of the normal suite:
//
//	AGENTNET_HISTORY_BENCH=70000 [AGENTNET_HISTORY_BENCH_LIMIT=30m] \
//	  go test ./itest -run TestHistoryCopyBenchmark -v -count=1 -timeout 2h
//
// It reports wall time from approval to the last imported message, progress
// each 10 s, the archive parts the source made and the new device's peak RSS
// (VmHWM). With a limit it stops early and reports the measured rate.
func TestHistoryCopyBenchmark(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("AGENTNET_HISTORY_BENCH"))
	if n <= 0 {
		t.Skip("set AGENTNET_HISTORY_BENCH=N to run the history copy benchmark")
	}
	limit := 2 * time.Hour
	if v := os.Getenv("AGENTNET_HISTORY_BENCH_LIMIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		limit = d
	}
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	conv := c.dmReady()
	stopAlice := c.start("alice-seed.log", "--home", "alice", "daemon")
	stopBob := c.start("bob.log", "--home", "bob", "daemon")
	c.run("--home", "alice", "dm", "send", conv, "hello bob")
	waitFor(t, "bob's copy of the DM", func() bool {
		out, err := c.try("--home", "bob", "dm", "list")
		return err == nil && strings.TrimSpace(out) != ""
	})
	c.run("--home", "bob", "dm", "send", c.dmOf("bob"), "seed message")
	waitFor(t, "alice receives the seed", func() bool {
		out, err := c.try("--home", "alice", "dm", "show", conv)
		return err == nil && strings.Contains(out, "seed message")
	})
	stopBob()
	stopAlice()

	// One statement clones the received row: distinct IDs, ascending
	// arrival order, bodies bulk-1..bulk-N.
	source := filepath.Join(c.dir, "alice", "agent.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(source)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err = db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND body='seed message'`, conv).Scan(&original); err != nil {
		t.Fatal(err)
	}
	seeded := time.Now()
	if _, err = db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?) INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv,lid,origin,received_ms,verified_by,acked) SELECT lower(hex(randomblob(16))),i.sender,i.ts,i.kind,'bulk-'||n.x,i.received_at,i.conv,lower(hex(randomblob(16))),i.origin,i.received_ms+n.x,i.verified_by,1 FROM n JOIN inbox i ON i.id=?`, n, original); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Logf("seeded %d source rows in %s", n, time.Since(seeded))

	c.start("alice.log", "--home", "alice", "daemon")
	c.start("bob2.log", "--home", "bob", "daemon")
	var code string
	lines := strings.Split(c.run("--home", "alice", "person", "link"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Device link code") && i+1 < len(lines) {
			code = strings.TrimSpace(lines[i+1])
		}
	}
	if code == "" {
		t.Fatal("no device link code")
	}
	c.run("--home", "phone", "join", "--agent", "phone", code)
	log, err := os.Create(filepath.Join(c.dir, "phone.log"))
	if err != nil {
		t.Fatal(err)
	}
	phone := exec.Command(c.bin, "--home", "phone", "daemon")
	phone.Dir, phone.Stdout, phone.Stderr = c.dir, log, log
	if err = phone.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Process.Signal(os.Interrupt); phone.Wait(); log.Close() })
	var link string
	waitFor(t, "the link request", func() bool {
		for _, line := range strings.Split(c.run("--home", "alice", "person", "links"), "\n") {
			if f := strings.Fields(line); len(f) > 1 && f[1] == "pending" {
				link = f[0]
			}
		}
		return link != ""
	})
	c.run("--home", "alice", "person", "approve", link)
	start := time.Now()

	target := filepath.Join(c.dir, "phone", "agent.db")
	count := func() int {
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(target)+"?mode=ro&_pragma=busy_timeout(2000)")
		if err != nil {
			return -1
		}
		defer db.Close()
		var got int
		if db.QueryRow(`SELECT count(*) FROM inbox WHERE body LIKE 'bulk-%'`).Scan(&got) != nil {
			return -1
		}
		return got
	}
	hwm := func() string {
		f, err := os.Open(fmt.Sprintf("/proc/%d/status", phone.Process.Pid))
		if err != nil {
			return "n/a"
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			if strings.HasPrefix(s.Text(), "VmHWM:") {
				return strings.TrimSpace(strings.TrimPrefix(s.Text(), "VmHWM:"))
			}
		}
		return "n/a"
	}
	parts := func() string {
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(source)+"?mode=ro&_pragma=busy_timeout(2000)")
		if err != nil {
			return "n/a"
		}
		defer db.Close()
		var archives, entries, biggest int
		db.QueryRow(`SELECT count(DISTINCT manifest),count(*),coalesce((SELECT max(c) FROM (SELECT count(*) c FROM history_archive_entries GROUP BY manifest)),0) FROM history_archive_entries`).Scan(&archives, &entries, &biggest)
		return fmt.Sprintf("parts=%d entries=%d largest=%d", archives, entries, biggest)
	}
	got, last := 0, time.Now()
	for got < n {
		time.Sleep(time.Second)
		if v := count(); v >= 0 {
			got = v
		}
		if time.Since(last) >= 10*time.Second {
			last = time.Now()
			t.Logf("t=%s imported=%d/%d rate=%.0f/min %s phoneVmHWM=%s", time.Since(start).Round(time.Second), got, n, float64(got)/time.Since(start).Minutes(), parts(), hwm())
		}
		if time.Since(start) > limit {
			t.Logf("LIMIT %s reached: imported=%d/%d rate=%.0f/min extrapolated total=%s %s phoneVmHWM=%s", limit, got, n, float64(got)/time.Since(start).Minutes(), time.Duration(float64(time.Since(start))*float64(n)/float64(max(got, 1))).Round(time.Minute), parts(), hwm())
			return
		}
	}
	t.Logf("RESULT n=%d wall=%s rate=%.0f/min %s phoneVmHWM=%s", n, time.Since(start).Round(time.Second), float64(n)/time.Since(start).Minutes(), parts(), hwm())
}
