package itest

import (
	"bytes"
	"context"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// cli runs the real agentnet binary as separate processes.
type cli struct {
	t   *testing.T
	bin string
	dir string
	env []string // extra environment for every process
}

func buildCLI(t *testing.T) *cli {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "agentnet")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "../cmd/agentnet")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	c := &cli{t: t, bin: bin, dir: dir}
	t.Cleanup(func() { // registered first, so it runs after the processes stop
		if t.Failed() {
			logs, _ := filepath.Glob(filepath.Join(dir, "*.log"))
			for _, l := range logs {
				data, _ := os.ReadFile(l)
				t.Logf("--- %s\n%s", filepath.Base(l), data)
			}
		}
	})
	return c
}

// run returns a successful command's stdout, which is what scripts read;
// explanations on stderr are left out.
func (c *cli) run(args ...string) string {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), c.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		c.t.Fatalf("agentnet %s: %v\n%s%s", strings.Join(args, " "), err, out, stderr.Bytes())
	}
	return strings.TrimSpace(string(out))
}

// try runs a command that may fail and returns its output and error.
func (c *cli) try(args ...string) (string, error) {
	cmd := exec.Command(c.bin, args...)
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), c.env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// persons gives each home a person: a plain send from a person's shell goes
// in their DM (MEL-713), which needs both.
func (c *cli) persons(homes ...string) {
	c.t.Helper()
	for _, h := range homes {
		c.run("--home", h, "person", "create", h)
	}
}

// dmOf is the ID of home's newest DM, where its plain sends went.
func (c *cli) dmOf(home string) string {
	c.t.Helper()
	lines := strings.Split(c.run("--home", home, "dm", "list"), "\n")
	return strings.Fields(lines[len(lines)-1])[0]
}

// oldSend sends m from home as an older AgentNet sends it: a version 1
// device message, which no command here starts any more (MEL-713 step 1)
// but devices not yet updated still send, so this one still takes them in.
// It waits up to 5s for the receipt unless m says otherwise.
func (c *cli) oldSend(home string, m client.Outgoing) client.SendResult {
	c.t.Helper()
	a, err := client.Open(filepath.Join(c.dir, home))
	if err != nil {
		c.t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if m.Wait == 0 {
		m.Wait = 5 * time.Second
	}
	r, err := a.SendMessage(ctx, m)
	if err != nil {
		c.t.Fatalf("old-style send from %s: %v", home, err)
	}
	return r
}

// dmWithBob gives alice and bob persons and returns alice's DM with
// bob/desk, whose daemon runs: a device takes part in conversations once it
// has connected since it joined.
func (c *cli) dmWithBob() string {
	c.t.Helper()
	c.persons("alice", "bob")
	var conv string
	waitFor(c.t, "a DM with bob", func() bool {
		out, err := c.try("--home", "alice", "dm", "new", "bob/desk")
		conv = out
		return err == nil
	})
	return conv
}

// dmReady gives alice and bob persons and alice a DM with bob/desk, the
// conversation it returns. bob's daemon runs once for it: a device takes
// part in conversations once it has connected since it joined.
func (c *cli) dmReady() string {
	c.t.Helper()
	stop := c.start("bob-first.log", "--home", "bob", "daemon")
	defer stop()
	return c.dmWithBob()
}

// start runs a long-lived process; the returned func interrupts and reaps it.
func (c *cli) start(logName string, args ...string) func() {
	c.t.Helper()
	stop, _ := c.startProc(logName, args...)
	return stop
}

// gracefulStop reports whether start's stop lets the process clean up.
// On Windows it can only kill: there is no console interrupt for a child.
var gracefulStop = runtime.GOOS != "windows"

// startProc is start that also returns kill, which ends the process at once
// with no cleanup, as a crash or power cut would.
func (c *cli) startProc(logName string, args ...string) (stop, kill func()) {
	c.t.Helper()
	log, err := os.Create(filepath.Join(c.dir, logName))
	if err != nil {
		c.t.Fatal(err)
	}
	cmd := exec.Command(c.bin, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = c.dir, log, log
	cmd.Env = append(os.Environ(), c.env...)
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	stopped := false
	end := func(force bool) {
		if stopped {
			return
		}
		stopped = true
		if force || !gracefulStop {
			cmd.Process.Kill() // SQLite recovers
		} else {
			cmd.Process.Signal(os.Interrupt)
		}
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		log.Close()
	}
	stop = func() { end(false) }
	c.t.Cleanup(stop)
	return stop, func() { end(true) }
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	waitFor(t, path, func() bool { _, err := os.Stat(path); return err == nil })
}

// TestCLIFileJourney: the sender exits after the Hub takes custody of a
// 10 MiB file sent in its DM, the Hub restarts, and the recipient, offline
// until then, downloads a byte-identical copy. A DM needs the recipient to
// have connected once since it joined.
func TestCLIFileJourney(t *testing.T) {
	c := buildCLI(t)
	addr := freeAddr(t)
	hub := []string{"hub", "serve", "--data", "hub", "--listen", addr}
	stopHub := c.start("hub1.log", hub...)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code, _ := os.ReadFile(filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	conv := c.dmReady()

	data := make([]byte, 10<<20+777)
	rand.Read(data)
	copy(data, "CLI-PLAINTEXT-MARKER")
	os.WriteFile(filepath.Join(c.dir, "quarterly-secret.bin"), data, 0o600)
	out := c.run("--home", "alice", "send", "--file", "quarterly-secret.bin", "bob/desk", "numbers attached")
	if f := strings.Fields(out); len(f) != 3 || f[1] != "custody" || f[2] != "relay" {
		t.Fatalf("send: %s", out)
	}
	id := strings.Fields(out)[0]

	stopHub()
	c.start("hub2.log", hub...)
	stopBob := c.start("bob.log", "--home", "bob", "daemon")
	waitFor(t, "bob to receive", func() bool {
		out, err := c.try("--home", "bob", "dm", "show", conv)
		return err == nil && strings.Contains(out, "numbers attached")
	})
	// Storage and its asynchronous relay receipt are separate facts. Keep
	// the recipient running until the sender can observe that receipt.
	waitFor(t, "relay to confirm bob's stored message", func() bool {
		return c.run("--home", "alice", "status", id) == id+" delivered relay"
	})
	stopBob()
	os.Mkdir(filepath.Join(c.dir, "out"), 0o700)
	saved := c.run("--home", "bob", "download", "--dir", "out", id)
	got, err := os.ReadFile(filepath.Join(c.dir, saved))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded file differs (%v)", err)
	}
	if st := c.run("--home", "alice", "status", id); st != id+" delivered relay" {
		t.Fatalf("status: %s", st)
	}
	filepath.Walk(filepath.Join(c.dir, "hub"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte("CLI-PLAINTEXT-MARKER")) || bytes.Contains(b, []byte("quarterly-secret")) {
				t.Errorf("plaintext or file name in Hub file %s", p)
			}
		}
		return nil
	})
}

