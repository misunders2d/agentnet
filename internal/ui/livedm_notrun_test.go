package ui

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Real signed group/agent requests; only the worker terminal transition is
// staged in this disposable store. No native assistant or user job runs.
func TestLiveDMOwnNestedNotRunKeepsExactCause(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	hub, home := t.TempDir(), t.TempDir()
	testhub.Start(t, hub, "127.0.0.1:0", "")
	a, err := client.Join(ctx, home, testhub.BootstrapCode(t, hub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.CreatePerson(ctx, "Fictional owner"); err != nil {
		t.Fatal(err)
	}
	code, err := a.Invite(ctx, "colleague", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	person, err := b.CreatePerson(ctx, "Fictional colleague")
	if err != nil {
		t.Fatal(err)
	}
	stopB := runDaemon(t, b)
	defer stopB()
	if err = a.SetResponder(&client.Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	stop := runDaemon(t, a)
	defer stop()
	group, err := a.CreateGroup(ctx, "Fictional nested question")
	if err != nil {
		t.Fatal(err)
	}
	var invitation client.GroupInvitationInfo
	waitFor(t, "invite colleague", func() bool {
		invitation, err = a.InviteGroup(ctx, group.State.Conv, person.Person, nil)
		return err == nil
	})
	waitFor(t, "colleague invitation", func() bool {
		rows, e := b.GroupInvitations()
		if e != nil {
			return false
		}
		for _, r := range rows {
			if r.ID == invitation.ID {
				return true
			}
		}
		return false
	})
	if err = b.DecideGroupInvitation(ctx, invitation.ID, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "colleague membership", func() bool { g, e := a.GroupContext(group.State.Conv); return e == nil && len(g.State.Members) == 2 })
	named, err := a.CreateLocalAgent("Reviewer", client.Responder{Harness: "claude", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	from, err := a.InviteAgent(ctx, group.State.Conv, a.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.InviteNamedAgent(ctx, group.State.Conv, a.Address, named.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "own group agents active", func() bool {
		f, e := a.Participation(from.PID)
		p, e2 := a.Participation(target.PID)
		return e == nil && e2 == nil && f.Claimable() && p.Claimable()
	})
	stop() // Before any question: never launch even a stand-in native harness.
	root, err := a.AskAgent(ctx, from.PID, envelope.KindQuestion, "Check the fictional plan")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE inbox SET state='running' WHERE id=? AND local=1`, root.LID); err != nil {
		t.Fatal(err)
	}
	child, err := a.SendRoomAsk(ctx, root.LID, target.PID, envelope.KindQuestion, "Review the fictional plan")
	if err != nil {
		t.Fatal(err)
	}
	const detail = "not run: originating local run stopped"
	result, err := db.Exec(`UPDATE inbox SET state='not_run',detail=? WHERE id=? AND local=1`, detail, child.LID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("exact local child updated %d rows", n)
	}
	thread, err := NewLive(a).DM(group.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range thread.Messages {
		if m.LID == child.LID {
			if m.Dir != "out" || m.Exec == nil || m.Exec.State != "not_run" || m.JobDetail != detail {
				t.Fatalf("child execution projection: %+v", m)
			}
			want := "Your agent: not run: the agent request that started this request finished or stopped first."
			if m.StateText != want || strings.Contains(m.StateText, "part in this DM ended") {
				t.Fatalf("state_text=%q; want %q", m.StateText, want)
			}
			return
		}
	}
	t.Fatal("exact nested child missing from live DM")
}
