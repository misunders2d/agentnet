package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// MEL-546: a phone opened after hours closed catches up on what the relay
// kept for it. Every message is pushed once, stored once and acknowledged
// as delivered; receipts go out several at a time alongside the next
// messages, not one round trip after another before them; the page is told
// a few times, not after every message; and no message makes the engine
// read every message held here again.
//
// As a measurement: AGENTNET_BACKLOG_N messages (default 120) over
// AGENTNET_BACKLOG_HISTORY already held, AGENTNET_BACKLOG_RTT ms per
// request (receipts are then not held back), AGENTNET_BACKLOG_WRITE ms per
// store write, and
// AGENTNET_BACKLOG_PAGE=1 for a page open on the DM that reads its views
// again on every change, as the skins do.
func TestBrowserEngineBacklogCatchUp(t *testing.T) {
	backlogCatchUp(t, envInt("AGENTNET_BACKLOG_N", 120), false)
}

// The same catch-up with a small file on every message (MEL-546 review):
// keeping the files tells the page in the same bursts, and finds each file
// without reading every message held again for it.
func TestBrowserEngineBacklogCatchUpFiles(t *testing.T) {
	backlogCatchUp(t, envInt("AGENTNET_BACKLOG_N", 60), true)
}

func backlogCatchUp(t *testing.T, n int, files bool) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + h.Addr})
	code, err := alice.Invite(ctx, "dana", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("stream connected", func() bool {
		v := w.ok(map[string]any{"op": "status"})
		return v["connected"] == true && v["members"] == true
	})
	var conv string
	w.until("DM ready", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": alice.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	w.api("/api/dm/send", map[string]any{"conv": conv, "body": "hello"})
	w.until("Alice has the DM", func() bool {
		rows, e := alice.ConversationMessages(conv)
		return e == nil && len(rows) == 1
	})
	count := func(store string) int { return int(w.ok(map[string]any{"op": "count", "store": store})["n"].(float64)) }
	if old := envInt("AGENTNET_BACKLOG_HISTORY", 0); old > 0 { // received while the phone was open
		for i := 0; i < old; i++ {
			if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: fmt.Sprintf("earlier %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		w.until("the phone holds the earlier messages", func() bool { return count("inbox") >= old })
	}
	// The phone is closed: what Alice sends now stays in the relay's custody.
	w.ok(map[string]any{"op": "stopStream"})
	before := count("inbox")
	history, _ := alice.ConversationMessages(conv)
	keptBefore := count("files")
	for i := 0; i < n; i++ {
		out := client.ConvOutgoing{Body: fmt.Sprintf("backlog %d", i)}
		if files {
			out.Files = []client.OutgoingFile{{Path: goFile(t, fmt.Sprintf("f%d.txt", i), []byte(fmt.Sprintf("file %d", i)))}}
		}
		if _, err := alice.SendConv(ctx, conv, out); err != nil {
			t.Fatal(err)
		}
	}
	page := ""
	if envInt("AGENTNET_BACKLOG_PAGE", 0) == 1 {
		page = conv
	}
	w.ok(map[string]any{"op": "stats", "reset": true, "latency": envInt("AGENTNET_BACKLOG_RTT", 0), "writeDelay": envInt("AGENTNET_BACKLOG_WRITE", 0), "page": page})
	measure := os.Getenv("AGENTNET_BACKLOG_RTT") != ""
	want := 0 // files the phone keeps
	if files {
		want = n
	}
	if !measure { // the receipts are held back: every message is stored all the same
		w.ok(map[string]any{"op": "holdAcks", "on": true})
	}
	start := time.Now()
	w.ok(map[string]any{"op": "start"})
	var stored time.Duration
	for deadline := time.Now().Add(8 * time.Minute); ; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("catch-up did not finish: %d of %d stored, %d receipts left, %d of %d files kept\n%s", count("inbox")-before, n, count("receipts"), count("files")-keptBefore, want, w.stderr)
		}
		if stored == 0 && count("inbox") == before+n {
			stored = time.Since(start)
			w.ok(map[string]any{"op": "holdAcks", "on": false})
		}
		if stored != 0 && count("receipts") == 0 && count("files") == keptBefore+want {
			break
		}
		if !measure && stored == 0 && time.Since(start) > 30*time.Second {
			t.Fatalf("%d of %d messages stored while their receipts were held: each waits for the one before it to be acknowledged", count("inbox")-before, n)
		}
	}
	acked := time.Since(start)
	time.Sleep(400 * time.Millisecond) // the page is told once more when arrivals stop
	st := w.ok(map[string]any{"op": "stats", "reset": true, "latency": 0, "writeDelay": 0, "page": ""})
	req := st["req"].(map[string]any)
	shapes := make([]string, 0, len(req))
	total := 0
	for k, v := range req {
		shapes = append(shapes, fmt.Sprintf("%5.0f %s", v.(float64), k))
		total += int(v.(float64))
	}
	sort.Strings(shapes)
	scans := st["scans"].(map[string]any)
	t.Logf("backlog %d (%d files) over %d held: stored in %v, all acknowledged and kept in %v; %d requests (%v receipts at once), %v store reads (%v rows: %v), %v writes, %v page changes, %v page reads, %v pushed\n%s",
		n, want, before, stored.Round(time.Millisecond), acked.Round(time.Millisecond), total, st["acksAtOnce"], st["reads"], st["rows"], scans, st["writes"], st["changed"], st["renders"], st["pushed"], strings.Join(shapes, "\n"))
	// Nothing lost or stored twice; the relay holds nothing more: Alice
	// sees every message delivered.
	if got := count("inbox"); got != before+n {
		t.Fatalf("inbox has %d rows, want %d", got, before+n)
	}
	w.until("Alice sees every message delivered", func() bool {
		rows, e := alice.ConversationMessages(conv)
		if e != nil || len(rows) != len(history)+n {
			return false
		}
		for _, m := range rows[len(history):] {
			if m.Delivery != "delivered" {
				return false
			}
		}
		return true
	})
	if p := int(st["pushed"].(float64)); p != n {
		t.Errorf("the relay pushed %d messages for a backlog of %d", p, n)
	}
	if a := int(st["acksAtOnce"].(float64)); !measure && a < 4 {
		t.Errorf("receipts went one round trip after another (at most %d at once)", a)
	}
	// Files arriving slowly can end one burst and start another. Each quiet
	// gap permits a final notification and the next burst's first one; the
	// engine's burstQuiet is 250 ms. Fast catch-up still checks coalescing.
	changesBudget := n/4 + 2*int(acked/(250*time.Millisecond))
	if c := int(st["changed"].(float64)); c > changesBudget {
		t.Errorf("the page was told of %d changes for %d messages and %d files over %v (budget %d)", c, n, want, acked, changesBudget)
	}
	if rows, _ := scans["inbox"].(float64); page == "" && int(rows) > 5*(before+n) {
		t.Errorf("catching up read %v inbox rows for %d held: every message held was read again per message", rows, before+n)
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

// The same catch-up in a group (MEL-546), as a measurement only
// (AGENTNET_BACKLOG_GROUP=1, with the AGENTNET_BACKLOG_* settings above):
// Alice's group turns wait in the relay while the phone is closed.
func TestBrowserEngineGroupBacklogMeasure(t *testing.T) {
	if os.Getenv("AGENTNET_BACKLOG_GROUP") == "" {
		t.Skip("measurement: set AGENTNET_BACKLOG_GROUP=1")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	n := envInt("AGENTNET_BACKLOG_N", 120)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + h.Addr})
	code, err := alice.Invite(ctx, "dana", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	dana := w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("stream connected", func() bool {
		v := w.ok(map[string]any{"op": "status"})
		return v["connected"] == true && v["members"] == true
	})
	var dm string
	w.until("DM ready", func() bool { // pins both people
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": alice.Address}})
		if v["error"] != nil {
			return false
		}
		dm = v["v"].(map[string]any)["id"].(string)
		return true
	})
	w.api("/api/dm/send", map[string]any{"conv": dm, "body": "hello"})
	w.until("Alice has the DM", func() bool {
		rows, e := alice.ConversationMessages(dm)
		return e == nil && len(rows) == 1
	})
	packet, err := alice.CreateGroup(ctx, "Backlog group")
	if err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	pv, _ := dana["person"].(map[string]any)
	person, _ := pv["person"].(string)
	if person == "" {
		t.Fatalf("no person id: %v", dana)
	}
	inv, err := alice.InviteGroup(ctx, conv, person, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.until("the phone has the invitation", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/groups/invitations"})
		list, _ := v["v"].([]any)
		for _, i := range list {
			if i.(map[string]any)["id"] == inv.ID {
				return true
			}
		}
		return false
	})
	w.api("/api/groups/decide", map[string]any{"id": inv.ID, "accept": true})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if err := alice.PublishGroupInvitation(ctx, inv.ID); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("publish: %v", err)
		}
	}
	if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "welcome"}); err != nil {
		t.Fatal(err)
	}
	count := func(store string) int { return int(w.ok(map[string]any{"op": "count", "store": store})["n"].(float64)) }
	w.until("the phone has the group's first turn", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + conv})
		m, _ := v["v"].(map[string]any)
		ms, _ := m["messages"].([]any)
		return len(ms) >= 1
	})
	if old := envInt("AGENTNET_BACKLOG_HISTORY", 0); old > 0 { // received while the phone was open
		for i := 0; i < old; i++ {
			if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: fmt.Sprintf("earlier %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		w.until("the phone holds the earlier turns", func() bool { return count("inbox") >= old })
	}
	w.ok(map[string]any{"op": "stopStream"})
	before := count("inbox")
	for i := 0; i < n; i++ {
		if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: fmt.Sprintf("group backlog %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	page := ""
	if envInt("AGENTNET_BACKLOG_PAGE", 0) == 1 {
		page = conv
	}
	w.ok(map[string]any{"op": "stats", "reset": true, "latency": envInt("AGENTNET_BACKLOG_RTT", 0), "writeDelay": envInt("AGENTNET_BACKLOG_WRITE", 0), "page": page})
	start := time.Now()
	w.ok(map[string]any{"op": "start"})
	var stored time.Duration
	for deadline := time.Now().Add(8 * time.Minute); ; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("catch-up did not finish: %d of %d\n%s", count("inbox")-before, n, w.stderr)
		}
		if stored == 0 && count("inbox") >= before+n {
			stored = time.Since(start)
		}
		if stored != 0 && count("receipts") == 0 {
			break
		}
	}
	acked := time.Since(start)
	time.Sleep(400 * time.Millisecond)
	st := w.ok(map[string]any{"op": "stats", "reset": true, "latency": 0, "writeDelay": 0, "page": ""})
	req := st["req"].(map[string]any)
	shapes := make([]string, 0, len(req))
	for k, v := range req {
		shapes = append(shapes, fmt.Sprintf("%5.0f %s", v.(float64), k))
	}
	sort.Strings(shapes)
	t.Logf("group backlog %d over %d held: stored in %v, all acknowledged in %v; %v receipts at once, %v store reads (%v rows: %v), %v writes, %v page changes, %v page reads, %v pushed; inbox %d\n%s",
		n, before, stored.Round(time.Millisecond), acked.Round(time.Millisecond), st["acksAtOnce"], st["reads"], st["rows"], st["scans"], st["writes"], st["changed"], st["renders"], st["pushed"], count("inbox"), strings.Join(shapes, "\n"))
}
