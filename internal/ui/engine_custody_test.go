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

// Both directions learn delivery through the stream, with no receipt GET.
func TestBrowserEngineReceiptPush(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	stop := runDaemon(t, alice)
	if _, err = alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	n := startEngineNode(t, dir)
	n.ok(map[string]any{"op": "init", "base": "https://" + h.Addr})
	code, err := alice.Invite(ctx, "dana", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	n.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	n.api("/api/person", map[string]any{"label": "Dana"})
	n.ok(map[string]any{"op": "start"})
	n.until("stream connected", func() bool {
		v := n.ok(map[string]any{"op": "status"})
		return v["connected"] == true && v["members"] == true
	})
	var conv string
	n.until("DM ready", func() bool {
		v := n.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": alice.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	stop()
	sent := n.api("/api/dm/send", map[string]any{"conv": conv, "body": "receipt after reconnect"})
	id := sent["id"].(string)
	n.until("browser custody", func() bool {
		return n.ok(map[string]any{"op": "outbox", "id": id})["rec"].(map[string]any)["state"] == "custody"
	})
	runDaemon(t, alice)
	n.until("browser pushed delivery", func() bool {
		return n.ok(map[string]any{"op": "outbox", "id": id})["rec"].(map[string]any)["state"] == "delivered"
	})
	n.ok(map[string]any{"op": "stopStream"})
	out, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "native waits for browser"})
	if err != nil {
		t.Fatal(err)
	}
	n.ok(map[string]any{"op": "start"})
	n.until("native pushed delivery", func() bool {
		rows, e := alice.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, m := range rows {
			if m.LID == out.LID {
				return m.Delivery == "delivered"
			}
		}
		return false
	})
	if c := n.ok(map[string]any{"op": "receiptReads"})["count"].(float64); c != 0 {
		t.Fatalf("browser made %v status GETs", c)
	}
}

// The sender browser releases an already encrypted invitation on a signed
// capability change, without a manual retry or a status request.
func TestEngineHumanInviteWaits(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	runDaemon(t, alice)
	if _, err = alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	join := func(label string) *engineNode {
		n := startEngineNode(t, dir)
		n.ok(map[string]any{"op": "init", "base": "https://" + h.Addr})
		code, err := alice.Invite(ctx, label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		n.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
		n.api("/api/person", map[string]any{"label": label})
		n.ok(map[string]any{"op": "start"})
		n.until("connected with capabilities", func() bool {
			v := n.ok(map[string]any{"op": "status"})
			return v["connected"] == true && v["members"] == true
		})
		return n
	}
	dana, carol := join("dana"), join("carol")
	carol.ok(map[string]any{"op": "testHumanCaps", "supported": false})
	var conv string
	dana.until("original DM ready", func() bool {
		v := dana.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": alice.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	check := dana.api("/api/dm/guest/check", map[string]any{"conv": conv, "host": "carol/phone"})
	if check["ready"] != false || len(check["needs_update"].([]any)) != 1 {
		t.Fatalf("%v", check)
	}
	guest := dana.api("/api/dm/guest/invite", map[string]any{"conv": conv, "host": "carol/phone"})
	pid := guest["pid"].(string)
	dana.until("guest waiting for update", func() bool {
		for _, x := range dana.api("/api/dm?id="+conv, nil)["guests"].([]any) {
			g := x.(map[string]any)
			if g["pid"] == pid {
				return len(g["needs_update"].([]any)) == 1
			}
		}
		return false
	})
	carol.ok(map[string]any{"op": "testHumanCaps", "supported": true})
	carol.until("invitation released after update", func() bool {
		v := carol.call(map[string]any{"op": "api", "path": "/api/dm?id=" + conv})
		if v["error"] != nil {
			return false
		}
		for _, x := range v["v"].(map[string]any)["guests"].([]any) {
			g := x.(map[string]any)
			if g["pid"] == pid {
				return g["state"] == "invited" && g["host_here"] == true
			}
		}
		return false
	})
	if got := dana.ok(map[string]any{"op": "receiptReads"})["count"].(float64); got != 0 {
		t.Fatalf("status GETs: %v", got)
	}
}
