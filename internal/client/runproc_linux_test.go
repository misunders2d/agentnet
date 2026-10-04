//go:build linux

package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// runTestHarnessParent is the test binary run as a daemon (TestMain,
// AGENTNET_TEST_HARNESS_PARENT=FILE): it starts a harness as the worker
// does (ownProcessGroup), which writes its pid to FILE, and waits to be
// killed.
func runTestHarnessParent(pidFile string) int {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo $$ > '"+pidFile+".tmp' && mv '"+pidFile+".tmp' '"+pidFile+"' && exec sleep 60")
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cmd.Wait() // until killed (the harness sleeps longer than any test)
	return 0
}

// procGone reports whether process pid has ended (exited, or a zombie no one
// reaped yet).
func procGone(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(data), ')')
	return i >= 0 && strings.HasPrefix(strings.TrimSpace(string(data[i+1:])), "Z")
}

func readPid(t *testing.T, file string) int {
	t.Helper()
	var pid int
	eventually(t, "pid in "+file, func() bool {
		data, err := os.ReadFile(file)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	})
	return pid
}

// BUG-09: a harness dies with the daemon that started it, however the
// daemon dies (SIGKILL here): before, it was reparented and kept running,
// unseen, and a rerun after the restart ran beside it.
func TestHarnessDiesWithItsDaemon(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "harness.pid")
	daemon := exec.Command(os.Args[0], "-test.run=^$")
	daemon.Env = append(os.Environ(), "AGENTNET_TEST_HARNESS_PARENT="+pidFile)
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	harness := readPid(t, pidFile)
	t.Cleanup(func() { syscall.Kill(-harness, syscall.SIGKILL) })
	if procGone(harness) {
		t.Fatal("setup: the harness is not running")
	}
	daemon.Process.Kill()
	daemon.Wait()
	eventually(t, "the harness to die with its daemon", func() bool { return procGone(harness) })
}

// BUG-09: a daemon starting after one that died stops what is left of a
// run it recorded before marking it interrupted, so that run never goes on
// beside a rerun: the harness itself, and a helper it started that
// outlived it (proven the run's by its environment). A process group
// recorded with another start is never touched.
func TestStartStopsSurvivingHarness(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	stopBob := runAgent(t, w.bob)
	var ids []string
	for _, body := range []string{"left running", "left running too", "never ran here"} {
		q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: body, Kind: envelope.KindQuestion})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "bob holds "+body, func() bool { return inboxCount(t, w.bob, `id = ?`, q.ID) == 1 })
		ids = append(ids, q.ID)
	}
	stopBob()
	home, err := filepath.Abs(w.bob.home)
	if err != nil {
		t.Fatal(err)
	}
	// start runs script as a run's harness would be left: its own group,
	// the run's environment. ended yields once it has been waited for.
	start := func(script string) (pgid int, ended <-chan error) {
		t.Helper()
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "AGENTNET_HOME="+home, BackgroundEnv+"=1") // as runJob gives a run
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
		return cmd.Process.Pid, done
	}
	// A run whose harness is still running, as a daemon that died left it.
	pgid, leaderEnded := start("sleep 60 & wait")
	// A run whose harness died, but whose helper outlived it.
	helperFile := filepath.Join(t.TempDir(), "helper.pid")
	orphanGroup, orphanEnded := start("sleep 60 & echo $! > '" + helperFile + "'; wait")
	helper := readPid(t, helperFile)
	recorded := procStart(orphanGroup)
	syscall.Kill(orphanGroup, syscall.SIGKILL) // the leader alone
	<-orphanEnded
	// An unrelated process group, recorded with a start that is not its own.
	otherGroup, _ := start("sleep 60")
	for i, r := range []struct {
		pgid  int
		start string
	}{{pgid, procStart(pgid)}, {orphanGroup, recorded}, {otherGroup, procStart(otherGroup) + "1"}} {
		if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateRunning, ids[i]); err != nil {
			t.Fatal(err)
		}
		if err := w.bob.store.setRunGroup(ids[i], r.pgid, r.start); err != nil {
			t.Fatal(err)
		}
	}
	runAgent(t, w.bob)
	for _, id := range ids {
		waitState(t, w.bob, id, stateInterrupt)
	}
	select {
	case <-leaderEnded:
	case <-time.After(10 * time.Second):
		t.Fatal("the harness left running was not stopped")
	}
	eventually(t, "the run's surviving helper stopped", func() bool { return procGone(helper) })
	if procGone(otherGroup) {
		t.Fatal("an unrelated process group was stopped")
	}
	if n := inboxCount(t, w.bob, `run_pgid IS NOT NULL`); n != 0 {
		t.Fatalf("%d interrupted job(s) keep a process group", n)
	}
}
