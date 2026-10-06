package ui

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBrowserRootSyncLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/rootsync_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("root sync engine: %v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserRootSyncWireMatchesGo(t *testing.T) {
	w := startWireNode(t)
	setup := w.ok(map[string]any{"op": "setup", "address": "alice/phone"})
	var phone identity.Public
	if err := json.Unmarshal([]byte(setup["public"].(string)), &phone); err != nil {
		t.Fatal(err)
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	desk := id.Public("alice/desk")
	person, peer := protocol.NewID(), protocol.NewID()
	hash := strings.Repeat("a", 64)
	root := protocol.ConvRoot{V: 2, Kind: protocol.ConvKindDM, Creator: protocol.ConvCreator{Person: person, Roster: hash, Address: desk.Address, Fingerprint: desk.Fingerprint()}, Members: []protocol.ConvMember{{Person: person, Roster: hash}, {Person: peer, Roster: hash}}, Nonce: protocol.NewID(), Created: time.Now().Unix()}
	slices.SortFunc(root.Members, func(a, b protocol.ConvMember) int { return strings.Compare(a.Person, b.Person) })
	root.Sign(id.Sign)
	raw, _ := json.Marshal(root)
	in := envelope.Inner{V: 2, ID: protocol.NewID(), From: desk.Address, To: phone.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: root.ID(), LID: protocol.NewID(), Root: raw, Sub: envelope.SubRootSync, Replica: true, Body: `{"v":1}`}
	recipient, _ := phone.Recipient()
	env, err := envelope.Seal(in, id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(env)
	got := w.ok(map[string]any{"op": "open", "envelope": string(encoded), "from": publicJSON(t, desk)})["inner"].(map[string]any)
	if got["sub"] != envelope.SubRootSync || got["conv"] != root.ID() || got["replica"] != true {
		t.Fatal("native/browser root fields differ")
	}
	m := map[string]any{"v": 2, "id": protocol.NewID(), "to": desk.Address, "ts": time.Now().Unix(), "kind": "message", "conv": root.ID(), "lid": protocol.NewID(), "root": string(raw), "sub": envelope.SubRootSync, "replica": true, "body": `{"v":1}`}
	js := w.ok(map[string]any{"op": "seal", "message": m, "to": publicJSON(t, desk)})["envelope"].(string)
	var reverse envelope.Envelope
	if err = json.Unmarshal([]byte(js), &reverse); err != nil {
		t.Fatal(err)
	}
	opened, err := envelope.Open(reverse, id, desk.Address, phone)
	if err != nil || opened.Conv != root.ID() || opened.Sub != envelope.SubRootSync || reverse.Attn {
		t.Fatalf("browser/native root mismatch: %v", err)
	}
	channel := strings.Repeat("a", 64)
	if _, err = envelope.SealAttention(in, id.Sign, recipient, channel); err == nil {
		t.Fatal("native attention allowed")
	}
	for name, value := range map[string]any{"replica": false, "body": "visible fake turn", "kind": "task", "reply_to": protocol.NewID(), "conv": strings.Repeat("b", 64), "chan": channel, "pid": protocol.NewID(), "quote": protocol.NewID()} {
		bad := maps.Clone(m)
		bad[name] = value
		if result := w.call(map[string]any{"op": "seal", "message": bad, "to": publicJSON(t, desk)}); result["error"] == nil {
			t.Errorf("browser accepted forbidden root field %s", name)
		}
	}
	if slices.Contains(protocol.RoomImplies, protocol.CapRootSync) {
		t.Fatal("root sync must require explicit capability")
	}
}
