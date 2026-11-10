package client

// Warm native workers (MEL-746 Part B). A conversation agent's native
// process (Pi/OMP RPC, Claude stream-json, Codex app-server) stays alive
// after a clean answer, so the same person's next turn in the same saved
// session goes into the running process instead of a cold start.
//
// Adapted from Heimdall cd39bdb0530bffc7aa1a276270fe005901511d22
// internal/harness/warm.go: one process per (session scope, verified
// person, harness); limits idle 900 s, lifetime 14400 s, 8 idle (LRU);
// only a clean completed turn is retained; a changed launch fingerprint
// (arguments, directory, environment, program file, native settings files)
// discards the worker; idle workers hold no run slot; a worker lost while
// idle is forgotten and the next turn resumes the saved session cold; work
// is never re-run. AgentNet differences: planSession (session.go) stays the
// only authority on which saved session a job continues, so a worker is
// reused only for a resume of its exact native session id; per-job
// environment values become one stable handle that the CLI resolves to the
// current turn (RunCause), since a running process's environment is fixed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// warmLimits bound retained processes. Zero values select the defaults.
type warmLimits struct {
	idle, lifetime time.Duration
	max            int
	off            bool // no warm workers: every turn starts its own process
}

func (l warmLimits) withDefaults() warmLimits {
	if l.idle <= 0 {
		l.idle = 900 * time.Second
	}
	if l.lifetime <= 0 {
		l.lifetime = 14400 * time.Second
	}
	if l.max <= 0 {
		l.max = 8
	}
	return l
}

// warmGrace is how long a discarded worker gets to end by itself after its
// input closes (native session-end hooks run then) before its process group
// is killed.
var warmGrace = 5 * time.Second

// warmHarness reports the harnesses whose native transport keeps a session
// open across turns over one process.
func warmHarness(name string) bool {
	return name == "pi" || name == "omp" || name == "claude" || name == "codex"
}

// warmKey: the exact saved native session (planSession scopes it to one
// conversation/participation/topic, verified person, agent, mode, directory
// and preset), the verified person and the harness.
type warmKey struct{ session, person, harness string }

// warmSink routes a retained process's output to the current turn only.
type warmSink struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *warmSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		_, _ = s.w.Write(p)
	}
	return len(p), nil
}

func (s *warmSink) set(w io.Writer) { s.mu.Lock(); s.w = w; s.mu.Unlock() }

// warmProc is one retained native process.
type warmProc struct {
	pool    *warmPool
	harness string
	fp      string
	handle  string // stable request handle in its environment (RunCause)
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	wmu     sync.Mutex // one writer at a time on the native input
	out     warmSink
	errOut  warmSink
	exited  chan struct{}
	waitErr error
	born    time.Time
	turns   int

	// Codex: the owned thread and its last reported model.
	thread string
	model  modelObservation

	// under pool.mu
	key       warmKey
	idleAt    time.Time
	timer     *time.Timer
	discarded bool
}

// warmInput is one turn's view of the native input. Closing it ends that
// turn's writes only; the process input stays open for the next turn.
type warmInput struct {
	p      *warmProc
	closed atomic.Bool
}

func (in *warmInput) Write(b []byte) (int, error) {
	if in.closed.Load() {
		return 0, os.ErrClosed
	}
	in.p.wmu.Lock()
	defer in.p.wmu.Unlock()
	if in.closed.Load() {
		return 0, os.ErrClosed
	}
	return in.p.stdin.Write(b)
}

func (in *warmInput) Close() error { in.closed.Store(true); return nil }

func (p *warmProc) input() *warmInput { return &warmInput{p: p} }

func (p *warmProc) alive() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// warmPool belongs to one daemon run (worker): it is created when the
// worker starts and closed, with every process it holds, when it stops.
type warmPool struct {
	a        *Agent
	limits   warmLimits
	mu       sync.Mutex
	idle     map[warmKey]*warmProc
	live     map[*warmProc]struct{}
	closed   bool
	spawns   atomic.Int64 // processes started (tests)
	lastTurn atomic.Int64 // the last turn's launch-to-answer time, ns (tests)
}

