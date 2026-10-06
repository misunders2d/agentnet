package client

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
)

type googleReviewWorld struct {
	ctx    context.Context
	issuer *testgoogle.Issuer
	admin  *Agent
	opts   GoogleOptions
}

func newGoogleReviewWorld(t *testing.T) *googleReviewWorld {
	t.Helper()
	i := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleHTTP: i.Client}, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, _ := protocol.DecodeInvite(code)
	admin, err := Join(ctx, t.TempDir(), code, "admin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	if err = admin.ChangeGoogleAccess(ctx, protocol.GoogleAccessChange{Domain: "example.com"}); err != nil {
		t.Fatal(err)
	}
	return &googleReviewWorld{ctx, i, admin, GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}}
}
func (w *googleReviewWorld) join(t *testing.T, name string) *Agent {
	t.Helper()
	home := t.TempDir()
	pub, err := GoogleDevice(home, name)
	if err != nil {
		t.Fatal(err)
	}
	a, err := JoinGoogle(w.ctx, home, w.opts, w.issuer.IDToken(t, pub, "person@example.com", nil), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}
func TestGoogleReviewAnyDeviceRemovalAndReinvite(t *testing.T) {
	w := newGoogleReviewWorld(t)
	first := w.join(t, "laptop")
	stopFirst := runAgent(t, first)
	old, _, _ := first.Person()
	second := w.join(t, "phone")
	req := pendingLink(t, first)
	if err := first.DecideLink(w.ctx, req.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := second.finishLink(w.ctx); err != nil {
		t.Fatal(err)
	}
	stopFirst()
	runAgent(t, second)
	third := w.join(t, "tablet")
	request := pendingLink(t, second)
	if err := second.DecideLink(w.ctx, request.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := third.finishLink(w.ctx); err != nil {
		t.Fatal("approval by second device rejected:", err)
	}

	runAgent(t, third)
	fourth := w.join(t, "refused")
	refusal := pendingLink(t, third)
	if err := third.DecideLink(w.ctx, refusal.ID, false); err != nil {
		t.Fatal(err)
	}
	var refused *HubError
	if _, err := fourth.Members(w.ctx); !errors.As(err, &refused) || refused.Code != protocol.CodeLinkRefused {
		t.Fatal("non-selected device could not refuse pending Google device", err)
	}
	// The relay-selected laptop did not approve the third device.
	if err := second.RemoveDevice(w.ctx, first.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Members(w.ctx); err == nil {
		t.Fatal("removed first Google device still authenticates")
	}
	access, err := w.admin.GoogleAccess(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range access.Emails {
		if e.Email == old.Email && e.DomainMember {
			found = true
		}
	}
	if !found {
		t.Fatal("domain-admitted person missing from admin list")
	}
	if err = w.admin.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email, Remove: true}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{first, second, third} {
		if _, err = a.Members(w.ctx); err == nil {
			t.Fatal("offboarded device authenticated")
		}
	}
	home := t.TempDir()
	pub, _ := GoogleDevice(home, "new")
	token := w.issuer.IDToken(t, pub, old.Email, nil)
	if a, e := JoinGoogle(w.ctx, home, w.opts, token, "new"); e == nil {
		a.Close()
		t.Fatal("removed email admitted through domain")
	}
	if err = w.admin.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: old.Email}); err != nil {
		t.Fatal(err)
	}
	fresh, err := JoinGoogle(w.ctx, home, w.opts, token, "new")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	p, ok, err := fresh.Person()
	if err != nil || !ok || p.Person == old.Person || !fresh.PersonPublished() {
		t.Fatalf("fresh first device: %+v %v", p, err)
	}
}
func TestGoogleReviewInvitePreservesAdmin(t *testing.T) {
	w := newGoogleReviewWorld(t)
	yes := true
	if err := w.admin.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: "person@example.com", Admin: &yes}); err != nil {
		t.Fatal(err)
	}
	a := w.join(t, "laptop")
	for _, role := range []*bool{nil, new(bool)} {
		if err := w.admin.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: "person@example.com", Admin: role}); err != nil {
			t.Fatal(err)
		}
		v, err := a.GoogleAccess(w.ctx)
		if err != nil || !v.CanAdmin {
			t.Fatalf("invite demoted admin: %+v %v", v, err)
		}
		if err = a.ChangeGoogleAccess(w.ctx, protocol.GoogleAccessChange{Email: "another@example.com"}); err != nil {
			t.Fatal("requireAdmin failed:", err)
		}
	}
}
func saveGoogleReviewIntent(t *testing.T, home string, req protocol.GoogleRequest) {
	t.Helper()
	st, err := openStore(filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	b, _ := json.Marshal(req)
	if err = st.setConfig(map[string]string{"google_intent": string(b)}); err != nil {
		t.Fatal(err)
	}
}
func TestGoogleReviewIntentRetry(t *testing.T) {
	for _, scenario := range []string{"first-race", "expired-link", "device-limit"} {
		t.Run(scenario, func(t *testing.T) {
			w := newGoogleReviewWorld(t)
			first := w.join(t, "laptop")
			runAgent(t, first)
			me, _, _ := first.store.selfPerson(first.Address)
			home := t.TempDir()
			pub, _ := GoogleDevice(home, "retry")
			id, _ := identity.Load(filepath.Join(home, "identity.json"))
			pub = id.Public(protocol.GoogleLabel(me.roster.Email) + "/retry")
			token := w.issuer.IDToken(t, pub, me.roster.Email, nil)
			if scenario == "first-race" {
				r := protocol.PersonRoster{Person: protocol.NewID(), Label: "Race", Email: me.roster.Email, Devices: []identity.Public{pub}}
				r.Sign(id.Sign)
				saveGoogleReviewIntent(t, home, protocol.GoogleRequest{Public: pub, First: &r})
				if a, err := JoinGoogle(w.ctx, home, w.opts, token, "retry"); err == nil {
					a.Close()
					t.Fatal("stale first unexpectedly joined")
				}
			} else if scenario == "expired-link" {
				l := protocol.GoogleLink{Email: me.roster.Email, Person: me.info.Person, Seq: me.info.Seq, Roster: me.info.Roster, Approver: protocol.LinkApprover{Address: first.Address, Fingerprint: first.Self().Fingerprint()}, Expires: time.Now().Unix() - 1, Offer: protocol.NewID(), Join: ed25519.Sign(id.Sign, protocol.JoinBytes(me.info.Person, me.info.Seq+1, me.info.Roster, pub))}
				saveGoogleReviewIntent(t, home, protocol.GoogleRequest{Public: pub, Link: &l})
			} else {
				for n := 1; n < protocol.MaxPersonDevices; n++ {
					w.join(t, fmt.Sprintf("waiting-%d", n))
				}
				if a, err := JoinGoogle(w.ctx, home, w.opts, token, "retry"); err == nil {
					a.Close()
					t.Fatal("full roster accepted device")
				}
				request := pendingLink(t, first)
				if err := first.DecideLink(w.ctx, request.ID, false); err != nil {
					t.Fatal(err)
				}
				if _, err := first.RenamePerson(w.ctx, "Changed while retrying"); err != nil {
					t.Fatal(err)
				}
			}
			a, err := JoinGoogle(w.ctx, home, w.opts, token, "retry")
			if err != nil {
				t.Fatal("fresh consent retry:", err)
			}
			defer a.Close()
			if a.LinkState().State != LinkPending {
				t.Fatal("retry did not wait for approval")
			}
		})
	}
}
func TestGoogleReviewEmailConflict(t *testing.T) {
	for _, own := range []bool{false, true} {
		t.Run(fmt.Sprint(own), func(t *testing.T) {
			st, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.db.Close()
			first, _ := identity.Generate()
			second, _ := identity.Generate()
			me := first.Public("person/laptop")
			r := protocol.PersonRoster{Person: protocol.NewID(), Label: "First", Email: "person@example.com", Devices: []identity.Public{me}}
			r.Sign(first.Sign)
			raw, _ := json.Marshal(r)
			if !own {
				other, _ := identity.Generate()
				me = other.Public("other/laptop")
			}
			if _, err = st.pinChain(r.Person, [][]byte{raw}, me, own); err != nil {
				t.Fatal(err)
			}
			spoof := protocol.PersonRoster{Person: protocol.NewID(), Label: "Spoof", Email: r.Email, Devices: []identity.Public{second.Public("spoof/laptop")}}
			spoof.Sign(second.Sign)
			raw, _ = json.Marshal(spoof)
			if _, err = st.pinChain(spoof.Person, [][]byte{raw}, me, false); !errors.Is(err, errPersonConflict) {
				t.Fatal("duplicate email pinned:", err)
			}
			pinned, _, _ := st.personByID(r.Person)
			bad, _, _ := st.personByID(spoof.Person)
			if pinned.info.State == personConflict || bad.info.State != personConflict || bad.info.Email != "" {
				t.Fatalf("email pins: %+v %+v", pinned.info, bad.info)
			}
			if _, err = st.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, r.Person); err != nil {
				t.Fatal(err)
			}
			key, _ := identity.Generate()
			third := protocol.PersonRoster{Person: protocol.NewID(), Label: "Third", Email: r.Email, Devices: []identity.Public{key.Public("third/laptop")}}
			third.Sign(key.Sign)
			raw, _ = json.Marshal(third)
			if _, err = st.pinChain(third.Person, [][]byte{raw}, me, false); !errors.Is(err, errPersonConflict) {
				t.Fatal("fork warning released original email pin:", err)
			}

		})
	}
}

