package itest

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/tlscert"
)

// setup starts a Hub process from dataDir and enrolls alice/laptop and
// bob/desk. It returns the Hub's listen address and its stop function.
func (c *cli) setup(t *testing.T, dataDir string) (string, func()) {
	t.Helper()
	addr := freeAddr(t)
	stop := c.start("hub-"+dataDir+".log", "hub", "serve", "--data", dataDir, "--listen", addr)
	waitFile(t, filepath.Join(c.dir, dataDir, "bootstrap-invite.txt"))
	code := c.run("hub", "bootstrap-invite", "--raw", "--data", dataDir)
	c.run("--home", "alice", "join", "--agent", "laptop", code)
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	return addr, stop
}

func (c *cli) writeRandom(name string, size int) []byte {
	data := make([]byte, size)
	rand.Read(data)
	os.WriteFile(filepath.Join(c.dir, name), data, 0o600)
	return data
}

func (c *cli) downloadInto(home, dir, id string) []byte {
	os.MkdirAll(filepath.Join(c.dir, dir), 0o700)
	saved := c.run("--home", home, "download", "--dir", dir, id)
	data, _ := os.ReadFile(filepath.Join(c.dir, saved))
	return data
}

// TestCLIBackupRestoreAndCleanup: a stopped Hub is backed up, restored into a
// fresh directory, and serves the old clients an offline file with no trust
// reset; storage cleanup never touches undelivered attachments.
func TestCLIBackupRestoreAndCleanup(t *testing.T) {
	c := buildCLI(t)
	addr, stopHub := c.setup(t, "hub")
	first := c.writeRandom("first.bin", 2<<20)
	id1 := strings.Fields(c.run("--home", "alice", "send", "--file", "first.bin", "bob/desk", "offline file"))[0]

	if out, err := c.try("hub", "backup", "--data", "hub", "--out", "early.tgz"); err == nil || !strings.Contains(out, "stop it first") {
		t.Fatalf("backup of a running Hub: %v %s", err, out)
	}
	stopHub()
	c.run("hub", "backup", "--data", "hub", "--out", "hub.tgz")
	if info, _ := os.Stat(filepath.Join(c.dir, "hub.tgz")); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions %v", info.Mode())
	}
	if out, err := c.try("hub", "restore", "--from", "hub.tgz", "--data", "hub"); err == nil || !strings.Contains(out, "not empty") {
		t.Fatalf("restore over existing data: %v %s", err, out)
	}
	os.WriteFile(filepath.Join(c.dir, "junk.tgz"), []byte("not a backup"), 0o600)
	if out, err := c.try("hub", "restore", "--from", "junk.tgz", "--data", "junk"); err == nil || !strings.Contains(out, "not an AgentNet Hub backup") {
		t.Fatalf("restore of junk: %v %s", err, out)
	}
	if out := c.run("hub", "restore", "--from", "hub.tgz", "--data", "hub2"); !strings.Contains(out, "2 agents") || !strings.Contains(out, "1 attachments") {
		t.Fatalf("restore: %s", out)
	}

	// The restored Hub, same address, same certificate: clients just continue.
	stopHub = c.start("hub2.log", "hub", "serve", "--data", "hub2", "--listen", addr)
	stopBob := c.start("bob.log", "--home", "bob", "daemon")
	waitFor(t, "offline message after restore", func() bool { return len(c.inbox("bob")) == 1 })
	if got := c.downloadInto("bob", "in1", id1); !bytes.Equal(got, first) {
		t.Fatal("restored attachment differs")
	}
	waitFor(t, "delivered", func() bool { return strings.Contains(c.run("--home", "alice", "status", id1), "delivered") })
	if out, _ := c.try("--home", "bob", "doctor"); !strings.Contains(out, "ok   hub") || !strings.Contains(out, "ok   membership") {
		t.Fatalf("doctor: %s", out)
	}

	// A second file stays undelivered while bob is away; cleanup keeps it.
	stopBob()
	second := c.writeRandom("second.bin", 1<<20)
	id2 := strings.Fields(c.run("--home", "alice", "send", "--file", "second.bin", "bob/desk", "later"))[0]
	stopHub()
	time.Sleep(1100 * time.Millisecond) // delivery times have one-second resolution
	if out := c.run("hub", "cleanup", "--data", "hub2", "--delivered-older-than", "0s"); !strings.Contains(out, "removed 1 files") {
		t.Fatalf("cleanup: %s", out)
	}
	if out := c.run("hub", "storage", "--data", "hub2"); !strings.Contains(out, "undelivered (kept)        1 files") {
		t.Fatalf("storage after cleanup: %s", out)
	}
	c.start("hub3.log", "hub", "serve", "--data", "hub2", "--listen", addr)
	c.start("bob2.log", "--home", "bob", "daemon")
	waitFor(t, "second message", func() bool { return len(c.inbox("bob")) == 2 })
	if got := c.downloadInto("bob", "in2", id2); !bytes.Equal(got, second) {
		t.Fatal("undelivered attachment was lost by cleanup")
	}
}

