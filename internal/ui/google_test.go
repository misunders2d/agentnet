package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestGoogleBrowserWire(t *testing.T) {
	w := startWireNode(t)
	emails := []string{"person@example.com", "name+alias@team.example.com", "UPPER@example.com", "a@local", "a@.example.com", "a@-example.com", "a..b@example.com", ".a@example.com", "a.@example.com", "a@example..com", "a@example.com "}
	valid := w.ok(map[string]any{"op": "validEmail", "emails": emails})["valid"].([]any)
	for n, email := range emails {
		normal, err := protocol.NormalizeEmail(email)
		if valid[n] != (err == nil && normal == email) {
			t.Fatalf("Go/browser email validation differs for %q", email)
		}
	}
	setup := w.ok(map[string]any{"op": "setup", "address": protocol.GoogleLabel("person@example.com") + "/browser"})
	var pub identity.Public
	if err := json.Unmarshal([]byte(setup["public"].(string)), &pub); err != nil {
		t.Fatal(err)
	}
	raw := w.ok(map[string]any{"op": "roster", "label": "Person Name", "email": "person@example.com"})["json"].(string)
	r, err := protocol.ParsePersonRoster([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.VerifyFirst(); err != nil {
		t.Fatal(err)
	}
	if r.Email != "person@example.com" {
		t.Fatal("missing signed email")
	}
	v := w.ok(map[string]any{"op": "google", "token": "mock-token", "first": raw})
	if v["nonce"] != protocol.GoogleNonce(pub) || int(v["ttl"].(float64)) != protocol.MaxLinkTTL {
		t.Fatal("Go/browser login constants differ")
	}
	var req protocol.GoogleRequest
	if err = json.Unmarshal([]byte(v["body"].(string)), &req); err != nil {
		t.Fatal(err)
	}
	if err = req.Verify(); err != nil {
		t.Fatal(err)
	}
	req.IDToken = "changed"
	if req.Verify() == nil {
		t.Fatal("token substitution was unsigned")
	}
	next := w.ok(map[string]any{"op": "nextRoster", "prev": raw, "devices": []string{setup["public"].(string)}})["json"].(string)
	n, err := protocol.ParsePersonRoster([]byte(next))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = n.VerifyNext(r); err != nil || n.Email != r.Email {
		t.Fatalf("next email: %v", err)
	}
}

// Both engines use the real signed Hub API; Google's fixed endpoints alone
// are replaced by the local RSA issuer. No real Google request is possible.
func TestGoogleBrowserEnrollmentAndDeviceApproval(t *testing.T) {
	i := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleHTTP: i.Client}, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, _ := protocol.DecodeInvite(code)
	admin, err := client.Join(ctx, filepath.Join(t.TempDir(), "admin"), code, "admin")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Email: "person@example.com"}); err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, relay.Dir)
	w.ok(map[string]any{"op": "init", "base": inv.Hub})
	v := w.ok(map[string]any{"op": "googleKeys", "name": "browser"})
	var pub identity.Public
	if err = json.Unmarshal([]byte(v["public"].(string)), &pub); err != nil {
		t.Fatal(err)
	}
	if v["nonce"] != protocol.GoogleNonce(pub) {
		t.Fatal("wrong browser nonce")
	}
	w.ok(map[string]any{"op": "googleJoin", "name": "browser", "token": i.IDToken(t, pub, "person@example.com", nil)})
	w.ok(map[string]any{"op": "start"})
	view := w.api("/api/overview", nil)
	person := view["person"].(map[string]any)
	if person["email"] != "person@example.com" {
		t.Fatal("browser view lost signed email")
	}
	if w.api("/api/invites", nil)["can_invite"] != false {
		t.Fatal("browser showed code invitations with Google enabled")
	}
	home := filepath.Join(t.TempDir(), "phone")
	p, _ := client.GoogleDevice(home, "phone")
	a, err := client.JoinGoogle(ctx, home, client.GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}, i.IDToken(t, p, "person@example.com", nil), "phone")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.Members(ctx); err == nil {
		t.Fatal("later device admitted before approval")
	}
	var id string
	w.until("Google consent on browser", func() bool {
		links, _ := w.api("/api/overview", nil)["links"].([]any)
		if len(links) == 0 {
			return false
		}
		id = links[0].(map[string]any)["id"].(string)
		return true
	})
	w.api("/api/device/decide", map[string]any{"id": id, "accept": true})
	if _, err = a.AwaitLink(ctx); err != nil {
		t.Fatal(err)
	}
	got, ok, err := a.Person()
	if err != nil || !ok || got.Person != person["person"] || got.Email != "person@example.com" || len(got.Devices) != 2 {
		t.Fatalf("approved browser roster: %+v %v", got, err)
	}
	// A browser can also join later, but must wait for the same normal tap.
	other := startEngineNode(t, relay.Dir)
	other.ok(map[string]any{"op": "init", "base": inv.Hub})
	keys := other.ok(map[string]any{"op": "googleKeys", "name": "tablet"})
	if err = json.Unmarshal([]byte(keys["public"].(string)), &pub); err != nil {
		t.Fatal(err)
	}
	other.ok(map[string]any{"op": "googleJoin", "name": "tablet", "token": i.IDToken(t, pub, "person@example.com", nil)})
	other.ok(map[string]any{"op": "start"})
	if other.ok(map[string]any{"op": "status"})["link"] != "pending" {
		t.Fatal("later browser bypassed approval")
	}
	w.until("second browser consent", func() bool {
		links, _ := w.api("/api/overview", nil)["links"].([]any)
		for _, value := range links {
			link := value.(map[string]any)
			if link["state"] == "pending" {
				id = link["id"].(string)
				return true
			}
		}
		return false
	})
	w.api("/api/device/decide", map[string]any{"id": id, "accept": true})
	other.until("approved browser", func() bool { return other.ok(map[string]any{"op": "status"})["link"] == "linked" })
	if other.api("/api/overview", nil)["person"].(map[string]any)["email"] != "person@example.com" {
		t.Fatal("later browser lost email")
	}
	fourthHome := t.TempDir()
	fourthPub, _ := client.GoogleDevice(fourthHome, "fourth")
	fourth, err := client.JoinGoogle(ctx, fourthHome, client.GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}, i.IDToken(t, fourthPub, "person@example.com", nil), "fourth")
	if err != nil {
		t.Fatal(err)
	}
	defer fourth.Close()
	other.until("Google request on non-selected browser", func() bool {
		links, _ := other.api("/api/overview", nil)["links"].([]any)
		for _, value := range links {
			link := value.(map[string]any)
			if link["state"] == "pending" {
				id = link["id"].(string)
				return true
			}
		}
		return false
	})
	other.api("/api/device/decide", map[string]any{"id": id, "accept": true})
	if _, err = fourth.AwaitLink(ctx); err != nil {
		t.Fatal("approval by non-selected browser:", err)
	}

}

func TestGoogleReviewBrowserRetryAndEmailConflict(t *testing.T) {
	w := startEngineNode(t, t.TempDir())
	if w.ok(map[string]any{"op": "googleReviewProbes"})["passed"] != true {
		t.Fatal("browser Google review probes failed")
	}
}
