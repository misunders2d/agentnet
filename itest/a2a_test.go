package itest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/misunders2d/agentnet/internal/a2abind"
)

type inboxEntry struct {
	ID, From, Kind, State, Body string
	Attachments                 []struct{ Name string }
}

func (c *cli) inbox(home string) []inboxEntry {
	var out []inboxEntry
	json.Unmarshal([]byte(c.run("--home", home, "inbox", "--json")), &out)
	return out
}

// stockClient builds an official A2A SDK client for the adapter at base.
func stockClient(t *testing.T, ctx context.Context, base, token string) (*a2aclient.Client, context.Context) {
	t.Helper()
	card, err := agentcard.NewResolver(http.DefaultClient).Resolve(ctx, base, agentcard.WithRequestHeader("Authorization", "Bearer "+token))
	if err != nil {
		t.Fatalf("resolve card: %v", err)
	}
	creds := a2aclient.NewInMemoryCredentialsStore()
	creds.Set("s", a2abind.SchemeName, a2aclient.AuthCredential(token))
	cl, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithCallInterceptors(&a2aclient.AuthInterceptor{Service: creds}))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return cl, a2aclient.AttachSessionID(ctx, "s")
}

func sendText(t *testing.T, ctx context.Context, cl *a2aclient.Client, kind string, parts ...*a2a.Part) *a2a.Task {
	t.Helper()
	msg := a2a.NewMessage(a2a.MessageRoleUser, parts...)
	if kind != "" {
		msg.Metadata = map[string]any{a2abind.KindKey: kind}
	}
	res, err := cl.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	task, ok := res.(*a2a.Task)
	if !ok {
		t.Fatalf("send returned %T", res)
	}
	return task
}

func waitTask(t *testing.T, ctx context.Context, cl *a2aclient.Client, id a2a.TaskID, want a2a.TaskState) *a2a.Task {
	t.Helper()
	var task *a2a.Task
	waitFor(t, "task "+string(id)+" "+string(want), func() bool {
		var err error
		task, err = cl.GetTask(ctx, &a2a.GetTaskRequest{ID: id})
		return err == nil && task.Status.State == want
	})
	return task
}

