package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Both runtimes publish their normal signed session capabilities. No test-only
// caps or fabricated inbox rows make the selected executor reachable.
func TestBrowserNamedAgentPublishedJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	bin := t.TempDir()
	runs := filepath.Join(bin, "runs")
	script := "#!/bin/sh\ncat >/dev/null\ncat agent-name >> '" + runs + "'\ncat agent-name\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, hubDir, "127.0.0.1:0", "")
	host := personAgent(t, ctx, testhub.BootstrapCode(t, hubDir), "laptop", "Host")
	var agents []protocol.AgentRecord
	for _, label := range []string{"A", "B"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "agent-name"), []byte(label+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		agent, err := host.CreateLocalAgent(label, client.Responder{Harness: "claude", Dir: dir, Timeout: 20 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, agent)
	}
	if err := host.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	code, err := host.Invite(ctx, "browser", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, hubDir)
	w.ok(map[string]any{"op": "init", "base": "https://" + hub.Addr})
	address := w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})["address"].(string)
	w.ok(map[string]any{"op": "start"})
	w.until("normal signed native catalog discovery", func() bool {
		r := w.call(map[string]any{"op": "api", "path": "/api/agents?host=" + host.Address})
		if r["error"] != nil {
			return false
		}
		v := r["v"].(map[string]any)
		return v["local"] == false && len(v["agents"].([]any)) == 2
	})
	if err := host.Approve(address); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for i, agent := range []protocol.AgentRecord{agents[0], agents[1], agents[0]} {
		body := "selected question " + string(rune('1'+i))
		sent := w.api("/api/send", map[string]any{"to": host.Address, "kind": "question", "body": body, "agent_id": agent.ID, "reply_to": previous})
		var answer map[string]any
		w.until("selected answer in browser", func() bool {
			thread := threadWith(w, body)
			if thread == nil {
				return false
			}
			for _, value := range thread["messages"].([]any) {
				m := value.(map[string]any)
				if m["kind"] == "answer" && m["reply_to"] == sent["id"] {
					answer = m
					return true
				}
			}
			return false
		})
		if answer["agent_id"] != agent.ID || strings.TrimSpace(answer["body"].(string)) != agent.Label || answer["from"] != host.Address {
			t.Fatalf("selected answer lost identity: %v", answer)
		}
		previous = answer["id"].(string)
	}
	log, err := os.ReadFile(runs)
	if err != nil || string(log) != "A\nB\nA\n" {
		t.Fatalf("executor sequence %q: %v", log, err)
	}
	if responder, err := host.Responder(); err != nil || responder != nil {
		t.Fatalf("device default changed: %+v %v", responder, err)
	}
	if err := host.SetLocalAgentResponder(agents[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := host.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	w.refuses("disabled selection has no fallback", w.call(map[string]any{"op": "api", "path": "/api/send", "body": map[string]any{"to": host.Address, "kind": "question", "body": "do not execute", "agent_id": agents[0].ID}}), "")
}
