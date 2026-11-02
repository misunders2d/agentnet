package client

import (
	"context"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMEL736RequesterAndAdminAPI(t *testing.T) {
	issuer := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleDesktopClientID: testgoogle.ClientID, GoogleHTTP: issuer.Client}, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, e := protocol.DecodeInvite(code)
	if e != nil {
		t.Fatal(e)
	}
	admin, e := Join(ctx, filepath.Join(t.TempDir(), "admin"), code, "admin")
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	email := "bohdan@example.com"
	if e = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: email}); e != nil {
		t.Fatal(e)
	}
	o := GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}
	enroll := func(name string) (*Agent, string) {
		home := filepath.Join(t.TempDir(), name)
		pub, e := GoogleDevice(home, name)
		if e != nil {
			t.Fatal(e)
		}
		a, e := JoinGoogle(ctx, home, o, issuer.IDToken(t, pub, email, nil), name)
		if e != nil {
			t.Fatal(e)
		}
		return a, home
	}
	// The lost device has no surviving local database. Enroll its signed
	// genesis directly, retaining only public Hub evidence for the recovery.
	oldKey, _ := identity.Generate()
	oldPub := oldKey.Public("google/old")
	conn, e := newHubConn(o.Hub, o.CertPEM, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.release()
	oldReq := protocol.GoogleRequest{Public: oldPub, IDToken: issuer.IDToken(t, oldPub, email, nil)}
	oldReq.Sign(oldKey.Sign)
	var prepared protocol.GooglePrepared
	if e = conn.do(ctx, "POST", "/v1/google/prepare", oldReq, &prepared); e != nil {
		t.Fatal(e)
	}
	oldPub = oldKey.Public(prepared.Address)
	oldPerson := protocol.PersonRoster{Person: protocol.NewID(), Label: "Bohdan", Email: email, Devices: []identity.Public{oldPub}}
	oldPerson.Sign(oldKey.Sign)
	oldReq.Public = oldPub
	oldReq.First = &oldPerson
	oldReq.Sign(oldKey.Sign)
	if e = conn.do(ctx, "POST", "/v1/google/join", oldReq, nil); e != nil {
		t.Fatal(e)
	}
	if e = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: email, Remove: true}); e != nil {
		t.Fatal(e)
	}
	if e = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: email}); e != nil {
		t.Fatal(e)
	}
	next, home := enroll("new")
	defer next.Close()
	token := issuer.IDToken(t, next.Self(), email, nil)
	if _, e = next.RequestGoogleRecovery(ctx, "", token); e == nil {
		t.Fatal("implicit old accepted")
	}
	r, e := next.RequestGoogleRecovery(ctx, oldPerson.Person, token)
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "pending" {
		t.Fatal(r)
	}
	again, e := next.RequestGoogleRecovery(ctx, oldPerson.Person, token)
	if e != nil || again.Successor.Hash() != r.Successor.Hash() {
		t.Fatal("retry changed", again, e)
	}
	list, e := admin.GoogleRecoveries(ctx)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	r, e = admin.GoogleRecoveryDecision(ctx, r.Successor.Hash(), true)
	if e != nil || r.Status != "committed" {
		t.Fatal(r, e)
	}
	raw, e := os.ReadFile(filepath.Join(home, "agent.db"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("persisted OAuth credential")
	}
}
