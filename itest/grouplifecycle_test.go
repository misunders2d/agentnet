package itest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The production capability stays unadvertised. This fixture signs grp1 only
// for its two disposable, known-current native daemon sessions, as the existing
// native group carrier tests do. It never changes either binary or user config.
func groupCLIFixtureCaps(t *testing.T, c *cli, addr, home, address, cert string, before ...[]string) []string {
	t.Helper()
	id, err := identity.Load(filepath.Join(c.dir, home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cert)) {
		t.Fatal("synthetic Hub certificate")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}, Proxy: nil}
	defer tr.CloseIdleConnections()
	h := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	do := func(method, path string, value any, out any) {
		var body []byte
		if value != nil {
			body, _ = json.Marshal(value)
		}
		req, err := http.NewRequestWithContext(context.Background(), method, "https://"+addr+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		protocol.SignRequest(req, address, id.Sign, body)
		res, err := h.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatalf("synthetic caps request status %d", res.StatusCode)
		}
		if out != nil {
			if err = json.NewDecoder(res.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		} else {
			io.Copy(io.Discard, res.Body)
		}
	}
	label, name, _ := protocol.SplitAddress(address)
	var p protocol.Profile
	waitFor(t, "synthetic native daemon session/person", func() bool {
		do("GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &p)
		if len(p.Sessions) == 0 || len(p.Person) == 0 {
			return false
		}
		if len(before) != 0 {
			fresh := false
			for _, session := range p.Sessions {
				if !slices.Contains(before[0], session) {
					fresh = true
				}
			}
			return fresh
		}
		return true
	})
	caps := []string{protocol.CapAgentIdentity, protocol.CapExternalParticipation, protocol.CapControl, protocol.CapDriveSpace, protocol.CapEnv2, protocol.CapHeadless, protocol.CapNotify, protocol.CapPerson, protocol.CapTyping, protocol.CapGroup}
	slices.Sort(caps)
	for _, session := range p.Sessions {
		rec := protocol.CapsRecord{Address: address, Session: session, Caps: caps, TS: time.Now().Unix() + 200}
		rec.Sign(id.Sign)
		do("PUT", "/v1/caps", rec, nil)
	}
	return slices.Clone(p.Sessions)
}

func TestCLIGroupLifecycle(t *testing.T) {
	c := buildCLI(t)
	addr, _ := c.setup(t, "hub")
	code := c.run("--home", "alice", "admin", "invite", "--raw", "caps-fixture")
	invite, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	c.start("alice.log", "--home", "alice", "daemon")
	c.start("bob.log", "--home", "bob", "daemon")
	c.run("--home", "alice", "person", "create", "Alice")
	c.run("--home", "bob", "person", "create", "Bob")
	groupCLIFixtureCaps(t, c, addr, "alice", "admin/laptop", invite.CertPEM)
	groupCLIFixtureCaps(t, c, addr, "bob", "bob/desk", invite.CertPEM)
	a, err := client.Open(filepath.Join(c.dir, "bob"))
	if err != nil {
		t.Fatal(err)
	}
	person, ok, err := a.Person()
	a.Close()
	if err != nil || !ok {
		t.Fatalf("synthetic Bob person %v", err)
	}
	var packet client.GroupContext
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "create", "CLI group")), &packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.State.Members) != 1 {
		t.Fatal("CLI create not alone")
	}
	var proposal client.GroupInvitationInfo
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", packet.Root.ID(), person.Person)), &proposal); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "verified CLI pending invitation", func() bool {
		out, e := c.try("--home", "bob", "group", "invitations")
		return e == nil && strings.Contains(out, proposal.ID) && strings.Contains(out, `"status":"pending"`)
	})
	b, err := client.Open(filepath.Join(c.dir, "bob"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.GroupContext(packet.Root.ID()); err == nil {
		b.Close()
		t.Fatal("CLI invite joined before human accept")
	}
	b.Close()
	c.run("--home", "bob", "group", "accept", proposal.ID)
	c.run("--home", "bob", "group", "accept", proposal.ID)
	waitFor(t, "CLI acceptance publishes without extra click", func() bool {
		out, e := c.try("--home", "alice", "group", "invitations")
		return e == nil && strings.Contains(out, `"status":"published"`)
	})
	waitFor(t, "CLI new member current group", func() bool {
		b, e := client.Open(filepath.Join(c.dir, "bob"))
		if e != nil {
			return false
		}
		defer b.Close()
		p, e := b.GroupContext(packet.Root.ID())
		return e == nil && p.State.Seq == 1
	})
	for _, home := range []string{"alice", "bob"} {
		body := "CLI ordinary " + home
		c.run("--home", home, "dm", "send", packet.Root.ID(), body)
		other := "bob"
		if home == "bob" {
			other = "alice"
		}
		waitFor(t, "CLI ordinary encrypted group message", func() bool { return strings.Contains(dmShowArriving(c, other, packet.Root.ID()), body) })
	}
	// A different group's explicit decline never installs membership.
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "create", "Declined CLI group")), &packet); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", packet.Root.ID(), person.Person)), &proposal); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second CLI proposal", func() bool { return strings.Contains(c.run("--home", "bob", "group", "invitations"), proposal.ID) })
	c.run("--home", "bob", "group", "decline", proposal.ID)
	waitFor(t, "CLI decline returns to inviter", func() bool {
		return strings.Contains(c.run("--home", "alice", "group", "invitations"), `"status":"declined"`)
	})
	b, err = client.Open(filepath.Join(c.dir, "bob"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err = b.GroupContext(packet.Root.ID()); err == nil {
		t.Fatal("declined CLI group joined")
	}
	if _, err = os.Stat(filepath.Join(c.dir, "bob", "agent.db")); err != nil {
		t.Fatal(err)
	}
}
