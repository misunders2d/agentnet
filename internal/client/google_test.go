package client

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestGoogleEnrollmentApprovalAndOffboarding(t *testing.T) {
	i := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleDesktopClientID: testgoogle.ClientID, GoogleDesktopClientSecret: "fixture-desktop-secret", GoogleHTTP: i.Client}, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := Join(ctx, filepath.Join(t.TempDir(), "admin"), code, "admin")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Domain: "example.com"}); err != nil {
		t.Fatal(err)
	}
	o := GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}
	home := filepath.Join(t.TempDir(), "first")
	pub, err := GoogleDevice(home, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	token := i.IDToken(t, pub, "person@example.com", nil)
	first, err := JoinGoogle(ctx, home, o, token, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	p, ok, err := first.Person()
	if err != nil || !ok || p.Email != "person@example.com" {
		t.Fatalf("person binding: %+v %v %v", p, ok, err)
	}
	if !first.PersonPublished() {
		t.Fatal("first roster not published atomically")
	}
	if err = first.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: "intruder@example.com"}); err == nil {
		t.Fatal("non-admin edited membership")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	first, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// Reopening retains the key/person, and contains no ID/access token.
	if got, _, _ := first.Person(); got.Person != p.Person || got.Email != p.Email {
		t.Fatal("binding lost after reopen")
	}
	raw, err := os.ReadFile(filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "local-unused-access") {
		t.Fatal("Google credential persisted")
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- first.Run(runCtx, RunOptions{}) }()
	defer func() { stop(); <-done }()
	secondHome := filepath.Join(t.TempDir(), "second")
	secondPub, _ := GoogleDevice(secondHome, "phone")
	second, err := JoinGoogle(ctx, secondHome, o, i.IDToken(t, secondPub, "person@example.com", nil), "phone")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.LinkState().State != LinkPending {
		t.Fatal("later device did not wait")
	}
	if _, err = second.Members(ctx); err == nil {
		t.Fatal("pending device accessed members")
	}
	if _, err = second.CreatePerson(ctx, "Another Person"); !errors.Is(err, ErrLinkWaiting) {
		t.Fatalf("pending device created another person: %v", err)
	}
	var request LinkRequest
	for {
		links, err := first.PendingLinks()
		if err != nil {
			t.Fatal(err)
		}
		if len(links) > 0 {
			request = links[0]
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("approval event did not arrive")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = first.DecideLink(ctx, request.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = second.finishLink(ctx); err != nil {
		t.Fatal(err)
	}
	secondPerson, ok, err := second.Person()
	if err != nil || !ok || secondPerson.Person != p.Person || secondPerson.Email != p.Email || len(secondPerson.Devices) != 2 {
		t.Fatalf("approved roster: %+v %v", secondPerson, err)
	}
	if _, err = first.RenamePerson(ctx, "New Name"); err != nil {
		t.Fatal(err)
	}
	if _, err = second.refreshPerson(ctx, p.Person, false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := second.Person(); got.Label != "New Name" || got.Email != p.Email {
		t.Fatal("rename lost email")
	}
	waitingHome := filepath.Join(t.TempDir(), "waiting")
	waitingPub, _ := GoogleDevice(waitingHome, "tablet")
	waiting, err := JoinGoogle(ctx, waitingHome, o, i.IDToken(t, waitingPub, p.Email, nil), "tablet")
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Close()
	if waiting.LinkState().State != LinkPending {
		t.Fatal("third device did not wait")
	}
	if err = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: p.Email, Remove: true}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{first, second, waiting} {
		if _, err = a.Members(ctx); err == nil {
			t.Fatal("offboarded device still authenticated")
		}
	}
	thirdHome := filepath.Join(t.TempDir(), "third")
	thirdPub, _ := GoogleDevice(thirdHome, "desk")
	if a, err := JoinGoogle(ctx, thirdHome, o, i.IDToken(t, thirdPub, p.Email, nil), "desk"); err == nil {
		a.Close()
		t.Fatal("domain allowance bypassed offboarding")
	}
}

func TestGoogleAdmissionAndDesktopExchange(t *testing.T) {
	i := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleDesktopClientID: testgoogle.ClientID, GoogleHTTP: i.Client}, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, _ := protocol.DecodeInvite(code)
	admin, err := Join(ctx, filepath.Join(t.TempDir(), "admin"), code, "admin")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Domain: "example.com"}); err != nil {
		t.Fatal(err)
	}
	o := GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}
	for _, tc := range []struct {
		email  string
		claims map[string]any
	}{
		{"person@example.com", map[string]any{"hd": nil}},
		{"person@example.com", map[string]any{"hd": "other.com"}},
		{"person@sub.example.com", nil},
	} {
		home := filepath.Join(t.TempDir(), "denied")
		pub, _ := GoogleDevice(home, "desk")
		if a, e := JoinGoogle(ctx, home, o, i.IDToken(t, pub, tc.email, tc.claims), "desk"); e == nil {
			a.Close()
			t.Fatal("domain authority or exact matching bypassed")
		}
	}
	if err = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: "external@example.net"}); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "external")
	pub, _ := GoogleDevice(home, "desk")
	token := i.IDToken(t, pub, "external@example.net", map[string]any{"hd": nil})
	if b, e := JoinGoogle(ctx, home, o, token, "desk"); e == nil {
		b.Close()
		t.Fatal("third-party historical email verification admitted")
	}
	token = i.IDToken(t, pub, "external@example.net", nil)
	a, err := JoinGoogle(ctx, home, o, token, "desk")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// A different Google subject cannot inherit a recycled email's person.
	other := filepath.Join(t.TempDir(), "other")
	otherPub, _ := GoogleDevice(other, "phone")
	if b, e := JoinGoogle(ctx, other, o, i.IDToken(t, otherPub, "external@example.net", map[string]any{"sub": "replacement-account"}), "phone"); e == nil {
		b.Close()
		t.Fatal("subject changed silently")
	}
	// A normal invited key cannot publish an email assertion.
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Forged", Email: "victim@example.com", Devices: []identity.Public{admin.Self()}}
	r.Sign(admin.id.Sign)
	if err = admin.hub.do(ctx, "PUT", "/v1/person", r, nil); err == nil {
		t.Fatal("invite-code device forged verified email")
	}
	// Exact-key replay after a lost response is idempotent.
	row, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		t.Fatal(err)
	}
	req := protocol.GoogleRequest{IDToken: token, Public: a.Self(), First: &row.roster}
	req.Sign(a.id.Sign)
	conn, err := newHubConn(o.Hub, o.CertPEM, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.release()
	if err = conn.do(ctx, "POST", "/v1/google/join", req, nil); err != nil {
		t.Fatal(err)
	}
	req.IDToken = "substituted"
	if err = conn.do(ctx, "POST", "/v1/google/prepare", req, nil); err == nil {
		t.Fatal("unsigned request field changed")
	}
	i.Token = token
	exchanged := false
	i.OnExchange = func(r *http.Request) {
		r.ParseForm()
		exchanged = true
		if r.Form.Get("code_verifier") != strings.Repeat("v", 43) || r.Form.Get("redirect_uri") != "http://127.0.0.1:32123/oauth/google" || r.Form.Get("code") != "fixture-code" {
			t.Error("PKCE exchange changed")
		}
	}
	e := protocol.GoogleExchange{Code: "fixture-code", Verifier: strings.Repeat("v", 43), Redirect: "http://127.0.0.1:32123/oauth/google", Public: pub}
	if got, err := GoogleExchangeCode(ctx, home, o, e); err != nil || got != token || !exchanged {
		t.Fatalf("local exchange: %v", err)
	}
	exchanged = false
	e.Redirect = "https://attacker.example/oauth/google"
	if _, err = GoogleExchangeCode(ctx, home, o, e); err == nil || exchanged {
		t.Fatal("redirect allowlist bypassed")
	}
	e.Redirect = "http://127.0.0.1:32123/oauth/google"
	e.Public = otherPub
	// Even a properly signed exchange cannot use a token with another key's nonce.
	id, _ := identity.Load(filepath.Join(other, "identity.json"))
	e.Sign(id.Sign)
	if err = conn.do(ctx, "POST", "/v1/google/exchange", e, nil); err == nil {
		t.Fatal("exchange accepted stolen ID token")
	}
}
