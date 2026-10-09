package ui

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestP7BrowserHumanRosterParity(t *testing.T) {
	w := startWireNode(t)
	desk, _ := identity.Generate()
	dp := desk.Public("person/desk")
	host, _ := identity.Generate()
	hp := host.Public("person/host")
	r0 := protocol.PersonRoster{Person: protocol.NewID(), Label: "P7 human", Devices: []identity.Public{dp}}
	r0.Sign(desk.Sign)
	r1 := protocol.PersonRoster{Person: r0.Person, Label: r0.Label, Seq: 1, Prev: r0.Hash(), Devices: []identity.Public{dp, hp}, HumanKeys: []string{dp.Fingerprint()}, By: dp.Fingerprint()}
	r1.Join = ed25519.Sign(host.Sign, protocol.JoinBytes(r1.Person, 1, r1.Prev, hp))
	r1.Sign(desk.Sign)
	raw := func(r protocol.PersonRoster) string { b, _ := json.Marshal(r); return string(b) }
	if v := w.ok(map[string]any{"op": "parseRoster", "json": raw(r1), "prev": raw(r0)}); v["hash"] != r1.Hash() {
		t.Fatal("canonical role hash differs", v)
	}
	other, _ := identity.Generate()
	pub := other.Public("person/third")
	r2 := protocol.PersonRoster{Person: r1.Person, Label: r1.Label, Seq: 2, Prev: r1.Hash(), Devices: append(r1.Devices, pub), HumanKeys: r1.Humans(), By: hp.Fingerprint()}
	r2.Join = ed25519.Sign(other.Sign, protocol.JoinBytes(r2.Person, 2, r2.Prev, pub))
	r2.Sign(host.Sign)
	if _, e := r2.VerifyNext(r1); e == nil {
		t.Fatal("Go let host enroll")
	}
	if v := w.call(map[string]any{"op": "parseRoster", "json": raw(r2), "prev": raw(r1)}); v["error"] == nil {
		t.Fatal("browser let host enroll")
	}
	r2.By = dp.Fingerprint()
	r2.Sign(desk.Sign)
	if v := w.ok(map[string]any{"op": "parseRoster", "json": raw(r2), "prev": raw(r1)}); v["hash"] != r2.Hash() {
		t.Fatal("human enrollment parity", v)
	}
	r2.Devices = r1.Devices
	r2.Join = nil
	r2.HumanKeys = append(r1.Humans(), hp.Fingerprint())
	r2.By = hp.Fingerprint()
	r2.Sign(host.Sign)
	if _, e := r2.VerifyNext(r1); e == nil {
		t.Fatal("Go let host promote itself")
	}
	if v := w.call(map[string]any{"op": "parseRoster", "json": raw(r2), "prev": raw(r1)}); v["error"] == nil {
		t.Fatal("browser let host promote itself")
	}
}

