package itest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Real client binaries/daemons and CLI views; the local in-process Hub uses
// the deterministic Google issuer transport, never Google's live service.
func TestMEL736OneRealBinaries(t *testing.T) {
	c := buildCLI(t)
	issuer := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(c.dir, "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleHTTP: issuer.Client}, "127.0.0.1:0")
	ctx := context.Background()
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, _ := protocol.DecodeInvite(code)
	admin, e := client.Join(ctx, filepath.Join(c.dir, "peer"), code, "desk")
	if e != nil {
		t.Fatal(e)
	}
	if e = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: "bohdan@mellanni.com"}); e != nil {
		t.Fatal(e)
	}
	admin.Close()
	join := func(home, name string) {
		t.Helper()
		dir := filepath.Join(c.dir, home)
		pub, e := client.GoogleDevice(dir, name)
		if e != nil {
			t.Fatal(e)
		}
		a, e := client.JoinGoogle(ctx, dir, client.GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}, issuer.IDToken(t, pub, "bohdan@mellanni.com", nil), name)
		if e != nil {
			t.Fatal(e)
		}
		a.Close()
	}
	join("old", "lost")
	c.run("--home", "peer", "person", "create", "Colleague")
	stopPeer := c.start("peer.log", "--home", "peer", "daemon")
	stopOld := c.start("old.log", "--home", "old", "daemon")
	var conv string
	waitFor(t, "old DM", func() bool {
		out, e := c.try("--home", "peer", "dm", "new", protocol.GoogleLabel("bohdan@mellanni.com")+"/lost")
		if e == nil {
			conv = out
		}
		return e == nil
	})
	c.run("--home", "peer", "dm", "send", conv, "Before every device was lost")
	waitFor(t, "old device receives", func() bool { return strings.Contains(dmShowArriving(c, "old", conv), "Before every device was lost") })
	stopOld()
	c.run("--home", "peer", "admin", "google", "remove", "bohdan@mellanni.com")
	c.run("--home", "peer", "admin", "google", "invite", "bohdan@mellanni.com")
	join("new", "fresh")
	c.start("new.log", "--home", "new", "daemon")
	waitFor(t, "one directory person", func() bool {
		out, e := c.try("--home", "peer", "person", "list")
		return e == nil && strings.Count(out, protocol.GoogleLabel("bohdan@mellanni.com")+"/fresh") == 1 && !strings.Contains(out, "/lost")
	})
	// Member push must pin and settle the successor before using the old link.
	waitFor(t, "one continued DM and notice", func() bool {
		out, e := c.try("--home", "peer", "dm", "show", conv)
		return e == nil && strings.Count(out, "signed in again") == 1
	})
	// Sending preflight verifies the fresh device's current signed capability
	// profile. Directory continuity can settle before that profile is published.
	waitFor(t, "fresh device signed messaging profile", func() bool {
		out, err := c.try("--home", "peer", "dm", "send", conv, "After recovery through the old link")
		if err != nil && !strings.Contains(out, "peer_update:") {
			t.Fatalf("recovered DM send: %v: %s", err, out)
		}
		return err == nil
	})
	waitFor(t, "new device receives", func() bool {
		out, e := c.try("--home", "new", "dm", "list")
		if e != nil || out == "" {
			return false
		}
		id := strings.Fields(out)[0]
		return strings.Contains(dmShowArriving(c, "new", id), "After recovery through the old link")
	})
	stopPeer()
	c.start("peer-restarted.log", "--home", "peer", "daemon")
	waitFor(t, "restart catch-up stays one", func() bool {
		list, e := c.try("--home", "peer", "dm", "list")
		if e != nil || len(strings.Split(list, "\n")) != 1 {
			return false
		}
		out := dmShow(c, "peer", conv)
		return strings.Contains(out, "Before every device was lost") && strings.Contains(out, "After recovery through the old link") && strings.Count(out, "signed in again") == 1
	})
}