func newWarmPool(a *Agent, l warmLimits) *warmPool {
	// Handles of an earlier daemon name processes that no longer exist.
	if _, err := a.store.db.Exec(`DELETE FROM config WHERE k LIKE 'warm-run:%'`); err != nil {
		a.Logf("warm workers: earlier handles not cleared: %v", err)
	}
	return &warmPool{a: a, limits: l.withDefaults(), idle: map[warmKey]*warmProc{}, live: map[*warmProc]struct{}{}}
}

// take removes and returns key's idle worker, or nil. The caller reuses it
// for exactly this turn or discards it before any cold start of the same
// session: one session never has two processes.
func (p *warmPool) take(k warmKey) *warmProc {
	p.mu.Lock()
	defer p.mu.Unlock()
	w := p.idle[k]
	if w == nil {
		return nil
	}
	delete(p.idle, k)
	if w.timer != nil {
		w.timer.Stop()
	}
	return w
}

// fits reports whether w may serve a turn that may run for timeout: alive,
// same launch, and its lifetime leaves room for the whole turn.
func (p *warmPool) fits(w *warmProc, fp string, timeout time.Duration) (bool, string) {
	p.mu.Lock()
	lifetime := p.limits.lifetime
	p.mu.Unlock()
	switch {
	case !w.alive():
		return false, "its native process had exited"
	case w.fp != fp:
		return false, "its launch or native settings changed"
	case time.Since(w.born)+timeout >= lifetime:
		return false, "it reached its lifetime"
	}
	return true, ""
}

// spawn prepares a retained process for cmd (arguments already set for the
// native transport). The caller starts it after the first turn's bridge is
// attached (start).
func (p *warmPool) spawn(cmd *exec.Cmd, harness, fp, handle string) (*warmProc, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errors.New("the worker is stopping")
	}
	cmd.Stdin = nil
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	w := &warmProc{pool: p, harness: harness, fp: fp, handle: handle, cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = &w.out, &w.errOut
	return w, nil
}

func (w *warmProc) start() error {
	p := w.pool
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = w.stdin.Close()
		return errors.New("the worker is stopping")
	}
	if err := w.cmd.Start(); err != nil {
		p.mu.Unlock()
		_ = w.stdin.Close()
		return err
	}
	w.born = time.Now()
	p.live[w] = struct{}{}
	p.mu.Unlock()
	p.spawns.Add(1)
	go w.wait()
	return nil
}

func (w *warmProc) wait() {
	err := w.cmd.Wait()
	p := w.pool
	p.mu.Lock()
	w.waitErr = err
	wasIdle := p.idle[w.key] == w
	if wasIdle {
		delete(p.idle, w.key)
		if w.timer != nil {
			w.timer.Stop()
		}
	}
	delete(p.live, w)
	discarded := w.discarded
	p.mu.Unlock()
	close(w.exited)
	stopGroup(w.cmd) // helpers it left behind in its group
	p.a.dropWarmBinding(w.handle)
	if wasIdle && !discarded {
		// Nothing ran: the next turn resumes the saved session in a new
		// process. Nothing is run again.
		p.a.Logf("warm %s worker of session %s exited while idle (%v); its next turn starts a new process", w.harness, w.key.session, err)
	}
}

