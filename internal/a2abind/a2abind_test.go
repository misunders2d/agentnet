package a2abind

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func join(t *testing.T, code, name string) *client.Agent {
	t.Helper()
	a, err := client.Join(context.Background(), filepath.Join(t.TempDir(), name), code, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func run(t *testing.T, a *client.Agent) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx, client.RunOptions{}); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// A third coworker naming alice's task in its own reply must not complete it.
func TestOnlyThePeersReplyCompletesATask(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice := join(t, testhub.BootstrapCode(t, dir), "alice")
	invite := func(label string) string {
		code, err := alice.Invite(ctx, label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	bob, carol := join(t, invite("bob"), "desk"), join(t, invite("carol"), "desk")
	run(t, alice)
	run(t, bob)
	ad := New(alice, bob.Address, "http://127.0.0.1:1", "token")

	res, err := ad.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("q"))})
	if err != nil {
		t.Fatal(err)
	}
	id := res.(*a2a.Task).ID

	forged, err := carol.SendMessage(ctx, client.Outgoing{To: alice.Address, Body: "forged", ReplyTo: string(id), Kind: envelope.KindAnswer})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "carol's reply to arrive", func() bool {
		msgs, _ := alice.Inbox(false, false)
		for _, m := range msgs {
			if m.ID == forged.ID {
				return true
			}
		}
		return false
	})
	if task, err := ad.GetTask(ctx, &a2a.GetTaskRequest{ID: id}); err != nil || task.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("after a foreign reply: %+v, %v", task, err)
	}

	eventually(t, "bob to receive", func() bool { msgs, _ := bob.Inbox(false, false); return len(msgs) == 1 })
	if _, err := bob.Reply(ctx, string(id), "real answer"); err != nil {
		t.Fatal(err)
	}
	var task *a2a.Task
	eventually(t, "task completed", func() bool {
		task, _ = ad.GetTask(ctx, &a2a.GetTaskRequest{ID: id})
		return task != nil && task.Status.State == a2a.TaskStateCompleted
	})
	if got := task.Artifacts[0].Parts[0].Text(); got != "real answer" {
		t.Fatalf("artifact %q", got)
	}

	if _, err := ad.ListTasks(ctx, &a2a.ListTasksRequest{}); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Fatalf("ListTasks: %v", err)
	}
}
