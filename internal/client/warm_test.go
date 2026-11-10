package client

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// warmFixture installs harness name as the existing native fake-harness
// fixture (TestNativeFixtureAnswerProcess; Codex: TestCodexSessionFixtureProcess)
// with a cold-start delay, and a conversation with bob's agent in it.
func warmFixture(t *testing.T, name string, startMS string) (st *stub, w *world, conv, pid string) {
	t.Helper()
	st = installWarmHarness(t, name, startMS)
	var lids []string
	w, conv, lids, _ = agentWorld(t)
	setResponder(t, w.bob, name, st.dir, time.Minute)
	pid = participate(t, w, conv, lids[:1], nil)
	return st, w, conv, pid
}

// installWarmHarness installs harness name as the existing native fixture.
func installWarmHarness(t *testing.T, name string, startMS string) (st *stub) {
	t.Helper()
	const help = "--print -p --no-session --extension --session-dir --continue --output-format --no-session-persistence --permission-mode --add-dir --input-format --verbose --replay-user-messages --include-partial-messages --session-id --resume --auto-approve --mode"
	st = installStub(t, "answer")
	old := Harnesses[name]
	h := old
	if name == "codex" {
		stub := installCodexSessionStub(t, st.log)
		// The same fixture, after a cold start's cost.
		h.bin = filepath.Join(t.TempDir(), "codex")
		if err := os.WriteFile(h.bin, []byte("#!/bin/sh\nsleep "+strconv.FormatFloat(float64(mustAtoi(t, startMS))/1000, 'f', 3, 64)+"\nexec \""+stub+"\" \"$@\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("STUB_THREAD", "fresh")
		t.Setenv("STUB_NOISE", "0")
		t.Setenv("STUB_TURN", "")
	} else {
		h.bin = filepath.Join(t.TempDir(), name)
		script := "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo '" + help + "'; exit 0; fi\necho \"run args=$*\" >> \"$STUB_LOG\"\nexport AGENTNET_NATIVE_STUB_PROMPT=\"$STUB_LOG.stdin\"\n" +
			nativeAnswerLauncher(t, name, "native answer\n\nemotion: calm\n")
		if err := os.WriteFile(h.bin, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTNET_NATIVE_STUB_START_MS", startMS)
	}
	Harnesses[name] = h
	t.Cleanup(func() { Harnesses[name] = old })
	return st
}

func mustAtoi(t *testing.T, s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func warmPoolOf(t *testing.T, a *Agent) *warmPool {
	t.Helper()
	p := a.warm.Load()
	if p == nil {
		t.Fatal("no warm pool: the daemon is not running")
	}
	return p
}

func (s *stub) prompt() string { data, _ := os.ReadFile(s.log + ".stdin"); return string(data) }

// MEL-746 Part B, acceptance 1: the same person's next message in the same
// conversation goes into the live native process of their saved session (no
// new spawn) and is answered faster than the cold start.
func TestWarmWorkerContinuesSameSession(t *testing.T) {
	for _, name := range []string{"claude", "pi", "omp", "codex"} {
		t.Run(name, func(t *testing.T) {
			st, w, conv, pid := warmFixture(t, name, "1500")
			pool := warmPoolOf(t, w.bob)
			q1 := convAsk(t, w.alice, conv, pid, "first")
			// Launch to answer: AgentNet's own setup before it is the same
			// for both turns and is not measured.
			cold := time.Duration(pool.lastTurn.Load())
			ref := sessionOf(t, w.bob, q1)
			if ref == nil || ref.ID == "" {
				t.Fatalf("no saved session: %+v", ref)
			}
			eventually(t, "the worker kept warm", func() bool { return pool.idleCount() == 1 })
			q2 := convAsk(t, w.alice, conv, pid, "second")
			warm := time.Duration(pool.lastTurn.Load())
			if runs := st.runs(); len(runs) != 1 || pool.spawns.Load() != 1 {
				t.Fatalf("the second message started another process: %v (spawns %d)", runs, pool.spawns.Load())
			}
			if r2 := sessionOf(t, w.bob, q2); r2 == nil || r2.ID != ref.ID {
				t.Fatalf("second turn's session %+v, want %s", r2, ref.ID)
			}
			if name != "codex" && !strings.Contains(st.prompt(), "second") {
				t.Fatalf("the live process did not get the second turn: %q", st.prompt())
			}
			if name == "codex" {
				raw, _ := os.ReadFile(st.log + ".prompt")
				if !strings.Contains(string(raw), "second") {
					t.Fatalf("the live app-server did not get the second turn: %q", raw)
				}
				rpc, _ := os.ReadFile(st.log + ".rpc")
				if strings.Count(string(rpc), "thread/") != 1 {
					t.Fatalf("the warm turn opened its thread again: %s", rpc)
				}
			}
			if cold < 1500*time.Millisecond {
				t.Fatalf("the fixture's cold start was not paid: %v", cold)
			}
			if warm >= cold/2 {
				t.Fatalf("warm turn %v not faster than cold %v", warm, cold)
			}
			t.Logf("%s: cold %v, warm %v", name, cold, warm)
		})
	}
}

// The owner's direct chat with their own agent (a device conversation: the
// session is the verified sender's reply chain) keeps its process warm too.
func TestWarmWorkerDeviceConversation(t *testing.T) {
	st := installWarmHarness(t, "claude", "0")
	w := newWorld(t, "")
	setResponder(t, w.bob, "claude", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	pool := warmPoolOf(t, w.bob)
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	if ref == nil || ref.ID == "" || ref.Person != "" {
		t.Fatalf("not a device-conversation session: %+v", ref)
	}
	eventually(t, "the worker kept warm", func() bool { return pool.idleCount() == 1 })
	q2, _ := ask(t, w, a1)
	if runs := st.runs(); len(runs) != 1 || pool.spawns.Load() != 1 {
		t.Fatalf("the device conversation's next ask started another process: %v (spawns %d)", runs, pool.spawns.Load())
	}
	if r2 := sessionOf(t, w.bob, q2); r2 == nil || r2.ID != ref.ID {
		t.Fatalf("second ask's session %+v, want %s", r2, ref.ID)
	}
	pool.mu.Lock()
	var key warmKey
	for k := range pool.idle {
		key = k
	}
	pool.mu.Unlock()
	if key.person != w.alice.Address || key.session != ref.ID {
		t.Fatalf("warm key %+v", key)
	}
}

// Acceptance 2: another person in the same conversation never gets the
// first person's process; each keeps their own.
func TestWarmWorkerPerPerson(t *testing.T) {
	st, w, conv, pid := warmFixture(t, "claude", "0")
	pool := warmPoolOf(t, w.bob)
	a1 := convAsk(t, w.alice, conv, pid, "alice first")
	b1 := convAsk(t, w.bob, conv, pid, "bob first")
	if ra, rb := sessionOf(t, w.bob, a1), sessionOf(t, w.bob, b1); ra == nil || rb == nil || ra.ID == rb.ID {
		t.Fatalf("sessions %+v %+v", ra, rb)
	}
	if n := len(st.runs()); n != 2 {
		t.Fatalf("another person's turn reused a process: %d runs", n)
	}
	eventually(t, "two warm workers", func() bool { return pool.idleCount() == 2 })
	convAsk(t, w.alice, conv, pid, "alice second")
	convAsk(t, w.bob, conv, pid, "bob second")
	if n := len(st.runs()); n != 2 || pool.spawns.Load() != 2 {
		t.Fatalf("each person's own worker not reused: %d runs, %d spawns", n, pool.spawns.Load())
	}
}

// Acceptance 3: an idle worker ends after its idle timeout, the least
// recently used one beyond max_warm is evicted, and the next turn resumes
// the saved session in a new process.
func TestWarmWorkerIdleTimeoutAndLRU(t *testing.T) {
	st, w, conv, pid := warmFixture(t, "claude", "0")
	pool := warmPoolOf(t, w.bob)
	pool.setLimits(warmLimits{idle: 200 * time.Millisecond})
	q1 := convAsk(t, w.alice, conv, pid, "first")
	ref := sessionOf(t, w.bob, q1)
	eventually(t, "the idle worker to expire", func() bool { pool.mu.Lock(); defer pool.mu.Unlock(); return len(pool.live) == 0 })
	convAsk(t, w.alice, conv, pid, "after idle")
	if runs := st.runs(); len(runs) != 2 || !strings.Contains(runs[1], "--resume "+ref.ID) {
		t.Fatalf("after idle expiry the session was not resumed cold: %v", runs)
	}

	pool.setLimits(warmLimits{max: 1})
	convAsk(t, w.alice, conv, pid, "alice again")
	eventually(t, "alice's worker warm", func() bool { return pool.idleCount() == 1 })
	convAsk(t, w.bob, conv, pid, "bob")
	eventually(t, "alice's worker evicted for bob's", func() bool {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		return len(pool.idle) == 1 && len(pool.live) == 1
	})
	convAsk(t, w.alice, conv, pid, "alice last")
	if runs := st.runs(); len(runs) != 5 || !strings.Contains(runs[4], "--resume "+ref.ID) {
		t.Fatalf("evicted worker's session not resumed cold: %v", runs)
	}
}

// Acceptance 5: an update proceeds while warm workers are idle; they are
// discarded first and never count as running jobs.
func TestWarmWorkerUpdateDiscardsIdle(t *testing.T) {
	_, w, conv, pid := warmFixture(t, "omp", "0")
	pool := warmPoolOf(t, w.bob)
	convAsk(t, w.alice, conv, pid, "first")
	eventually(t, "warm", func() bool { return pool.idleCount() == 1 })
	if !w.bob.executorsIdle() {
		t.Fatal("an idle warm worker counted as a running job")
	}
	resume, err := w.bob.PauseForAppUpdate()
	if err != nil {
		t.Fatalf("update refused with only an idle warm worker: %v", err)
	}
	defer resume()
	pool.mu.Lock()
	idle, live := len(pool.idle), len(pool.live)
	pool.mu.Unlock()
	if idle != 0 || live != 0 {
		t.Fatalf("idle warm workers left for the update: idle %d live %d", idle, live)
	}
}

// A warm process keeps one handle in its environment; the CLI resolves it to
// the turn running now, and to nothing between turns.
func TestWarmRunCause(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	handle, job := protocol.NewID(), protocol.NewID()
	if id, _, warm := a.RunCause(job); warm || id != job {
		t.Fatalf("an ordinary request id changed: %s %v", id, warm)
	}
	if err := a.setWarmBinding(handle, job, 3); err != nil {
		t.Fatal(err)
	}
	if id, n, warm := a.RunCause(handle); !warm || id != job || n != 3 {
		t.Fatalf("current turn %s/%d %v", id, n, warm)
	}
	if err := a.setWarmBinding(handle, "", 0); err != nil {
		t.Fatal(err)
	}
	if id, _, warm := a.RunCause(handle); !warm || id != "" {
		t.Fatalf("idle handle resolved to %q", id)
	}
	a.dropWarmBinding(handle)
	if id, _, warm := a.RunCause(handle); warm || id != handle {
		t.Fatalf("dropped handle %q %v", id, warm)
	}
}
