package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestSockPathFitsForLongHomes(t *testing.T) {
	short := filepath.Join(t.TempDir(), "h")
	if len(filepath.Join(short, "daemon.sock")) <= maxSockPath && sockPath(short) != filepath.Join(short, "daemon.sock") {
		t.Fatalf("short home moved its socket: %s", sockPath(short))
	}
	long := filepath.Join(t.TempDir(), strings.Repeat("very-long-directory-name-", 6), "home")
	p := sockPath(long)
	if len(p) > maxSockPath || strings.HasPrefix(p, long) {
		t.Fatalf("long home socket %q (%d bytes)", p, len(p))
	}
	if other := sockPath(long + "2"); other == p {
		t.Fatal("two homes share a socket")
	}
	if sockPath(long) != p {
		t.Fatal("socket path not stable")
	}
}

// A daemon whose home path is too long for a socket still wakes at once when
// another process accepts a task (no waiting for the next Hub ping).
func TestAcceptWakesDaemonWithLongHome(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	long := filepath.Join(t.TempDir(), strings.Repeat("very-long-directory-name-", 6), "bob")
	os.MkdirAll(filepath.Dir(long), 0o700)
	bob := mustJoin(t, long, w.aliceInvites("longbob"), "desk")
	setResponder(t, bob, "stub", st.dir, time.Minute)
	runWith(t, w, bob, RunOptions{}) // heartbeat is the default 90 s
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: bob.Address, Body: "go", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, task.ID, stateAwaiting)
	other, err := Open(long) // the CLI is a separate Agent on the same home
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := time.Now()
	if err := other.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, task.ID, stateAnswered)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("worker woke after %s: the local wake-up did not work", d)
	}
}

// Stopping the wake-up listener waits for a wake it is running, so what
// the stopper changes next (the daemon's stop replaces wakeWorker, which a
// wake reads) is ordered after that wake: no data race under -race, and no
// wake reads what stop's caller set afterwards.
func TestListenKicksStopWaitsForWake(t *testing.T) {
	home := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	target := func() {}
	stop, err := listenKicks(home, func() {
		entered <- struct{}{}
		<-release
		time.Sleep(50 * time.Millisecond) // still running when stop is called
		target()
	})
	if err != nil {
		t.Fatal(err)
	}
	notifyDaemon(home)
	<-entered
	close(release)
	stop()
	target = func() { t.Error("a wake read what was set after stop") }
	time.Sleep(200 * time.Millisecond) // a wake stop did not wait for runs meanwhile
}
