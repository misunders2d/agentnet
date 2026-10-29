package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Both runtimes publish their normal signed session capabilities. No test-only
// caps or fabricated inbox rows make the selected executor reachable; the
// browser discovers both named agents but asks them only in a chat.
func TestBrowserNamedAgentPublishedJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	bin := t.TempDir()
	runs := filepath.Join(bin, "runs")
	script := "#!/bin/sh\ncat >/dev/null\ncat agent-name >> '" + runs + "'\ncat agent-name\n"
	for _, harness := range []string{"claude", "pi"} {
		if err := testhub.WriteNativeScript(filepath.Join(bin, harness), script, harness); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, hubDir, "127.0.0.1:0", "")
	host := personAgent(t, ctx, testhub.BootstrapCode(t, hubDir), "laptop", "Host")
	var agents []protocol.AgentRecord
	for i, label := range []string{"A", "B"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "agent-name"), []byte(label+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		agent, err := host.CreateLocalAgent(label, client.Responder{Harness: []string{"claude", "pi"}[i], Dir: dir, Timeout: 20 * time.Second})
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
	// MEL-713 step 1: a named agent is no longer asked in a device
	// conversation; the refusal runs nothing and changes no default.
	for _, agent := range agents {
		w.refuses("a device ask of a named agent", w.call(map[string]any{"op": "api", "path": "/api/send", "body": map[string]any{"to": host.Address, "kind": "question", "body": "selected question", "agent_id": agent.ID}}), "read only")
	}
	time.Sleep(300 * time.Millisecond)
	if log, err := os.ReadFile(runs); err == nil && len(log) != 0 {
		t.Fatalf("a refused device ask ran: %q", log)
	}
	if responder, err := host.Responder(); err != nil || responder != nil {
		t.Fatalf("device default changed: %+v %v", responder, err)
	}
}
