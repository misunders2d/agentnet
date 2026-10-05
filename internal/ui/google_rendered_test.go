package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
)

type googleSetupFixture struct {
	mu     sync.Mutex
	status SetupGoogleStatus
	done   chan struct{}
}

func (*googleSetupFixture) SetupState() SetupView {
	return SetupView{State: "none", Device: "linux-laptop"}
}
func (*googleSetupFixture) SetupInspect(string) SetupInvite { return SetupInvite{} }
func (*googleSetupFixture) SetupJoin(SetupJoin) (SetupResult, error) {
	return SetupResult{}, Refuse("fixture")
}
func (*googleSetupFixture) SetupStartAgain() (SetupView, error) { return SetupView{State: "none"}, nil }
func (g *googleSetupFixture) SetupGoogle(hub string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if hub == "https://not-set.example" {
		return Refuse("Google sign-in is not set up for this workspace yet. Use your invitation instead.")
	}
	if hub != "https://workspace.example" && hub != "https://cancel.example" {
		return Refuse("workspace address")
	}
	g.done = make(chan struct{})
	g.status = SetupGoogleStatus{State: "waiting"}
	if hub == "https://workspace.example" {
		g.status = SetupGoogleStatus{State: "error", Problem: "Fixture sign-in declined. Try again."}
		close(g.done)
	}
	return nil
}
func (g *googleSetupFixture) SetupGoogleState() (SetupGoogleStatus, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status, g.done
}
func (g *googleSetupFixture) SetupGoogleCancel() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status.State == "waiting" {
		g.status = SetupGoogleStatus{State: "error", Problem: "Google sign-in was cancelled. Try again."}
		close(g.done)
	}
	return nil
}

func TestGoogleRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: local Chromium, mocked GIS only")
	}
	i := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), Web: true, GoogleWebClientID: testgoogle.ClientID, GoogleDesktopClientID: testgoogle.ClientID, GoogleHTTP: i.Client}, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, _ := protocol.DecodeInvite(code)
	bootstrap, err := client.Join(ctx, filepath.Join(t.TempDir(), "bootstrap"), code, "admin")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	for _, c := range []protocol.GoogleAccessChange{{Email: "operator@example.com", Admin: boolPointer(true)}, {Email: "browser@example.com"}, {Email: "alex@example.com"}} {
		if err = bootstrap.ChangeGoogleAccess(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	if err = bootstrap.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Domain: "example.com"}); err != nil {
		t.Fatal(err)
	}
	var domainMembers []*client.Agent
	for _, email := range []string{"domain.wide@example.com", "domain.phone@example.com"} {
		home := t.TempDir()
		pub, _ := client.GoogleDevice(home, "domain-device")
		member, e := client.JoinGoogle(ctx, home, client.GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}, i.IDToken(t, pub, email, nil), "domain-device")
		if e != nil {
			t.Fatal(e)
		}
		domainMembers = append(domainMembers, member)
		defer member.Close()
	}
	home := filepath.Join(t.TempDir(), "operator")
	pub, _ := client.GoogleDevice(home, "laptop")
	a, err := client.JoinGoogle(ctx, home, client.GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}, i.IDToken(t, pub, "operator@example.com", nil), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	peerHome := t.TempDir()
	peerPub, _ := client.GoogleDevice(peerHome, "phone")
	peer, err := client.JoinGoogle(ctx, peerHome, client.GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}, i.IDToken(t, peerPub, "alex@example.com", map[string]any{"name": "Alex"}), "phone")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	defer runDaemon(t, a)()
	defer runDaemon(t, peer)()
	waitFor(t, "peer capability publication", func() bool { _, err = a.CreateDM(ctx, peer.Address); return err == nil })
	offer, err := a.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live := NewLive(a)
	if v, err := live.Invites(); err != nil || v.CanInvite {
		t.Fatalf("native code-invite UI: %+v %v", v, err)
	}
	if _, err := live.Invite(InviteRequest{Name: "Old code", Days: 1}); err == nil {
		t.Fatal("native generated a code invite instead of email")
	}
	native := httptest.NewUnstartedServer(nil)
	native.Config.Handler = New(live, native.Listener.Addr().String(), testToken).Handler()
	native.Start()
	defer native.Close()
	setup := httptest.NewUnstartedServer(nil)
	setup.Config.Handler = NewSetup(&googleSetupFixture{}, setup.Listener.Addr().String(), testToken)
	setup.Start()
	defer setup.Close()
	// Only this disposable fixture emits credentials to the test browser.
	// Production contains no token endpoint or configurable issuer.
	signer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(i.IDToken(t, identity.Public{}, "browser@example.com", map[string]any{"nonce": r.URL.Query().Get("nonce")})))
	}))
	defer signer.Close()
	out := browserCheck(t, "testdata/google_browser_check.cjs", native.URL, inv.Hub, setup.URL, signer.URL, testToken, offer.Code, code)
	if !strings.Contains(out, "Google UI PASS") {
		t.Fatalf("%s", out)
	}
	t.Log(out)
	access, err := a.GoogleAccess(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var invited, domain bool
	for _, e := range access.Emails {
		if e.Email == "new.person@example.com" && !e.Denied {
			invited = true
		}
	}
	for _, d := range access.Domains {
		if d == "team.example.com" {
			domain = true
		}
	}

	for _, member := range domainMembers {
		if _, e := member.Members(ctx); e == nil {
			t.Fatal("rendered domain offboarding left device authenticated")
		}
	}
	if !invited || !domain {
		t.Fatal("rendered controls did not update real Hub policy")
	}
}

func boolPointer(v bool) *bool { return &v }