func TestGoogleReviewLostJoinResponseAfterApproval(t *testing.T) {
	w := newGoogleReviewWorld(t)
	first := w.join(t, "laptop")
	runAgent(t, first)
	me, _, _ := first.store.selfPerson(first.Address)
	home := t.TempDir()
	pub, _ := GoogleDevice(home, "retry")
	id, _ := identity.Load(filepath.Join(home, "identity.json"))
	pub = id.Public(protocol.GoogleLabel(me.roster.Email) + "/retry")
	token := w.issuer.IDToken(t, pub, me.roster.Email, nil)
	l := protocol.GoogleLink{Email: me.roster.Email, Person: me.info.Person, Seq: me.info.Seq, Roster: me.info.Roster, Approver: protocol.LinkApprover{Address: first.Address, Fingerprint: first.Self().Fingerprint()}, Expires: time.Now().Unix() + 300, Offer: protocol.NewID(), Join: ed25519.Sign(id.Sign, protocol.JoinBytes(me.info.Person, me.info.Seq+1, me.info.Roster, pub))}
	req := protocol.GoogleRequest{IDToken: token, Public: pub, Link: &l}
	req.Sign(id.Sign)
	conn, err := newHubConn(w.opts.Hub, w.opts.CertPEM, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.release()
	if err = conn.do(w.ctx, "POST", "/v1/google/join", req, nil); err != nil {
		t.Fatal(err)
	}
	request := pendingLink(t, first)
	if err = first.DecideLink(w.ctx, request.ID, true); err != nil {
		t.Fatal(err)
	}
	// Model a late retry, after the original consent's expiry. The key has
	// already landed and was approved; its original target sequence must survive.
	l.Expires = time.Now().Unix() - 1
	req.IDToken = ""
	req.Sig = nil
	saveGoogleReviewIntent(t, home, req)
	a, err := JoinGoogle(w.ctx, home, w.opts, token, "retry")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.LinkState().Seq != me.info.Seq+1 {
		t.Fatal("ambiguous join consent rebuilt against later head")
	}
	if _, err = a.AwaitLink(w.ctx); err != nil {
		t.Fatal("approved late retry:", err)
	}
}

func TestGoogleReviewPreGooglePersonStaysSeparate(t *testing.T) {
	w := newGoogleReviewWorld(t)
	old, err := w.admin.CreatePerson(w.ctx, "Pre-Google Person")
	if err != nil {
		t.Fatal(err)
	}
	fresh := w.join(t, "google-device")
	p, ok, err := fresh.Person()
	if err != nil || !ok || p.Person == old.Person || len(p.Devices) != 1 || p.Email == "" {
		t.Fatalf("fresh Google enrollment: %+v %v", p, err)
	}
	prior, _, err := w.admin.Person()
	if err != nil || prior.Person != old.Person || prior.Email != "" {
		t.Fatal("Google retrofitted existing person", prior, err)
	}
}
