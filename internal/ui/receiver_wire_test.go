package ui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func startReceiverWireNode(t *testing.T) *wireNode {
	t.Helper()
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("node unavailable")
	}
	cmd := exec.Command(node, "testdata/receiver_wire_check.mjs")
	w := &wireNode{t: t, stderr: &bytes.Buffer{}}
	cmd.Stderr = w.stderr
	w.in, e = cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { w.in.Close(); cmd.Wait() })
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 4096), 8<<20)
	return w
}
func TestBrowserReceiverWireMatchesGo(t *testing.T) {
	w := startReceiverWireNode(t)
	setup := w.ok(map[string]any{"op": "setup", "address": "alice/browser"})
	var browser identity.Public
	if json.Unmarshal([]byte(setup["public"].(string)), &browser) != nil || browser.Verify() != nil {
		t.Fatal("browser public invalid")
	}
	var caps protocol.CapsRecord
	if json.Unmarshal([]byte(setup["caps_json"].(string)), &caps) != nil || caps.Verify(browser.SignKey) != nil {
		t.Fatal("normal Engine capability record invalid")
	}
	count := 0
	for _, c := range caps.Caps {
		if c == protocol.CapReplyReceiver {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("normal Engine must advertise rcv1 exactly once: %v", caps.Caps)
	}
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	ap, bp := alice.Public("alice/phone"), bob.Public("bob/desk")
	request := envelope.ReceiverRequest{ID: strings.Repeat("1", 32), From: ap.Address, FromKey: ap.Fingerprint(), To: bp.Address, ToKey: bp.Fingerprint(), TS: 1700000000, Kind: envelope.KindQuestion, Body: "Original <&> Ю \u2028\u2029", Attachments: []envelope.Attachment{{Name: "selected.txt", Size: 4, SHA256: strings.Repeat("a", 64)}, {Name: "second.txt", Size: 4, SHA256: strings.Repeat("a", 64)}}}
	choice := envelope.ReceiverChoice{Kind: "live_session", SessionHandle: "selected-Codex", OnClose: &envelope.ReceiverOnClose{AgentID: strings.Repeat("a", 32), Instructions: "Exact <&> local continuation", Mode: envelope.KindQuestion}}
	route := envelope.ReceiverRoute{Op: "delegate", Host: browser.Address, HostKey: browser.Fingerprint(), RequestRef: request.ID, DelegationID: strings.Repeat("2", 32)}
	route.RequestDigest, _ = envelope.ReceiverDigest(route, request, choice)
	record := func(q envelope.ReceiverRequest, c envelope.ReceiverChoice, r envelope.ReceiverRoute) {
		t.Helper()
		got := w.ok(map[string]any{"op": "record", "route": r, "request": marshal(t, q), "choice": c})
		digest, e := envelope.ReceiverDigest(r, q, c)
		if e != nil {
			t.Fatal(e)
		}
		if got["route"] != marshal(t, r) || got["request"] != marshal(t, q) || got["choice"] != marshal(t, c) || got["digest"] != digest {
			t.Fatalf("Go/JS canonical or digest mismatch: %+v", got)
		}
	}
	record(request, choice, route)
	for _, choiceKind := range []string{"human", "managed_agent"} {
		c := envelope.ReceiverChoice{Kind: choiceKind}
		if choiceKind == "managed_agent" {
			c.AgentID, c.Instructions, c.Mode = strings.Repeat("c", 32), "Exact local request", envelope.KindTask
		}
		record(request, c, route)
	}
	// Frozen signed roots retain original signature and Go HTML escaping.
	for _, version := range []int{2, 3} {
		root := protocol.ConvRoot{V: version, Kind: "dm", Creator: protocol.ConvCreator{Person: strings.Repeat("1", 32), Roster: strings.Repeat("a", 64), Address: ap.Address, Fingerprint: ap.Fingerprint()}, Members: []protocol.ConvMember{{Person: strings.Repeat("1", 32), Roster: strings.Repeat("a", 64)}, {Person: strings.Repeat("2", 32), Roster: strings.Repeat("b", 64)}}, Nonce: strings.Repeat("3", 32), Created: request.TS}
		q := request
		q.LID, q.To, q.ToKey = q.ID, "", ""
		if version == 3 {
			root.Kind, root.Realm, root.Title, root.Admins = "group", strings.Repeat("4", 32), "Group <&> Ю", []string{root.Creator.Person}
			q.GroupAdmission = strings.Repeat("5", 64)
			q.GroupReplies = []envelope.ReceiverReplyKey{{Key: ap.Fingerprint(), Admission: q.GroupAdmission}}
		}
		root.Sign(alice.Sign)
		q.Conv, q.Root = root.ID(), json.RawMessage(marshal(t, root))
		record(q, choice, route)
		badRoot := strings.Replace(marshal(t, q), `"root":{"v":`, `"root":{ "v":`, 1)
		if w.call(map[string]any{"op": "record", "route": route, "request": badRoot, "choice": choice})["error"] == nil {
			t.Fatal("JS accepted alternate signed root bytes")
		}
	}
	for _, op := range []string{"request", "ready"} {
		r := route
		r.Op = op
		record(request, choice, r)
	}
	files := append([]envelope.Attachment(nil), request.Attachments...)
	for i := range files {
		files[i].Blob = envelope.Blob{ID: strings.Repeat(string(rune('3'+i)), 32), Size: 200, SHA256: strings.Repeat("b", 64)}
	}
	operation := envelope.ReceiverOperation{V: 1, Request: &request, Receiver: &choice}
	body := marshal(t, operation)
	got := w.ok(map[string]any{"op": "operation", "route": route, "body": body, "attachments": files})
	if got["json"] != body {
		t.Fatal("setup canonical differs")
	}
	recipient, _ := browser.Recipient()
	in := envelope.Inner{V: 1, ID: route.DelegationID, From: ap.Address, To: browser.Address, TS: 1700000000, Kind: envelope.KindTask, Body: body, Attachments: files, ReceiverRoute: &route}
	env, e := envelope.Seal(in, alice.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	got = w.ok(map[string]any{"op": "open", "json": marshal(t, env), "from": marshal(t, ap)})
	var openedRoute envelope.ReceiverRoute
	if json.Unmarshal([]byte(marshal(t, got["route"])), &openedRoute) != nil || got["body"] != body || openedRoute != route {
		t.Fatal("JS did not open Go delegation")
	}
	// JS -> Go uses the same frozen request, explicitly authored by browser.
	request.From, request.FromKey = browser.Address, browser.Fingerprint()
	route.Host, route.HostKey = ap.Address, ap.Fingerprint()
	route.RequestDigest, e = envelope.ReceiverDigest(route, request, choice)
	if e != nil {
		t.Fatal(e)
	}
	body = marshal(t, envelope.ReceiverOperation{V: 1, Request: &request, Receiver: &choice})
	in.From, in.To, in.Body, in.ReceiverRoute = browser.Address, ap.Address, body, &route
	got = w.ok(map[string]any{"op": "seal", "inner": in, "to": marshal(t, ap)})
	var jsEnv envelope.Envelope
	if json.Unmarshal([]byte(got["json"].(string)), &jsEnv) != nil {
		t.Fatal("JS envelope decode")
	}
	opened, e := envelope.Open(jsEnv, alice, ap.Address, browser)
	if e != nil || opened.ReceiverRoute == nil || *opened.ReceiverRoute != route || opened.Body != body {
		t.Fatalf("Go did not open JS delegation: %v", e)
	}
	// Every malformed setup is signed/encrypted by a real sender, then refused by JS.
	wrong := in
	wrong.ID = strings.Repeat("9", 32)
	bad := w.call(map[string]any{"op": "seal", "inner": wrong, "to": marshal(t, ap)})
	if bad["error"] == nil {
		t.Fatal("JS accepted delegate ID mismatch")
	}
	ready := route
	ready.Op = "ready"
	bad = w.call(map[string]any{"op": "operation", "route": ready, "body": `{"v":1}`, "replyTo": strings.Repeat("9", 32)})
	if bad["error"] == nil {
		t.Fatal("JS accepted wrong ready reply")
	}
	for name, b := range map[string]string{"unknown": strings.Replace(body, `"v":1`, `"v":1,"owner_token":"forbidden"`, 1), "trailing": body + ` {}`, "oversize": strings.Repeat(" ", envelope.MaxReceiverSetup+1), "changed-body": strings.Replace(body, "Original", "Forged", 1)} {
		t.Run(name, func(t *testing.T) {
			if w.call(map[string]any{"op": "operation", "route": route, "body": b, "attachments": files})["error"] == nil {
				t.Fatal("JS accepted malformed setup")
			}
		})
	}
	reversed := []envelope.Attachment{files[1], files[0]}
	if w.call(map[string]any{"op": "operation", "route": route, "body": body, "attachments": reversed})["error"] == nil {
		t.Fatal("JS accepted same-SHA wrong file index")
	}
	// History encodes only inert original route; exact Go DTO field order.
	historyRoute := route
	historyRoute.Op = "request"
	h := client.HistoryItem{V: 1, From: request.From, FromKey: request.FromKey, ID: request.ID, LID: request.ID, TS: request.TS, Kind: request.Kind, Body: request.Body, At: request.TS, ReceiverRoute: &historyRoute}
	got = w.ok(map[string]any{"op": "history", "json": marshal(t, h)})
	if got["json"] != marshal(t, h) {
		t.Fatalf("history route bytes differ: %s != %s", got["json"], marshal(t, h))
	}
	legacy := h
	legacy.ReceiverRoute = nil
	got = w.ok(map[string]any{"op": "history", "json": marshal(t, legacy)})
	if got["json"] != marshal(t, legacy) {
		t.Fatal("legacy history bytes changed")
	}
}

func TestBrowserReceiverRouteProjection(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	output, err := exec.Command(node, "testdata/receiver_route_projection_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("receiver projection: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestBrowserReceiverPreparedEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	output, err := exec.Command(node, "testdata/receiver_prepared_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("receiver prepared Engine: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// Optional collected vectors originate in real native encrypted outbox/Ready
// runtime tests. The exported opened DTOs prove codec fit, not JS execution.
func TestBrowserReceiverNativeRuntimeVectors(t *testing.T) {
	dir := os.Getenv("AGENTNET_RECEIVER_RUNTIME_VECTORS")
	if dir == "" {
		t.Skip("collected native receiver runtime vectors not supplied")
	}
	w := startReceiverWireNode(t)
	for _, name := range []string{"native-TestReceiverRemoteManagedContinuationAfterPhoneStops.json", "native-TestReceiverRemoteNativeClaimRestartReceipt.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var vector struct{ Original, Delegate, Ready envelope.Inner }
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		for _, in := range []envelope.Inner{vector.Original, vector.Delegate, vector.Ready} {
			if err = envelope.ValidateReceiverRoute(in); err != nil {
				t.Fatalf("%s native route: %v", name, err)
			}
		}
		op, err := envelope.ParseReceiverOperation([]byte(vector.Delegate.Body), *vector.Delegate.ReceiverRoute, "", vector.Delegate.Attachments)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := envelope.ReceiverDigest(*vector.Delegate.ReceiverRoute, *op.Request, *op.Receiver)
		if err != nil {
			t.Fatal(err)
		}
		got := w.ok(map[string]any{"op": "native-vector", "original": vector.Original, "delegate": vector.Delegate, "ready": vector.Ready})
		if got["delegate_body"] != vector.Delegate.Body || got["ready_body"] != vector.Ready.Body || got["digest"] != digest || digest != vector.Delegate.ReceiverRoute.RequestDigest {
			t.Fatalf("%s native→JS snapshot/Ready mismatch", name)
		}
	}
}
