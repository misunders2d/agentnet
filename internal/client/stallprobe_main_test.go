package client

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

var probeFiles []*os.File

// stopStallProbe removes the probe's files; the process exits next.
func stopStallProbe() {
	for _, f := range probeFiles {
		f.Close()
		os.Remove(f.Name())
	}
}

// startStallProbe, with AGENTNET_TEST_STALL_PROBE set, measures in the
// background what a slow CI runner does to every test at once: how late the
// Go scheduler wakes a sleeper, and how long one 4 KiB write plus disk sync
// takes in the OS temp directory, where the tests keep their stores, and
// for comparison in the user's cache directory (on a Windows runner, the
// system volume C:). It prints metadata only, to stderr: each sample past
// its threshold, and a summary every 30 s.
//
// Windows runners' C: volume has disk-sync stalls of 30-60 s, several per
// job (sync p50 ~10 ms rising to 0.3-0.8 s, peaks of 1.5-4.3 s), while
// RUNNER_TEMP's D: volume stays at a few ms; windows-client keeps its
// temp directory on D: (go.yml) and this probe shows whether it still does.
func startStallProbe() {
	if os.Getenv("AGENTNET_TEST_STALL_PROBE") == "" {
		return
	}
	dirs := map[string]string{"temp": os.TempDir()}
	if d, err := os.UserCacheDir(); err == nil && filepath.VolumeName(d) != filepath.VolumeName(os.TempDir()) {
		dirs["cache"] = d
	}
	// The wall clock's step: how far time.Now().UnixNano() moves when it
	// moves (stores order rows by such stamps; equal stamps tie).
	var steps []time.Duration
	for range 200 {
		start := time.Now().UnixNano()
		next := start
		for next == start {
			next = time.Now().UnixNano()
		}
		steps = append(steps, time.Duration(next-start))
	}
	slices.Sort(steps)
	fmt.Fprintf(os.Stderr, "stall probe: wall clock step p50=%s max=%s\n", steps[len(steps)/2], steps[len(steps)-1])
	var mu sync.Mutex
	samples := map[string][]time.Duration{}
	note := func(name string, d time.Duration, slow time.Duration) {
		mu.Lock()
		samples[name] = append(samples[name], d)
		mu.Unlock()
		if d >= slow {
			fmt.Fprintf(os.Stderr, "stall probe: %s %s=%dms\n", time.Now().UTC().Format("15:04:05.000"), name, d.Milliseconds())
		}
	}
	go func() {
		for {
			start := time.Now()
			time.Sleep(100 * time.Millisecond)
			note("sched_lag", time.Since(start)-100*time.Millisecond, 250*time.Millisecond)
		}
	}()
	for name, dir := range dirs {
		f, err := os.CreateTemp(dir, "agentnet-stall-probe-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "stall probe: %s unavailable: %v\n", name, err)
			continue
		}
		probeFiles = append(probeFiles, f)
		fmt.Fprintf(os.Stderr, "stall probe: %s on volume %s\n", name, filepath.VolumeName(dir))
		go func() {
			block := make([]byte, 4096)
			for {
				start := time.Now()
				if _, err := f.WriteAt(block, 0); err == nil {
					err = f.Sync()
				}
				note(name+"_sync", time.Since(start), 250*time.Millisecond)
				time.Sleep(500 * time.Millisecond)
			}
		}()
	}
	go func() {
		var last runtime.MemStats
		runtime.ReadMemStats(&last)
		for {
			time.Sleep(30 * time.Second)
			var now runtime.MemStats
			runtime.ReadMemStats(&now)
			mu.Lock()
			var parts []string
			for name, s := range samples {
				slices.Sort(s)
				parts = append(parts, fmt.Sprintf("%s n=%d p50=%dms p99=%dms max=%dms", name, len(s), s[len(s)/2].Milliseconds(), s[len(s)*99/100].Milliseconds(), s[len(s)-1].Milliseconds()))
			}
			samples = map[string][]time.Duration{}
			mu.Unlock()
			slices.Sort(parts)
			fmt.Fprintf(os.Stderr, "stall probe summary: %s %s goroutines=%d gc=%d gc_pause=%dms heap=%dMiB\n", time.Now().UTC().Format("15:04:05"), strings.Join(parts, "; "), runtime.NumGoroutine(), now.NumGC-last.NumGC, time.Duration(now.PauseTotalNs-last.PauseTotalNs).Milliseconds(), now.HeapAlloc>>20)
			last = now
		}
	}()
}

// logGoroutineGroups, with AGENTNET_TEST_STALL_PROBE set, logs what every
// goroutine of the test process waits in, grouped by identical stack
// (function and line only, no argument values), most common first.
func logGoroutineGroups(t interface{ Logf(string, ...any) }) {
	if os.Getenv("AGENTNET_TEST_STALL_PROBE") == "" {
		return
	}
	buf := make([]byte, 8<<20)
	buf = buf[:runtime.Stack(buf, true)]
	groups := map[string]int{}
	for _, g := range strings.Split(string(buf), "\n\n") {
		lines := strings.Split(strings.TrimSpace(g), "\n")
		if len(lines) < 2 {
			continue
		}
		state := lines[0]
		if i, j := strings.Index(state, "["), strings.Index(state, "]"); i >= 0 && j > i {
			state = state[i+1 : j]
			if k := strings.Index(state, ","); k >= 0 {
				state = state[:k] // drop the wait duration
			}
		}
		var frames []string
		for i := 1; i+1 < len(lines) && len(frames) < 10; i += 2 {
			fn := lines[i]
			if k := strings.LastIndex(fn, "("); k > 0 {
				fn = fn[:k]
			}
			loc := strings.TrimSpace(lines[i+1])
			if k := strings.Index(loc, " +0x"); k >= 0 {
				loc = loc[:k]
			}
			if k := strings.LastIndexAny(loc, `/\`); k >= 0 {
				loc = loc[k+1:]
			}
			frames = append(frames, fn+" "+loc)
		}
		groups["["+state+"] "+strings.Join(frames, " < ")]++
	}
	type group struct {
		stack string
		n     int
	}
	var all []group
	for s, n := range groups {
		all = append(all, group{s, n})
	}
	slices.SortFunc(all, func(a, b group) int { return b.n - a.n })
	for i, g := range all {
		if i == 40 {
			t.Logf("goroutines: %d more groups", len(all)-i)
			break
		}
		t.Logf("goroutines x%d %s", g.n, g.stack)
	}
}
