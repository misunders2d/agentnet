package ui

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func startAgentWireNode(t *testing.T) *wireNode {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command(node, "testdata/agent_wire_check.mjs")
	w := &wireNode{t: t, stderr: &bytes.Buffer{}}
	cmd.Stderr = w.stderr
	w.in, err = cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.in.Close(); cmd.Wait() })
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 4096), 8<<20)
	return w
}

func TestBrowserNamedAgentWireMatchesGo(t *testing.T) {
	w := startAgentWireNode(t)
	setup := w.ok(map[string]any{"op": "setup", "address": "browser/phone"})
	if setup["extractable"] != false {
		t.Fatal("browser keys became extractable")
	}
	var browser identity.Public
	if err := json.Unmarshal([]byte(setup["public"].(string)), &browser); err != nil || browser.Verify() != nil {
		t.Fatal(err)
	}
	// Public test-only identity from the original protocol/person_test.go fixture.
	box, err := age.ParseX25519Identity("AGE-SECRET-KEY-1UDEAJGR0KMACNTZ9YKJ9UHGFW6M42W574X7VX8YUE57M043804KSTWTDSN")
	if err != nil {
		t.Fatal(err)
	}
	host := &identity.Identity{Sign: ed25519.NewKeyFromSeed(make([]byte, 32)), Box: box}
	pub := host.Public("vitalii/desk")
	const id = "00112233445566778899aabbccddeeff"
	record := protocol.AgentRecord{V: 1, ID: id, Host: pub.Address, HostKey: pub.Fingerprint(), Label: "Builder <&> Ю", TS: 1700000000}
	record.Sign(host.Sign)
	// Original agentidentity_test.go canonical/hash/signature consumed by browser.
	const hash = "cf26d8a25764b83d020a1bd0eeed51d2ab206078b071f905ab500215ee1a5d5e"
	const sig = "c4f696037567b7b71bd8e370b799dcedac813d59d4b8e36b6e0bd0bc9bdae14b842c1d816edb923a2d85dc4c56ef77148989f194cc8570ec33e7e1c938b4060b"
	if record.Hash() != hash || hex.EncodeToString(record.Sig) != sig {
		t.Fatal("original named-agent vector changed")
	}
	v := w.ok(map[string]any{"op": "agent", "json": marshal(t, record), "host": marshal(t, pub)})
	if v["canonical"] != string(record.Canonical()) || v["hash"] != hash || v["json"] != marshal(t, record) {
		t.Fatal("agent canonical differs")
	}
	v = w.ok(map[string]any{"op": "sign-agent", "fields": map[string]any{"id": id, "label": "Browser <&> Ю", "ts": 1700000000}})
	back, err := protocol.ParseAgentRecord([]byte(v["json"].(string)))
	if err != nil || back.Verify(browser) != nil {
		t.Fatal("Go refuses browser agent record", err)
	}
	for name, mutate := range map[string]func(*protocol.AgentRecord){
		"foreign host":  func(r *protocol.AgentRecord) { r.Host = browser.Address },
		"foreign key":   func(r *protocol.AgentRecord) { r.HostKey = browser.Fingerprint() },
		"invalid ID":    func(r *protocol.AgentRecord) { r.ID = "program-name" },
		"label control": func(r *protocol.AgentRecord) { r.Label = "label\ncontrol" },
	} {
		r := record
		mutate(&r)
		r.Sign(host.Sign)
		w.refuses(name, w.call(map[string]any{"op": "agent", "json": marshal(t, r), "host": marshal(t, pub)}), "")
	}
	w.refuses("program on wire", w.call(map[string]any{"op": "agent", "json": strings.TrimSuffix(marshal(t, record), "}") + `,"program":"claude"}`, "host": marshal(t, pub)}), "")

	conv := strings.Repeat("a", 64)
	event := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: "invite", Author: protocol.EventAuthor{Person: protocol.NewID(), Roster: strings.Repeat("b", 64), Address: pub.Address, Fingerprint: pub.Fingerprint()}, TS: 1700000000,
		Host: &protocol.ParticipationHost{Person: protocol.NewID(), Address: browser.Address, Fingerprint: browser.Fingerprint(), AgentID: id}, Audience: "conversation"}
	for _, named := range []bool{false, true} {
		if named {
			event.Host.AgentID = id
		} else {
			event.Host.AgentID = ""
		}
		event.Sign(host.Sign)
		v = w.ok(map[string]any{"op": "event", "json": marshal(t, event), "key": b64(pub.SignKey)})
		if v["json"] != marshal(t, event) || v["hash"] != event.Hash() {
			t.Fatal("participation bytes differ")
		}
	}
	v = w.ok(map[string]any{"op": "sign-event", "fields": event})
	browserEvent, err := protocol.ParseParticipationEvent([]byte(v["json"].(string)))
	if err != nil || browserEvent.Verify(browser.SignKey) != nil || browserEvent.Host.AgentID != id {
		t.Fatal("Go refuses browser named participation", err)
	}
	rc, _ := browser.Recipient()
	for _, version := range []int{1, 2} {
		for _, kind := range []string{"question", "task", "answer", "result", "message"} {
			in := envelope.Inner{V: version, ID: protocol.NewID(), From: pub.Address, To: browser.Address, TS: 1700000000, Kind: kind, Body: "text <&> Ю"}
			if version == 2 {
				in.Conv = conv
				in.LID = protocol.NewID()
				in.Root = json.RawMessage(`{"v":2}`)
			}
			if kind == "question" || kind == "task" {
				in.Target = &envelope.Target{Address: browser.Address, Fingerprint: browser.Fingerprint(), AgentID: id}
			}
			if kind == "answer" || kind == "result" {
				in.AgentID = id
				in.ReplyTo = protocol.NewID()
			}
			sealed, err := envelope.Seal(in, host.Sign, rc)
			if err != nil {
				t.Fatal(err)
			}
			v = w.ok(map[string]any{"op": "open", "json": marshal(t, sealed), "from": marshal(t, pub)})
			if v["raw"] != marshal(t, in) {
				t.Fatal("encrypted inner bytes differ", version, kind)
			}
			in.From = browser.Address
			in.To = pub.Address
			if in.Target != nil {
				in.Target.Address = pub.Address
				in.Target.Fingerprint = pub.Fingerprint()
			}
			v = w.ok(map[string]any{"op": "seal", "inner": in, "to": marshal(t, pub)})
			var env envelope.Envelope
			if err = json.Unmarshal([]byte(v["json"].(string)), &env); err != nil {
				t.Fatal(err)
			}
			opened, err := envelope.Open(env, host, pub.Address, browser)
			if err != nil || marshal(t, opened) != marshal(t, in) {
				t.Fatal("Go cannot open browser named envelope", err)
			}
			reader, err := age.Decrypt(bytes.NewReader(env.CT), host.Box)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := io.ReadAll(reader)
			if err != nil || string(plain) != marshal(t, in) {
				t.Fatal("browser inner bytes differ from Go", err, version, kind)
			}
		}
	}
	for _, named := range []bool{false, true} {
		h := client.HistoryItem{V: 1, From: pub.Address, FromKey: pub.Fingerprint(), ID: protocol.NewID(), LID: protocol.NewID(), TS: 1700000000, Kind: "answer", Body: "historical", ReplyTo: protocol.NewID(), PID: event.PID, At: 1700000000123}
		if named {
			h.AgentID = id
		}
		v = w.ok(map[string]any{"op": "history", "json": marshal(t, h)})
		if v["json"] != marshal(t, h) {
			t.Fatal("history bytes differ")
		}
		want := ""
		if named {
			want = protocol.CapAgentIdentity
		}
		if v["requirement"] != want {
			t.Fatal("history capability differs")
		}
	}
}

func TestBrowserNamedAgentEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/agent_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "named-agent engine checks passed") {
		t.Fatalf("%v\n%s", err, out)
	}
}