// release retains w, after a clean answer, as key's idle worker. It reports
// false when w was discarded instead.
func (p *warmPool) release(w *warmProc, k warmKey) bool {
	p.mu.Lock()
	if p.closed || !w.alive() || k.session == "" || time.Since(w.born) >= p.limits.lifetime {
		p.mu.Unlock()
		w.discard("not retained")
		return false
	}
	if old := p.idle[k]; old != nil && old != w {
		delete(p.idle, k)
		go old.discard("replaced")
	}
	w.key, w.idleAt = k, time.Now()
	p.idle[k] = w
	wait := min(p.limits.idle, time.Until(w.born.Add(p.limits.lifetime)))
	w.timer = time.AfterFunc(max(wait, time.Millisecond), func() {
		if p.take(k) == w {
			w.discard("idle")
		}
	})
	var evict []*warmProc
	for len(p.idle) > p.limits.max {
		var oldest *warmProc
		for _, v := range p.idle {
			if oldest == nil || v.idleAt.Before(oldest.idleAt) {
				oldest = v
			}
		}
		delete(p.idle, oldest.key)
		oldest.timer.Stop()
		evict = append(evict, oldest)
	}
	p.mu.Unlock()
	for _, v := range evict {
		go v.discard("least recently used")
	}
	return true
}

// discard ends w: its input closes so the harness can end its session by
// itself (its own session-end hooks), then, after warmGrace, its process
// group is killed. It returns once the process has exited.
func (w *warmProc) discard(why string) { w.end(why, nil, warmGrace) }

// stop ends a turn that is still running (cancelled, timed out, stopped):
// a native interrupt, then the process group is killed shortly after.
func (w *warmProc) stop(why string, interrupt any) { w.end(why, interrupt, time.Second) }

func (w *warmProc) end(why string, interrupt any, grace time.Duration) {
	p := w.pool
	p.mu.Lock()
	first := !w.discarded
	w.discarded = true
	if p.idle[w.key] == w {
		delete(p.idle, w.key)
	}
	p.mu.Unlock()
	if first && w.cmd.Process != nil {
		p.a.Logf("warm %s worker of session %s ended: %s", w.harness, w.key.session, why)
	}
	if w.cmd.Process == nil {
		_ = w.stdin.Close()
		return
	}
	if first {
		if interrupt != nil {
			w.wmu.Lock()
			_ = json.NewEncoder(w.stdin).Encode(interrupt)
			w.wmu.Unlock()
		}
		_ = w.stdin.Close()
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-w.exited:
		return
	case <-t.C:
	}
	_ = killGroup(runGroup(w.cmd))
	_ = w.cmd.Process.Kill() // no process group on Windows
	<-w.exited
}

// nativeInterrupt is the harness's own stop request for its current turn.
func nativeInterrupt(harness, thread, turn string) any {
	switch harness {
	case "pi", "omp":
		return map[string]any{"type": "abort"}
	case "claude":
		return map[string]any{"type": "control_request", "request_id": "agentnet-interrupt", "request": map[string]any{"subtype": "interrupt"}}
	case "codex":
		if thread != "" && turn != "" {
			return map[string]any{"id": "agentnet-interrupt", "method": "turn/interrupt", "params": map[string]any{"threadId": thread, "turnId": turn}}
		}
	}
	return nil
}

// discardIdle ends every idle worker and waits for them (an update waits
// for running jobs only; idle workers are not jobs).
func (p *warmPool) discardIdle(why string) {
	p.mu.Lock()
	var all []*warmProc
	for k, w := range p.idle {
		delete(p.idle, k)
		if w.timer != nil {
			w.timer.Stop()
		}
		all = append(all, w)
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, w := range all {
		wg.Add(1)
		go func() { defer wg.Done(); w.discard(why) }()
	}
	wg.Wait()
}

// close ends every process of the pool; no new one starts.
func (p *warmPool) close() {
	p.mu.Lock()
	p.closed = true
	var all []*warmProc
	for w := range p.live {
		all = append(all, w)
	}
	p.mu.Unlock()
	p.discardIdle("the worker stopped")
	var wg sync.WaitGroup
	for _, w := range all {
		wg.Add(1)
		go func() { defer wg.Done(); w.discard("the worker stopped") }()
	}
	wg.Wait()
}

// setLimits changes the bounds for workers retained from now on.
func (p *warmPool) setLimits(l warmLimits) { p.mu.Lock(); p.limits = l.withDefaults(); p.mu.Unlock() }

func (p *warmPool) idleCount() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.idle) }

