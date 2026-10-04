package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// The page lists this device's standing grants, the same as agentnet
// approvals (questions answered automatically, tasks run without asking,
// and its agents' grants in conversations), and revokes one exact grant
// through the operation the command line uses; requests are decoded
// strictly and guarded like every other change. Nothing listed or clicked
// grants anything.
func TestLiveApprovalsListAndRevoke(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	pa, pb := NewLive(alice), NewLive(bob)
	pa.CreatePerson("Alice")
	pb.CreatePerson("Bob")
	var conv string
	waitFor(t, "a DM", func() bool { conv, err = pa.NewDM(bob.Address); return err == nil })
	if _, err := pa.SendDM(DMDraft{Conv: conv, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bob holds the DM", goHas(bob, conv, "hi", nil))

	// Bob's page, through its HTTP API.
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(pb, strings.TrimPrefix(ts.URL, "http://"), testToken)
	list := func() ApprovalsView {
		t.Helper()
		resp := do(t, ts, "GET", "/api/approvals", "", authed(ts, nil))
		var v ApprovalsView
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&v) != nil {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("GET /api/approvals: %d %s", resp.StatusCode, body)
		}
		return v
	}
	revoke := func(body string, want int) {
		t.Helper()
		if resp := do(t, ts, "POST", "/api/approvals/revoke", body, post(ts)); resp.StatusCode != want {
			got, _ := io.ReadAll(resp.Body)
			t.Fatalf("POST /api/approvals/revoke %s: %d %s, want %d", body, resp.StatusCode, got, want)
		}
	}
	if v := list(); len(v.Questions)+len(v.Tasks)+len(v.Participations) != 0 || v.ReadOnly {
		t.Fatalf("no grants yet: %+v", v)
	}

	// Bob approves Alice's questions, grants her tasks, and accepts his
	// agent into the DM with Alice's key allowed to give it tasks.
	if err := bob.Approve(alice.Address); err != nil {
		t.Fatal(err)
	}
	fp, err := bob.GrantTasks(alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := pa.InviteAgent(AgentInvite{Conv: conv, Host: bob.Address, TasksFrom: []string{alice.Self().Fingerprint()}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the invitation at bob", func() bool { p, e := bob.Participation(inv.PID); return e == nil && p.State == client.PartInvited })
	if _, err := pb.DecideAgent(inv.PID, true); err != nil {
		t.Fatal(err)
	}
	v := list()
	if len(v.Questions) != 1 || v.Questions[0].Address != alice.Address {
		t.Fatalf("questions: %+v", v.Questions)
	}
	if len(v.Tasks) != 1 || v.Tasks[0] != (TaskGrantView{Address: alice.Address, Fingerprint: fp, Status: "active"}) {
		t.Fatalf("tasks: %+v", v.Tasks)
	}
	if len(v.Participations) != 1 {
		t.Fatalf("participations: %+v", v.Participations)
	}
	if g := v.Participations[0]; g.Conv != conv || g.PID != inv.PID || g.External || len(g.Keys) != 1 || g.Keys[0] != alice.Self().Fingerprint() ||
		len(g.TasksFrom) != 1 || g.TasksFrom[0].Label != "Alice" {
		t.Fatalf("the agent's grant: %+v", g)
	}
	// Alice's page lists none of Bob's grants.
	if va, err := pa.Approvals(); err != nil || len(va.Questions)+len(va.Tasks)+len(va.Participations) != 0 {
		t.Fatalf("alice's grants: %+v %v", va, err)
	}

	// Refused: unknown fields, a wrong kind, a mixed name, a grant not
	// held, and a change from another site.
	revoke(`{"kind":"question","address":"`+alice.Address+`","all":true}`, http.StatusBadRequest)
	revoke(`{"kind":"everything"}`, http.StatusConflict)
	revoke(`{"kind":"question","address":"`+alice.Address+`","pid":"`+inv.PID+`"}`, http.StatusConflict)
	revoke(`{"kind":"question","address":"carol/box"}`, http.StatusNotFound)
	if resp := do(t, ts, "POST", "/api/approvals/revoke", `{"kind":"question","address":"`+alice.Address+`"}`, authed(ts, map[string]string{"Content-Type": "application/json"})); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a revoke without its origin: %d", resp.StatusCode)
	}
	if v := list(); len(v.Questions) != 1 || len(v.Tasks) != 1 || len(v.Participations) != 1 {
		t.Fatalf("a refused revoke changed grants: %+v", v)
	}

	// Each grant ends by its own operation.
	revoke(`{"kind":"question","address":"`+alice.Address+`"}`, http.StatusOK)
	if approved, _ := bob.QuestionApprovals(); len(approved) != 0 {
		t.Fatalf("questions still approved: %v", approved)
	}
	revoke(`{"kind":"task","address":"`+alice.Address+`"}`, http.StatusOK)
	if grants, _ := bob.TaskGrants(); len(grants) != 0 {
		t.Fatalf("tasks still granted: %+v", grants)
	}
	revoke(`{"kind":"participation","pid":"`+inv.PID+`"}`, http.StatusOK)
	if p, err := bob.Participation(inv.PID); err != nil || p.State != client.PartDismissed {
		t.Fatalf("the agent's grant still holds: %+v %v", p, err)
	}
	if v := list(); len(v.Questions)+len(v.Tasks)+len(v.Participations) != 0 {
		t.Fatalf("after revoking: %+v", v)
	}
}

// A conversation whose agents cannot be resolved here now (a group whose
// context is pending) blocks no other grant: the page still lists the rest,
// names that conversation as unresolved, and revokes a question approval.
func TestLiveApprovalsWithAPendingGroup(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	home := filepath.Join(t.TempDir(), "alice")
	alice, err := client.Join(ctx, home, testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	if _, err := alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	packet, err := alice.CreateGroup(ctx, "Stuck")
	if err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	if err := alice.Approve("bob/desk"); err != nil {
		t.Fatal(err)
	}
	// The group has a participation, and its context goes pending: a
	// member's withdrawal waits.
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	member := packet.State.Members[0]
	if _, err := db.Exec(`INSERT INTO group_pending_withdrawals(conv, person, admission, record) VALUES(?, ?, ?, ?)`, conv, member.Person, member.Admission.Hash(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO participation_events(hash, conv, pid, type, author, event, received_at) VALUES(?, ?, ?, 'invite', '{}', '{}', ?)`,
		strings.Repeat("e", 64), conv, protocol.NewID(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Participations(conv); !errors.Is(err, client.ErrGroupContextPending) {
		t.Fatalf("the group's agents resolve here: %v", err)
	}

	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(NewLive(alice), strings.TrimPrefix(ts.URL, "http://"), testToken)
	resp := do(t, ts, "GET", "/api/approvals", "", authed(ts, nil))
	var v ApprovalsView
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&v) != nil {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/approvals: %d %s", resp.StatusCode, body)
	}
	if len(v.Questions) != 1 || v.Questions[0].Address != "bob/desk" || len(v.Participations) != 0 || len(v.Unresolved) != 1 || v.Unresolved[0] != conv {
		t.Fatalf("with a pending group: %+v", v)
	}
	if resp := do(t, ts, "POST", "/api/approvals/revoke", `{"kind":"question","address":"bob/desk"}`, post(ts)); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("revoking a question approval: %d %s", resp.StatusCode, body)
	}
	if approved, err := alice.QuestionApprovals(); err != nil || len(approved) != 0 {
		t.Fatalf("questions still approved: %v %v", approved, err)
	}
}
