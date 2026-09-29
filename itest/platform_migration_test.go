//go:build linux

package itest

import (
	"bytes"
	"crypto/tls"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/tlscert"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Existing pinned installations keep their endpoint and keys while the Hub
// adds a platform-TLS endpoint for new browser invitations. Test certificates
// are trusted through SSL_CERT_FILE; this is not a deployed browser test.
func TestCLIPinnedClientsSurvivePlatformTLSMigration(t *testing.T) {
	c := buildCLI(t)
	old, stop := c.setup(t, "hub")
	content := c.writeRandom("before.bin", 1024)
	id := strings.Fields(c.run("--home", "alice", "send", "--file", "before.bin", "bob/desk", "before migration"))[0]
	stop()
	oldCert, err := tls.LoadX509KeyPair(filepath.Join(c.dir, "hub", "tls.crt"), filepath.Join(c.dir, "hub", "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	backend, public := freeAddr(t), freeAddr(t)
	pem, key, err := tlscert.Generate("127.0.0.1", "new public endpoint", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	newCert, err := tls.X509KeyPair(pem, key)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("http://" + backend)
	for addr, cert := range map[string]tls.Certificate{old: oldCert, public: newCert} {
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.FlushInterval = -1
		ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
		if err != nil {
			t.Fatal(err)
		}
		s := &http.Server{Handler: proxy, ErrorLog: log.New(io.Discard, "", 0)}
		go s.Serve(ln)
		t.Cleanup(func() { s.Close() })
	}
	c.start("migrated.log", "hub", "serve", "--data", "hub", "--listen", backend, "--platform-tls", "--web", "--public-url", "https://"+public)
	waitFor(t, "backend ready", func() bool {
		r, e := http.Get("http://" + backend + "/v1/version")
		if e != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	})
	c.start("bob.log", "--home", "bob", "daemon")
	waitFor(t, "old pinned client receives existing custody", func() bool { return len(c.inbox("bob")) == 1 })
	if got := c.downloadInto("bob", "received", id); !bytes.Equal(got, content) {
		t.Fatal("stored file lost during TLS transition")
	}
	c.run("--home", "alice", "send", "bob/desk", "after migration")
	waitFor(t, "old pinned client sends after migration", func() bool { return len(c.inbox("bob")) == 2 })
	if err := os.WriteFile(filepath.Join(c.dir, "newca.pem"), pem, 0600); err != nil {
		t.Fatal(err)
	}
	c.env = []string{"SSL_CERT_FILE=" + filepath.Join(c.dir, "newca.pem")}
	link, err := c.try("--home", "alice", "admin", "invite", "--link", "browser-person")
	if err != nil {
		t.Fatalf("migrated Hub ready for browser but existing admin cannot issue browser link: %v %s", err, link)
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		t.Fatal(err)
	}
	inv, err := protocol.DecodeInvite(u.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != public || inv.Hub != "https://"+public || inv.CertPEM != "" {
		t.Fatal("browser invite did not use the new trusted endpoint")
	}
	c.run("--home", "newdevice", "join", "--agent", "phone", u.Fragment)
	c.run("--home", "newdevice", "send", "bob/desk", "from the new endpoint")
	waitFor(t, "new and existing endpoints interoperate", func() bool { return len(c.inbox("bob")) == 3 })
}
