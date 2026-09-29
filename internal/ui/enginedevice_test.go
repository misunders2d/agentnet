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
	"github.com/misunders2d/agentnet/internal/identity"
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

// The browser writes to a device, as agentnet send does (version 1): a
// question its owner approved it for is answered once by that computer's
// responder; a task waits there until accepted, then its result comes
// back; a plain message never runs. Answers join their question's
// conversation here. A reply goes only to its own device's conversation;
// a changed key sends nothing; a send refused before it is stored leaves
// nothing behind. No model runs: the responder is a stub.
func TestBrowserEngineDeviceChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	bin := t.TempDir()
	runs := filepath.Join(bin, "runs")
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho run >> '"+runs+"'\nprintf 'four o clock\\n'\n"), 0o700)
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

	// A question: answered once, the answer in its conversation.
	q := w.api("/api/send", map[string]any{"to": laptop.Address, "kind": "question", "body": "what time is it?"})
	if q["id"] == nil || q["state"] == "failed" {
		t.Fatalf("question: %v", q)
	}
	w.until("the answer", func() bool {
		th := threadWith(w, "what time is it?")
		return th != nil && len(th["messages"].([]any)) == 2
	})
	th := threadWith(w, "what time is it?")
	ans := th["messages"].([]any)[1].(map[string]any)
	if th["peer"] != laptop.Address || ans["kind"] != "answer" || ans["reply_to"] != q["id"] || ans["dir"] != "in" || !strings.Contains(ans["body"].(string), "four o clock") {
		t.Fatalf("the answer: %v", th)
	}
	time.Sleep(300 * time.Millisecond)
	if ran() != 1 {
		t.Fatalf("the responder ran %d times for one question", ran())
	}

	// A plain message never runs; a task waits there until accepted.
	w.api("/api/send", map[string]any{"to": laptop.Address, "kind": "message", "body": "hey!"})
	waitFor(t, "the laptop has the message", func() bool { return goInbox(laptop, "hey!").ID != "" })
	task := w.api("/api/send", map[string]any{"to": laptop.Address, "kind": "task", "body": "rotate the logs"})
	waitFor(t, "the laptop has the task", func() bool { return goInbox(laptop, "rotate the logs").ID != "" })
	time.Sleep(500 * time.Millisecond)
	if ran() != 1 {
		t.Fatalf("a message or an unaccepted task ran (%d runs)", ran())
	}
	if s := w.api("/api/overview", nil); !strings.Contains(stringsOf(s["threads"]), `"waiting":true`) {
		t.Fatalf("a task waiting for its result: %v", s["threads"])
	}
	if err := laptop.Accept(goInbox(laptop, "rotate the logs").ID); err != nil {
		t.Fatal(err)
	}
	w.until("the task's result", func() bool {
		th := threadWith(w, "rotate the logs")
		if th == nil || len(th["messages"].([]any)) != 2 {
			return false
		}
		r := th["messages"].([]any)[1].(map[string]any)
		return r["kind"] == "result" && r["reply_to"] == task["id"]
	})

	// The laptop asks the phone: held here (nothing runs), answered by hand.
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

	// Replies stay with their device; a changed key or a bad address sends
	// nothing and keeps nothing.
	count := func() float64 { return w.ok(map[string]any{"op": "count", "store": "outbox"})["n"].(float64) }
	before := count()
	w.refuses("a reply to another device's message", w.call(map[string]any{"op": "api", "path": "/api/send",
		"body": map[string]any{"to": "admin/elsewhere", "kind": "message", "body": "x", "reply_to": q["id"]}}), "stays in its conversation")
	w.refuses("no address", w.call(map[string]any{"op": "api", "path": "/api/send", "body": map[string]any{"to": "nobody", "body": "x"}}), "not an AgentNet address")
	w.refuses("a kind", w.call(map[string]any{"op": "api", "path": "/api/send", "body": map[string]any{"to": laptop.Address, "kind": "answer", "body": "x"}}), "Choose message")
	other, _ := identity.Generate()
	op := other.Public(laptop.Address)
	w.ok(map[string]any{"op": "tamperPin", "address": laptop.Address, "json": string(marshalBytes(t, op)), "fingerprint": op.Fingerprint()})
	w.refuses("a changed key", w.call(map[string]any{"op": "api", "path": "/api/send", "body": map[string]any{"to": laptop.Address, "body": "x"}}), "key changed")
	if count() != before {
		t.Fatal("a refused send was kept")
	}
	if th := threadWith(w, "what time is it?"); th["key"].(map[string]any)["pending"] == "" {
		t.Fatalf("the changed key is not shown: %v", th["key"])
	}
}

func stringsOf(v any) string { b, _ := json.Marshal(v); return string(b) }
