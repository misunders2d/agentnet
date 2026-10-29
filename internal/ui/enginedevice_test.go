package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// threadWith is the browser's device conversation holding a message with
// body, or nil.
func threadWith(w *engineNode, body string) map[string]any {
	for _, s := range w.api("/api/overview", nil)["threads"].([]any) {
		t := w.api("/api/thread?id="+s.(map[string]any)["id"].(string), nil)
		for _, m := range t["messages"].([]any) {
			if m.(map[string]any)["body"] == body {
				return t
			}
		}
	}
	return nil
}

func goInbox(a *client.Agent, body string) client.Message {
	ms, _ := a.Inbox(false, false)
	for _, m := range ms {
		if m.Body == body {
			return m
		}
	}
	return client.Message{}
}

// MEL-713 step 1: the browser starts no device conversation (version 1)
// any more: /api/send is refused, nothing is kept and nothing runs. A
// request an older device still sends it is held (nothing runs in a
// browser) and answered by hand in that conversation, as before. No model
// runs: the responder is a stub.
func TestBrowserEngineDeviceChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	bin := t.TempDir()
	runs := filepath.Join(bin, "runs")
	testhub.WriteNativeScript(filepath.Join(bin, "claude"), "#!/bin/sh\ncat > /dev/null\necho run >> '"+runs+"'\nprintf 'four o clock\\n'\n", "claude")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ran := func() int { b, _ := os.ReadFile(runs); return strings.Count(string(b), "run") }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.Start(t, dir, "127.0.0.1:0", "")
	laptop, err := client.Join(ctx, filepath.Join(t.TempDir(), "laptop"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { laptop.Close() })
	if err := laptop.SetResponder(&client.Responder{Harness: "claude", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, laptop)
	code, _ := laptop.Invite(ctx, "dana", time.Hour, false)
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + h.Addr})
	phone := w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})["address"].(string)
	w.ok(map[string]any{"op": "start"})
	w.until("connected", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == true })
	if err := laptop.Approve(phone); err != nil {
		t.Fatal(err)
	}

	count := func() float64 { return w.ok(map[string]any{"op": "count", "store": "outbox"})["n"].(float64) }
	before := count()
	for _, kind := range []string{"question", "task", "message"} {
		w.refuses("a new "+kind+" in a device conversation", w.call(map[string]any{"op": "api", "path": "/api/send",
			"body": map[string]any{"to": laptop.Address, "kind": kind, "body": "what time is it?"}}), "read only")
	}
	time.Sleep(300 * time.Millisecond)
	if count() != before || ran() != 0 || goInbox(laptop, "what time is it?").ID != "" {
		t.Fatalf("a refused device send was kept or ran (%d runs)", ran())
	}

	// The laptop asks the phone the old way: held here (nothing runs),
	// answered by hand.
	asked, err := laptop.SendMessage(ctx, client.Outgoing{To: phone, Kind: "question", Body: "are you there?"})
	if err != nil {
		t.Fatal(err)
	}
	var held map[string]any
	w.until("the laptop's question, held", func() bool {
		th := threadWith(w, "are you there?")
		if th == nil {
			return false
		}
		held = th["messages"].([]any)[0].(map[string]any)
		return held["state"] == "held"
	})
	if acts := held["actions"].([]any); len(acts) != 1 || acts[0] != "reply" || !strings.Contains(held["state_text"].(string), "nothing runs in this browser") {
		t.Fatalf("a held question: %v", held)
	}
	if w.api("/api/act", map[string]any{"do": "reply", "id": held["id"], "body": "yes, on my phone"})["note"] == nil {
		t.Fatal("no note for the answer")
	}
	waitFor(t, "the laptop has the answer", func() bool {
		m := goInbox(laptop, "yes, on my phone")
		return m.Kind == "answer" && m.ReplyTo == asked.ID
	})
	if a := threadWith(w, "are you there?")["messages"].([]any)[0].(map[string]any); a["state"] != "answered" || len(a["actions"].([]any)) != 0 {
		t.Fatalf("after answering: %v", a)
	}
}

func stringsOf(v any) string { b, _ := json.Marshal(v); return string(b) }
