package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// BUG-11, the browser's part (as client.SendConv): a changed key of one of
// the other person's devices refuses the turn until it is trusted; it is
// not sent to their other device with that one silently left out.
func TestBrowserEngineTurnRefusedPastChangedKey(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	laptop, err := client.Join(ctx, filepath.Join(t.TempDir(), "laptop"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { laptop.Close() })
	runDaemon(t, laptop)
	if _, err := laptop.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	var offer client.DeviceLinkOffer
	until("a device link", func() bool { offer, err = laptop.NewDeviceLink(ctx); return err == nil })
	phone, err := client.JoinAndLink(ctx, filepath.Join(t.TempDir(), "phone"), offer.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	runDaemon(t, phone)
	var req client.LinkRequest
	until("the phone's request", func() bool {
		rs, _ := laptop.PendingLinks()
		for _, r := range rs {
			if r.State == "pending" {
				req = r
			}
		}
		return req.ID != ""
	})
	if err := laptop.DecideLink(ctx, req.ID, true); err != nil {
		t.Fatal(err)
	}
	until("the phone linked", func() bool { return phone.LinkState().State == "linked" })

	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + hub.Addr})
	code, _ := laptop.Invite(ctx, "dana", time.Hour, false)
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})
	var conv string
	w.until("a DM with Alice", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": laptop.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	if sent := w.api("/api/dm/send", map[string]any{"conv": conv, "body": "to both"}); len(sent["copies"].([]any)) != 2 {
		t.Fatalf("copies: %v", sent)
	}
	other, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := other.Public(phone.Address)
	w.ok(map[string]any{"op": "tamperPin", "address": phone.Address, "json": publicJSON(t, pub), "fingerprint": pub.Fingerprint()})
	w.refuses("a turn past a changed key", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": conv, "body": "past the change"}}), "key changed")
	if strings.Contains(dmBodies(w.api("/api/dm?id="+conv, nil)), "past the change") {
		t.Fatal("the turn was kept for Alice's other device")
	}
}