// TestCLIPlatformTLS: the Hub serves plain HTTP behind a TLS-terminating
// proxy; invites carry no pin and clients verify the proxy's certificate
// with their system trust store (SSL_CERT_FILE here, Linux only).
func TestCLIPlatformTLS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses SSL_CERT_FILE to add a test CA to the system trust store")
	}
	c := buildCLI(t)
	backend, public := freeAddr(t), freeAddr(t)
	certPEM, keyPEM, err := tlscert.Generate("127.0.0.1", "test platform", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	os.WriteFile(filepath.Join(c.dir, "ca.pem"), certPEM, 0o600)
	target, _ := url.Parse("http://" + backend)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // stream push events immediately
	ln, err := tls.Listen("tcp", public, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: proxy, ErrorLog: log.New(io.Discard, "", 0)} // the refused join logs a handshake error
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", backend, "--platform-tls", "--public-url", "https://"+public)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code := c.run("hub", "bootstrap-invite", "--raw", "--data", "hub")
	if inv, err := protocol.DecodeInvite(code); err != nil || inv.CertPEM != "" || inv.Hub != "https://"+public {
		t.Fatalf("platform invite %+v %v", inv, err)
	}
	if _, err := c.try("--home", "nobody", "join", "--agent", "x", code); err == nil {
		t.Fatal("joined without trusting the platform certificate")
	}
	c.env = []string{"SSL_CERT_FILE=" + filepath.Join(c.dir, "ca.pem")}
	c.run("--home", "alice", "join", "--agent", "laptop", code)
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	c.start("bob.log", "--home", "bob", "daemon")
	c.run("--home", "alice", "send", "bob/desk", "through the platform")
	waitFor(t, "message via platform TLS", func() bool { return len(c.inbox("bob")) == 1 })
	if _, err := net.DialTimeout("tcp", backend, time.Second); err != nil {
		t.Fatal("backend not listening")
	}
}

// stubClaude is a stand-in `claude` for the responder: it records its
// working directory and prompt, then answers.
const stubClaude = `#!/bin/sh
pwd > "$0.cwd"
cat > "$0.prompt"
echo "automatic answer"
`

// TestCLIFullJourney: question answered automatically in the background,
// task run only after the human accepts, direct delivery, offline wake-up
// with a file, and a revoked agent. All parties are separate processes.
func TestCLIFullJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script")
	}
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	c.run("--home", "carol", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "carol"))
	stubDir := filepath.Join(c.dir, "stub")
	os.MkdirAll(stubDir, 0o700)
	os.WriteFile(filepath.Join(stubDir, "claude"), []byte(stubClaude), 0o700)
	work := filepath.Join(c.dir, "bobwork")
	os.MkdirAll(work, 0o700)
	c.run("--home", "bob", "responder", "set", "--harness", "claude", "--dir", work, "--timeout", "30s")
	c.run("--home", "bob", "approve", "admin/laptop") // the bootstrap admin's label

	c.env = []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	stopBob := c.start("bob.log", "--home", "bob", "daemon", "--listen", freeAddr(t))
	c.env = nil
	c.start("alice.log", "--home", "alice", "daemon")
	waitFor(t, "bob's endpoint", func() bool {
		return strings.Contains(c.run("--home", "alice", "sessions", "bob/desk"), "direct https://")
	})

	q := strings.Fields(c.run("--home", "alice", "ask", "--answer-wait", "0", "bob/desk", "status?"))
	if len(q) != 3 || q[2] != "direct" {
		t.Fatalf("ask: %v", q)
	}
	waitFor(t, "automatic answer", func() bool {
		for _, m := range c.inbox("alice") {
			if m.Kind == "answer" && m.Body == "automatic answer" {
				return true
			}
		}
		return false
	})
	if cwd, _ := os.ReadFile(filepath.Join(stubDir, "claude.cwd")); strings.TrimSpace(string(cwd)) != work {
		t.Fatalf("responder ran in %q", cwd)
	}

	task := strings.Fields(c.run("--home", "alice", "task", "bob/desk", "tidy up"))[0]
	waitFor(t, "task waiting", func() bool {
		for _, m := range c.inbox("bob") {
			if m.ID == task && m.State == "awaiting" {
				return true
			}
		}
		return false
	})
	c.run("--home", "bob", "accept", task)
	waitFor(t, "task result", func() bool {
		for _, m := range c.inbox("alice") {
			if m.Kind == "result" {
				return true
			}
		}
		return false
	})

	stopBob()
	data := c.writeRandom("report.bin", 3<<20)
	sent := strings.Fields(c.run("--home", "alice", "send", "--file", "report.bin", "bob/desk", "while you were away"))
	if sent[2] != "relay" {
		t.Fatalf("offline send: %v", sent)
	}
	c.start("bob2.log", "--home", "bob", "daemon")
	waitFor(t, "offline file", func() bool {
		for _, m := range c.inbox("bob") {
			if m.ID == sent[0] {
				return true
			}
		}
		return false
	})
	if got := c.downloadInto("bob", "in", sent[0]); !bytes.Equal(got, data) {
		t.Fatal("file differs")
	}

	c.run("--home", "alice", "admin", "revoke", "carol/desk")
	if out, err := c.try("--home", "carol", "send", "bob/desk", "hi"); err == nil || !strings.Contains(out, "revoked") {
		t.Fatalf("revoked send: %v %s", err, out)
	}
}