// warmFingerprint names everything a native process is launched with,
// except the session flags and the per-job values that a warm worker gets
// through its handle: its program file, arguments, directory, environment,
// the names of its per-job variables and the native settings files.
// Transcripts and credentials are never hashed.
func warmFingerprint(harness, mode, preset, dir, path string, args, env, perJob []string) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode([]any{harness, mode, preset, dir, path, args, env, perJob})
	if info, err := os.Stat(path); err == nil {
		_ = enc.Encode([]any{info.Size(), info.ModTime().UnixNano()})
	}
	home, _ := os.UserHomeDir()
	for _, f := range nativeSettingsFiles(harness, home, dir, env) {
		h.Write([]byte(f))
		data, err := os.ReadFile(f)
		switch {
		case err == nil:
			if filepath.Base(f) == ".claude.json" {
				var cfg map[string]json.RawMessage
				if json.Unmarshal(data, &cfg) == nil {
					data = cfg["mcpServers"] // the rest is Claude's own changing state
				}
			}
			h.Write(data)
		case !os.IsNotExist(err):
			h.Write([]byte(time.Now().String())) // unreadable: never reused
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// nativeSettingsFiles are the harness's own configuration files (Heimdall
// nativeSettings): a change discards a warm worker.
func nativeSettingsFiles(harness, home, dir string, env []string) []string {
	get := func(name string) string {
		for _, kv := range slices.Backward(env) {
			if k, v, ok := strings.Cut(kv, "="); ok && k == name {
				return v
			}
		}
		return ""
	}
	switch harness {
	case "claude":
		return []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".claude.json"), filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json"), filepath.Join(dir, ".mcp.json")}
	case "codex":
		codexHome := get("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return []string{filepath.Join(codexHome, "config.toml"), filepath.Join(dir, ".codex", "config.toml")}
	case "pi", "omp":
		agentDir := filepath.Join(home, "."+harness, "agent")
		if harness == "pi" && get("PI_CODING_AGENT_DIR") != "" {
			agentDir = get("PI_CODING_AGENT_DIR")
		}
		return []string{filepath.Join(agentDir, "settings.json"), filepath.Join(dir, "."+harness, "settings.json")}
	}
	return nil
}

// warmRunKey is the config row that maps a warm worker's handle to the job
// its current turn serves ("" while idle).
func warmRunKey(handle string) string { return "warm-run:" + handle }

func (a *Agent) setWarmBinding(handle, jobID string, attempt int64) error {
	v := ""
	if jobID != "" {
		v = jobID + "/" + strconv.FormatInt(attempt, 10)
	}
	_, err := a.store.db.Exec(`INSERT INTO config(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, warmRunKey(handle), v)
	return err
}

func (a *Agent) dropWarmBinding(handle string) {
	if _, err := a.store.db.Exec(`DELETE FROM config WHERE k=?`, warmRunKey(handle)); err != nil {
		a.Logf("warm worker handle not removed: %v", err)
	}
}

// RunCause resolves a request value from a background run's environment.
// A warm worker's process keeps one handle for its whole life; it names the
// request of the turn running now. warm reports a handle; id is then ""
// while no turn runs. Any other value is returned unchanged. The value is
// never authority: the request must still be running with that attempt.
func (a *Agent) RunCause(v string) (id string, attempt int, warm bool) {
	if !protocol.ValidID(v) {
		return v, 0, false
	}
	var bound string
	if err := a.store.db.QueryRow(`SELECT v FROM config WHERE k=?`, warmRunKey(v)).Scan(&bound); err != nil {
		return v, 0, false
	}
	id, n, ok := strings.Cut(bound, "/")
	if !ok {
		return "", 0, true
	}
	attempt, err := strconv.Atoi(n)
	if err != nil || !protocol.ValidID(id) {
		return "", 0, true
	}
	return id, attempt, true
}

// ErrNoWarmTurn: a warm worker's handle while none of its turns runs.
var ErrNoWarmTurn = errors.New("this background session has no running request now")

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
