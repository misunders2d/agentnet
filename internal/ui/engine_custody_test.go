package ui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// BUG-25, the browser's part: a message the server holds says only that
// it is on the server and that delivery is not confirmed yet; it never
// claims to wait for the other person to connect (the server's custody
// does not show who has connected).
func TestBrowserEngineCustodyNotConfirmed(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	stop := runDaemon(t, alice)
	if _, err := alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + hub.Addr})
	code, _ := alice.Invite(ctx, "dana", time.Hour, false)
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})
	var conv string
	w.until("a DM with Alice", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": alice.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	// Alice's daemon stops: what is sent to her now stays in the server's
	// custody.
	stop()
	w.api("/api/dm/send", map[string]any{"conv": conv, "body": "are you there?"})
	var msg map[string]any
	w.until("the message in the server's custody", func() bool {
		ms := w.api("/api/dm?id="+conv, nil)["messages"].([]any)
		msg = ms[len(ms)-1].(map[string]any)
		return msg["state"] == "custody"
	})
	if want := "On the server; delivery to " + alice.Address + " not confirmed yet"; msg["state_text"] != want {
		t.Fatalf("custody says %q, want %q", msg["state_text"], want)
	}
}