// TestCLISendReportsDelivery: with the recipient's daemon running, send (in
// the DM) reports "delivered" (first field stays the message id); --wait 0
// returns at once with custody; with the recipient offline it says the Hub
// holds it. A person's plain shell asks no agent directly.
func TestCLISendReportsDelivery(t *testing.T) {
	c := buildCLI(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", freeAddr(t))
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", c.run("hub", "bootstrap-invite", "--raw", "--data", "hub"))
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	c.persons("alice", "bob")
	stopBob := c.start("bob.log", "--home", "bob", "daemon")
	waitFor(t, "bob online", func() bool {
		return strings.Contains(c.run("--home", "alice", "sessions", "bob/desk"), "connected")
	})
	waitFor(t, "a DM with bob", func() bool {
		_, err := c.try("--home", "alice", "dm", "new", "bob/desk")
		return err == nil
	})

	out := c.run("--home", "alice", "send", "bob/desk", "hello")
	if f := strings.Fields(out); len(f) != 3 || f[1] != "delivered" || f[2] != "relay" {
		t.Fatalf("online send stdout: %q", out)
	}
	if both, _ := c.try("--home", "alice", "send", "bob/desk", "again"); !strings.Contains(both, "not necessarily read or answered") {
		t.Fatalf("online send explanation:\n%s", both)
	}
	if f := strings.Fields(c.run("--home", "alice", "send", "--wait", "0", "bob/desk", "at once")); f[1] != "custody" {
		t.Fatalf("--wait 0: %v", f)
	}
	if out, err := c.try("--home", "alice", "ask", "--answer-wait", "0", "bob/desk", "q?"); err == nil || !strings.Contains(out, "dm ask-agent") {
		t.Fatalf("plain-shell ask: %v\n%s", err, out)
	}
	stopBob()
	out, err := c.try("--home", "alice", "send", "--wait", "1s", "bob/desk", "while away")
	if f := strings.Fields(out); err != nil || f[1] != "custody" || !strings.Contains(out, "held by the Hub") {
		t.Fatalf("offline send: %v\n%s", err, out)
	}
	id := strings.Fields(out)[0]
	if st := c.run("--home", "alice", "status", "--wait", "500ms", id); st != id+" custody relay" {
		t.Fatalf("status --wait: %s", st)
	}
}
