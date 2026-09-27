package itest

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cli runs the real agentnet binary as separate processes.
type cli struct {
	t   *testing.T
	bin string
	dir string
}

func buildCLI(t *testing.T) *cli {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX signals to stop processes")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "agentnet")
	cmd := exec.Command("go", "build", "-o", bin, "../cmd/agentnet")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return &cli{t, bin, dir}
}

func (c *cli) run(args ...string) string {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Dir = c.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		c.t.Fatalf("agentnet %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// start runs a long-lived process; the returned func interrupts and reaps it.
func (c *cli) start(logName string, args ...string) func() {
	c.t.Helper()
	log, err := os.Create(filepath.Join(c.dir, logName))
	if err != nil {
		c.t.Fatal(err)
	}
	cmd := exec.Command(c.bin, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = c.dir, log, log
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cmd.Process.Signal(os.Interrupt)
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
	c.t.Cleanup(stop)
	return stop
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
// 10 MiB file, the Hub restarts, and the recipient, offline until then,
// downloads a byte-identical copy.
func TestCLIFileJourney(t *testing.T) {
	c := buildCLI(t)
	addr := freeAddr(t)
	hub := []string{"hub", "serve", "--data", "hub", "--listen", addr}
	stopHub := c.start("hub1.log", hub...)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code, _ := os.ReadFile(filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "bob"))

	data := make([]byte, 10<<20+777)
	rand.Read(data)
	copy(data, "CLI-PLAINTEXT-MARKER")
	os.WriteFile(filepath.Join(c.dir, "quarterly-secret.bin"), data, 0o600)
	out := c.run("--home", "alice", "send", "--file", "quarterly-secret.bin", "bob/desk", "numbers attached")
	if !strings.HasSuffix(out, " custody") {
		t.Fatalf("send: %s", out)
	}
	id := strings.Fields(out)[0]

	stopHub()
	c.start("hub2.log", hub...)
	stopBob := c.start("bob.log", "--home", "bob", "daemon")
	waitFor(t, "bob to receive", func() bool {
		var msgs []struct{ ID string }
		json.Unmarshal([]byte(c.run("--home", "bob", "inbox", "--json")), &msgs)
		return len(msgs) == 1 && msgs[0].ID == id
	})
	stopBob()
	os.Mkdir(filepath.Join(c.dir, "out"), 0o700)
	saved := c.run("--home", "bob", "download", "--dir", "out", id)
	got, err := os.ReadFile(filepath.Join(c.dir, saved))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded file differs (%v)", err)
	}
	if st := c.run("--home", "alice", "status", id); !strings.HasSuffix(st, " delivered") {
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