func TestP7LivePersonGrantViews(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	alice, e := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, hub), "desk")
	if e != nil {
		t.Fatal(e)
	}
	defer alice.Close()
	code, e := alice.Invite(ctx, "bob", time.Hour, false)
	if e != nil {
		t.Fatal(e)
	}
	bob, e := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if e != nil {
		t.Fatal(e)
	}
	defer bob.Close()
	runDaemon(t, alice)
	runDaemon(t, bob)
	pa, pb := NewLive(alice), NewLive(bob)
	if _, _, e = pa.CreatePerson("Sergey"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = pb.CreatePerson("Vitalii"); e != nil {
		t.Fatal(e)
	}
	if _, e = pb.NewDM(alice.Address); e != nil {
		t.Fatal(e)
	}
	ap, _, _ := alice.Person()
	if _, e = pb.Act(Action{Do: DoApprove, ID: ap.Person}); e != nil {
		t.Fatal(e)
	}
	if _, e = pb.Act(Action{Do: "grant_tasks", ID: ap.Person}); e != nil {
		t.Fatal(e)
	}
	sent, e := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindQuestion, Body: "P7 current person"})
	if e != nil {
		t.Fatal(e)
	}
	var thread Thread
	waitFor(t, "person-aware thread", func() bool { thread, e = pb.Thread(sent.ID); return e == nil && thread.PermissionPerson != nil })
	if thread.PermissionPerson.Person != ap.Person || thread.QuestionTarget != ap.Person || thread.TaskTarget != ap.Person || !thread.Approved || thread.TaskGrant != "active" {
		t.Fatalf("thread %+v", thread)
	}
	v, e := pb.Approvals()
	if e != nil || len(v.Questions) != 1 || v.Questions[0].Person != ap.Person || len(v.Tasks) != 1 || v.Tasks[0].Person != ap.Person {
		t.Fatalf("views %+v %v", v, e)
	}
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	defer ts.Close()
	server = New(pb, strings.TrimPrefix(ts.URL, "http://"), testToken)
	// The new button uses the existing authenticated local action route.
	// Display names, absent consent, and malformed requests cannot grant it.
	me, _, err := bob.Person()
	if err != nil {
		t.Fatal(err)
	}
	grantBody := `{"do":"grant_tasks","id":"` + me.Person + `"}`
	for _, tc := range []struct {
		method, body string
		headers      map[string]string
		want         int
	}{
		{"POST", grantBody, nil, http.StatusUnauthorized},
		{"POST", grantBody, authed(ts, map[string]string{"Content-Type": "application/json"}), http.StatusForbidden},
		{"GET", "", authed(ts, nil), http.StatusMethodNotAllowed},
		{"POST", `{"do":"grant_tasks","id":"Vitalii"}`, post(ts), http.StatusConflict},
		{"POST", `{"do":"grant_tasks","id":"` + me.Person + `","all":true}`, post(ts), http.StatusBadRequest},
	} {
		if resp := do(t, ts, tc.method, "/api/act", tc.body, tc.headers); resp.StatusCode != tc.want {
			t.Fatalf("grant route %s: got %d want %d", tc.method, resp.StatusCode, tc.want)
		}
	}
	if gs, e := bob.PersonGrants(); e != nil || len(gs) != 1 || gs[0].Person != ap.Person {
		t.Fatalf("refused grant changed permissions: %v %v", gs, e)
	}
	if resp := do(t, ts, "POST", "/api/act", grantBody, post(ts)); resp.StatusCode != http.StatusOK {
		t.Fatalf("grant own person: %d", resp.StatusCode)
	}
	if _, e = pb.Act(Action{Do: DoRevokeTasks, ID: me.Person}); e != nil {
		t.Fatal(e)
	}
	body := `{"kind":"question","person":"` + ap.Person + `"}`
	for _, bad := range []string{`{"kind":"question","person":"` + ap.Person + `","address":"` + alice.Address + `"}`, `{"kind":"participation","person":"` + ap.Person + `","pid":"` + ap.Person + `"}`} {
		if resp := do(t, ts, "POST", "/api/approvals/revoke", bad, post(ts)); resp.StatusCode != http.StatusConflict {
			t.Fatalf("ambiguous revoke accepted: %d", resp.StatusCode)
		}
	}
	if resp := do(t, ts, "POST", "/api/approvals/revoke", body, post(ts)); resp.StatusCode != http.StatusOK {
		t.Fatalf("person revoke: %d", resp.StatusCode)
	}
	if _, e = pb.RevokeApproval(ApprovalRevoke{Kind: "task", Person: ap.Person}); e != nil {
		t.Fatal(e)
	}
	v, e = pb.Approvals()
	if e != nil || len(v.Questions) != 0 || len(v.Tasks) != 0 {
		t.Fatalf("revoke %+v %v", v, e)
	}
}

func TestP7BrowserHostCannotEnroll(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("node not installed")
	}
	out, e := exec.Command(node, "testdata/person_p7_engine.mjs").CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	if !strings.Contains(string(out), "P7 engine human gates PASS") {
		t.Fatal(string(out))
	}
}
