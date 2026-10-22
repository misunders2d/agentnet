package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// The requests, invitation, group and status are real signed records. Only the
// worker lifecycle transitions are staged; HumanOnly publishes the real status
// without launching a native harness. Node consumes the actual public DTOs.
func TestComposerOwnGroupCurrentAttempt(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	hub, home, peerHome := t.TempDir(), t.TempDir(), t.TempDir()
	testhub.Start(t, hub, "127.0.0.1:0", "")
	a, err := client.Join(ctx, home, testhub.BootstrapCode(t, hub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	code, err := a.Invite(ctx, "peer", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Join(ctx, peerHome, code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	stopA, stopB := runDaemon(t, a), runDaemon(t, b)
	defer stopA()
	defer stopB()
	for _, agent := range []*client.Agent{a, b} {
		if _, err = agent.CreatePerson(ctx, agent.Address); err != nil {
			t.Fatal(err)
		}
	}
	conv := liveTwoMemberGroup(t, a, b)
	named, err := a.CreateLocalAgent("Pi", client.Responder{Harness: "pi", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := a.InviteNamedAgent(ctx, conv, a.Address, named.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "signed own named participation active", func() bool { p, err = a.Participation(p.PID); return err == nil && p.Claimable() })
	stopA() // No request exists until its worker has stopped.
	q, err := a.AskAgent(ctx, p.PID, envelope.KindQuestion, "How is system health?")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stage := func(state string, attempt int64, due bool) {
		t.Helper()
		n := 0
		if due {
			n = 1
		}
		res, e := db.Exec(`UPDATE inbox SET state=?,attempts=?,status_due=? WHERE id=? AND local=1`, state, attempt, n, q.LID)
		if e != nil {
			t.Fatal(e)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("local row count=%d", n)
		}
	}
	stage("running", 1, true)
	humanCtx, stopHuman := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); a.Run(humanCtx, client.RunOptions{HumanOnly: true}) }()
	defer func() { stopHuman(); <-done }()
	var raw string
	waitFor(t, "actual signed running attempt1 status", func() bool {
		return db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND sub='status' AND ref_id=? AND json_extract(body,'$.attempt')=1 AND json_extract(body,'$.state')='running' LIMIT 1`, conv, q.LID).Scan(&raw) == nil
	})
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	peerID, err := identity.Load(filepath.Join(peerHome, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	inner, err := envelope.Open(env, peerID, b.Address, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	var signed envelope.Status
	if err = json.Unmarshal([]byte(inner.Body), &signed); err != nil || signed.Attempt != 1 || signed.State != "running" {
		t.Fatalf("signed source=%+v %v", signed, err)
	}
	live := NewLive(a)
	type observation struct {
		Label    string   `json:"label"`
		State    string   `json:"state"`
		Attempt  int64    `json:"attempt"`
		Overview Overview `json:"overview"`
		Thread   DMThread `json:"thread"`
	}
	var cases []observation
	capture := func(label, state string, want int64) {
		t.Helper()
		o, e := live.Overview()
		if e != nil {
			t.Fatal(e)
		}
		v, e := live.DM(conv)
		if e != nil {
			t.Fatal(e)
		}
		cases = append(cases, observation{label, state, want, o, v})
	}
	capture("own-running-1", "running", 1)
	stage("running", 2, false)
	// A retained signed status still names attempt1: it must never overwrite
	// the exact local row's current attempt2 or be relabeled after cancellation.
	before, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	foundOld := false
	for _, m := range before {
		if m.LID == q.LID && m.Exec != nil {
			foundOld = m.Exec.Attempt == 1
		}
	}
	if !foundOld {
		t.Fatal("fixture lost stale signed attempt1")
	}
	capture("own-running-2-status-1", "running", 2)
	ref := envelope.Ref{ID: q.LID, Fingerprint: a.Self().Fingerprint()}
	if _, err = live.RequestFollowup(RequestFollowupDraft{Conv: conv, Ref: ref, ID: protocol.NewID(), Attempt: 1, Body: "Wrong stale attempt"}); err == nil {
		t.Fatal("stale attempt1 accepted for current2")
	}
	if _, err = live.RequestFollowup(RequestFollowupDraft{Conv: conv, Ref: ref, ID: protocol.NewID(), Attempt: 2, Body: "And what is weather tomorrow?"}); err != nil {
		t.Fatalf("exact attempt2 route: %v", err)
	}
	for _, state := range []string{"needs_human", "answered", "resolved"} {
		stage(state, 2, false)
		capture("own-clarification-"+state, state, 0)
	}
	stage("cancelled", 2, false)
	capture("own-ended-status-1", "cancelled", 0)
	if _, err = live.RequestFollowup(RequestFollowupDraft{Conv: conv, Ref: ref, ID: protocol.NewID(), Attempt: 2, Body: "Ended work"}); err == nil {
		t.Fatal("ended attempt accepted")
	}
	stage("running", 0, false)
	capture("own-unverifiable-status-1", "running", 0)
	payload, err := json.Marshal(map[string]any{"pid": p.PID, "original": q.LID, "author": ref.Fingerprint, "cases": cases})
	if err != nil {
		t.Fatal(err)
	}
	script := os.Getenv("AGENTNET_STEERING_MODEL_CHECK")
	if script == "" {
		script = "testdata/composer_local_attempt_check.mjs"
	}
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "AGENTNET_SOURCE="+source)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual native DTO through Store/Composer: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// A starts the original on another host; B is a real signed linked human
// device and learns it through production device-history replication.
// Only the host's worker lifecycle is staged, as in the group fixture above.
func TestComposerLinkedDirectCurrentAttempt(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	hub, home, hostHome := t.TempDir(), t.TempDir(), t.TempDir()
	testhub.Start(t, hub, "127.0.0.1:0", "")
	a, err := client.Join(ctx, home, testhub.BootstrapCode(t, hub), "human-a")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	code, err := a.Invite(ctx, "host", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	host, err := client.Join(ctx, hostHome, code, "agent-host")
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	stopA := runDaemon(t, a)
	defer stopA()
	if _, err = a.CreatePerson(ctx, "Owner"); err != nil {
		t.Fatal(err)
	}
	offer, err := a.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.JoinAndLink(ctx, t.TempDir(), offer.Code, "human-b")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	stopB := runDaemon(t, b)
	defer stopB()
	waitFor(t, "signed linked B request", func() bool {
		rows, e := a.PendingLinks()
		for _, row := range rows {
			if row.State == client.LinkPending {
				return a.DecideLink(ctx, row.ID, true) == nil
			}
		}
		if e != nil {
			t.Fatal(e)
		}
		return false
	})
	waitFor(t, "B holds current owner roster", func() bool { p, ok, e := b.Person(); return e == nil && ok && len(p.Devices) == 2 })
	named, err := host.CreateLocalAgent("Selected agent", client.Responder{Harness: "pi", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	runHuman := func() func() {
		c, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); host.Run(c, client.RunOptions{HumanOnly: true}) }()
		return func() { stop(); <-done }
	}
	stopHost := runHuman()
	defer stopHost()
	// B establishes its own exact current host pin through the ordinary API.
	if _, err = b.SendMessage(ctx, client.Outgoing{To: host.Address, Body: "linked B host pin"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(hostHome, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	type projection struct {
		Label    string   `json:"label"`
		Original string   `json:"original"`
		Author   string   `json:"author"`
		HostKey  string   `json:"host_key"`
		Overview Overview `json:"overview"`
		Thread   Thread   `json:"thread"`
	}
	var linked []projection
	for _, target := range []*envelope.Target{nil, {Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: named.ID}} {
		q, e := a.SendMessage(ctx, client.Outgoing{To: host.Address, Kind: envelope.KindQuestion, Body: "Original linked steering work", Target: target})
		if e != nil {
			stopHost()
			t.Fatal(e)
		}
		waitFor(t, "host stores signed original", func() bool {
			var n int
			return db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, q.ID).Scan(&n) == nil && n == 1
		})
		linked = append(linked, projection{Original: q.ID, Author: a.Self().Fingerprint(), HostKey: host.Self().Fingerprint()})
	}
	stopHost()
	for i := range linked {
		res, e := db.Exec(`UPDATE inbox SET state='running',attempts=1,last_attempt_at=unixepoch(),status_due=1 WHERE id=?`, linked[i].Original)
		if e != nil {
			t.Fatal(e)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatal("original worker row missing")
		}
	}
	stopHost = runHuman()
	defer stopHost()
	live := NewLive(b)
	for i := range linked {
		c := &linked[i]
		c.Label = []string{"linked-default", "linked-named"}[i]
		waitFor(t, c.Label+" original and signed running status in B's actual Thread", func() bool {
			v, e := live.Thread(c.Original)
			if e != nil {
				return false
			}
			for _, m := range v.Messages {
				if m.ID == c.Original && m.Exec != nil && m.Exec.State == "running" && m.Exec.Attempt == 1 {
					c.Thread = v
					return true
				}
			}
			return false
		})
		c.Overview, err = live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range c.Thread.Messages {
			if m.ID == c.Original {
				t.Logf("%s original=%s dir=%s from_key=%s history=%v exec=%+v pin=%s target=%+v", c.Label, m.ID, m.Dir, m.FromKey, m.History, m.Exec, c.Thread.Key.Pinned, m.Target)
			}
		}
	}
	running := append([]projection(nil), linked...)
	// One signed transition per existing request proves both ordinary modes
	// without multiplying daemon restarts inside this focused fixture.
	states := []string{"needs_human", "answered"}
	stopHost()
	for i, old := range running {
		if _, err = db.Exec(`UPDATE inbox SET state=?,status_due=status_due+1 WHERE id=?`, states[i], old.Original); err != nil {
			t.Fatal(err)
		}
	}
	stopHost = runHuman()
	defer stopHost()
	for i, old := range running {
		c, state := old, states[i]
		c.Label += "-" + state
		waitFor(t, c.Label+" exact signed clarification status", func() bool {
			v, e := live.Thread(c.Original)
			if e != nil {
				return false
			}
			for _, m := range v.Messages {
				if m.ID == c.Original && m.Exec != nil && m.Exec.State == state {
					c.Thread = v
					return true
				}
			}
			return false
		})
		linked = append(linked, c)
	}
	payload, err := json.Marshal(map[string]any{"cases": []any{}, "linked": linked})
	if err != nil {
		t.Fatal(err)
	}
	if dest := os.Getenv("AGENTNET_LINKED_PROJECTION"); dest != "" {
		if err = os.WriteFile(dest, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "node", "testdata/composer_local_attempt_check.mjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SOURCE="+source)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("linked Live.Thread through actual Composer sends: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