// TestA2AStockClientJourney: an official A2A SDK client talks to bob through
// alice's `agentnet a2a serve`, while bob is offline at first and answers by
// hand later. All AgentNet parties are separate processes.
func TestA2AStockClientJourney(t *testing.T) {
	c := buildCLI(t)
	hubAddr := freeAddr(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", hubAddr)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code, _ := os.ReadFile(filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	c.run("--home", "carol", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "carol"))
	c.start("alice-daemon.log", "--home", "alice", "daemon")

	a2aAddr := freeAddr(t)
	stopA2A := c.start("a2a.log", "--home", "alice", "a2a", "serve", "--peer", "bob/desk", "--listen", a2aAddr)
	base := "http://" + a2aAddr
	waitFile(t, filepath.Join(c.dir, "alice", "a2a-token"))
	tokenData, _ := os.ReadFile(filepath.Join(c.dir, "alice", "a2a-token"))
	token := strings.TrimSpace(string(tokenData))
	waitFor(t, "adapter", func() bool { _, err := http.Get(base); return err == nil })

	// No or wrong token: nothing, not even the card.
	for _, h := range []string{"", "Bearer wrong"} {
		req, _ := http.NewRequest("GET", base+"/.well-known/agent-card.json", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("card with %q: %v %v", h, resp, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, cctx := stockClient(t, ctx, base, token)

	// Bob is offline: everything is submitted at once, nothing more.
	q := sendText(t, cctx, cl, "", a2a.NewTextPart("what is 2+2?"))
	data := make([]byte, 1<<20)
	rand.Read(data)
	os.WriteFile(filepath.Join(c.dir, "input.bin"), data, 0o600)
	task := sendText(t, cctx, cl, "task", a2a.NewTextPart("process the attached file"),
		a2a.NewFileURLPart(a2a.URL(fileURL(filepath.Join(c.dir, "input.bin"))), "application/octet-stream"))
	declined := sendText(t, cctx, cl, "task", a2a.NewTextPart("delete everything"))
	for _, tk := range []*a2a.Task{q, task, declined} {
		if tk.Status.State != a2a.TaskStateSubmitted {
			t.Fatalf("task %s state %s while bob offline", tk.ID, tk.Status.State)
		}
	}

	// Bob comes online; the tasks wait for him, the files are his to fetch.
	c.start("bob-daemon.log", "--home", "bob", "daemon")
	waitFor(t, "bob's inbox", func() bool { return len(c.inbox("bob")) == 3 })
	states := map[string]string{}
	for _, m := range c.inbox("bob") {
		states[m.ID] = m.State
	}
	if states[string(q.ID)] != "held" || states[string(task.ID)] != "awaiting" {
		t.Fatalf("bob's states: %v", states)
	}
	if got := waitTask(t, cctx, cl, task.ID, a2a.TaskStateSubmitted); got.Metadata["agentnet.delivery"] == "" {
		t.Fatal("no delivery detail")
	}
	os.Mkdir(filepath.Join(c.dir, "bobin"), 0o700)
	saved := c.run("--home", "bob", "download", "--dir", "bobin", string(task.ID))
	if got, _ := os.ReadFile(filepath.Join(c.dir, saved)); !bytes.Equal(got, data) {
		t.Fatal("A2A file part did not arrive intact")
	}
	os.WriteFile(filepath.Join(c.dir, "result.txt"), []byte("processed OK"), 0o600)
	c.run("--home", "bob", "reply", string(q.ID), "4")
	c.run("--home", "bob", "reply", "--file", "result.txt", string(task.ID), "done, see file")
	c.run("--home", "bob", "decline", string(declined.ID), "no")

	done := waitTask(t, cctx, cl, q.ID, a2a.TaskStateCompleted)
	if txt := done.Artifacts[0].Parts[0].Text(); txt != "4" {
		t.Fatalf("answer artifact %q", txt)
	}
	done = waitTask(t, cctx, cl, task.ID, a2a.TaskStateCompleted)
	parts := done.Artifacts[0].Parts
	if len(parts) != 2 || parts[1].Filename != "result.txt" || !strings.HasPrefix(string(parts[1].Content.(a2a.URL)), "agentnet://attachment/") {
		t.Fatalf("result artifact parts %+v", parts)
	}
	waitTask(t, cctx, cl, declined.ID, a2a.TaskStateRejected)
	os.Mkdir(filepath.Join(c.dir, "alicein"), 0o700)
	saved = c.run("--home", "alice", "download", "--dir", "alicein", string(done.Artifacts[0].ID))
	if got, _ := os.ReadFile(filepath.Join(c.dir, saved)); string(got) != "processed OK" {
		t.Fatalf("result file %q", got)
	}

	// Restart the adapter: the durable view is unchanged.
	stopA2A()
	c.start("a2a2.log", "--home", "alice", "a2a", "serve", "--peer", "bob/desk", "--listen", a2aAddr)
	waitFor(t, "adapter restart", func() bool { _, err := http.Get(base); return err == nil })
	cl, cctx = stockClient(t, ctx, base, token)
	waitTask(t, cctx, cl, q.ID, a2a.TaskStateCompleted)

	// Lookups outside this peer, cancellation and unsupported shapes.
	toCarol := strings.Fields(c.run("--home", "alice", "send", "carol/desk", "private"))[0]
	for _, id := range []string{toCarol, "0123456789abcdef0123456789abcdef"} {
		if _, err := cl.GetTask(cctx, &a2a.GetTaskRequest{ID: a2a.TaskID(id)}); !errors.Is(err, a2a.ErrTaskNotFound) {
			t.Fatalf("foreign/unknown task %s: %v", id, err)
		}
	}
	if _, err := cl.CancelTask(cctx, &a2a.CancelTaskRequest{ID: task.ID}); !errors.Is(err, a2a.ErrTaskNotCancelable) {
		t.Fatalf("cancel: %v", err)
	}
	cont := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("more"))
	cont.TaskID = q.ID
	if _, err := cl.SendMessage(cctx, &a2a.SendMessageRequest{Message: cont}); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Fatalf("continuation: %v", err)
	}
	remote := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewFileURLPart("https://example.com/x", "text/plain"))
	if _, err := cl.SendMessage(cctx, &a2a.SendMessageRequest{Message: remote}); !errors.Is(err, a2a.ErrUnsupportedContentType) {
		t.Fatalf("remote URL part: %v", err)
	}
}

// fileURL builds a file:// URL for a local path (file:///C:/x on Windows).
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
