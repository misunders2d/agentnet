package ui

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func startTypingWireNode(t *testing.T) *wireNode {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command(node, "testdata/typing_wire_check.mjs")
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
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.in.Close(); cmd.Wait() })
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 4096), 1<<20)
	return w
}

func TestBrowserTypingWireMatchesGo(t *testing.T) {
	w := startTypingWireNode(t)
	setup := w.ok(map[string]any{"op": "setup", "address": "browser/phone"})
	if setup["extractable"] != false {
		t.Fatal("typing device keys became extractable")
	}
	var browser identity.Public
	if err := json.Unmarshal([]byte(setup["public"].(string)), &browser); err != nil || browser.Verify() != nil {
		t.Fatal("invalid browser identity", err)
	}
	from, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	from.Sign = ed25519.NewKeyFromSeed(make([]byte, 32))
	fromPub := from.Public("vitalii/desk")
	const epoch int64 = 1790000000123
	now := time.UnixMilli(epoch)
	const id = "00112233445566778899aabbccddeeff"
	const session = "ffeeddccbbaa99887766554433221100"
	const realm = "11223344556677889900aabbccddeeff"
	vector := protocol.Signal{V: 1, ID: id, From: fromPub.Address, To: "bob/phone", TS: epoch, Session: session, CT: []byte{1, 2, 3}}
	vector.Sig = ed25519.Sign(from.Sign, vector.Canonical())
	// Original protocol/signal_test.go canonical/signature fixture, fed to the browser.
	const hash = "1559387675f7d85dc16f15940635983f54c847d45594a94dfad5ed19ecc95f4e"
	const sig = "223fa5e693c6e656b3e0af7fc18ce090222a8308eac31e83289323751a0faad371667938df076f8031d1aacf014515f4ae1b2e0add58843c20d5f03eccb7a605"
	goHash := sha256.Sum256(vector.Canonical())
	if hex.EncodeToString(goHash[:]) != hash || hex.EncodeToString(vector.Sig) != sig {
		t.Fatal("original Go typing fixture changed")
	}
	got := w.ok(map[string]any{"op": "verify", "signal": marshal(t, vector), "key": base64.StdEncoding.EncodeToString(fromPub.SignKey), "now": epoch})
	if got["canonical"] != string(vector.Canonical()) || got["hash"] != hash || got["json"] != marshal(t, vector) {
		t.Fatal("browser canonical bytes differ from Go")
	}

	for _, scope := range []struct{ conv, thread string }{{strings.Repeat("a", 64), ""}, {"", id}} {
		plain := protocol.TypingPlain{V: 1, ID: id, From: fromPub.Address, To: browser.Address, TS: epoch, Session: session, Realm: realm, Conv: scope.conv, Thread: scope.thread, Origin: "human", Active: true}
		s, err := protocol.SealTyping(plain, from.Sign, browser)
		if err != nil {
			t.Fatal(err)
		}
		v := w.ok(map[string]any{"op": "open", "signal": marshal(t, s), "from": publicJSON(t, fromPub), "realm": realm, "now": epoch})
		var opened protocol.TypingPlain
		raw, _ := json.Marshal(v["plain"])
		if err := json.Unmarshal(raw, &opened); err != nil || opened != plain {
			t.Fatal("browser cannot open Go typing", err, opened)
		}
		back := plain
		back.From = browser.Address
		back.To = fromPub.Address
		back.Active = false
		v = w.ok(map[string]any{"op": "seal", "plain": back, "to": publicJSON(t, fromPub)})
		bs, err := protocol.ParseSignal([]byte(v["signal"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		opened, err = protocol.OpenTyping(bs, from, fromPub.Address, browser, realm, now)
		if err != nil || opened != back {
			t.Fatal("Go cannot open browser stop", err, opened)
		}
		if strings.Contains(v["signal"].(string), realm) || strings.Contains(v["signal"].(string), "human") || strings.Contains(v["signal"].(string), "active") {
			t.Fatal("typing plaintext leaked to relay")
		}

		for name, change := range map[string]func(protocol.Signal) protocol.Signal{
			"wrong key": func(x protocol.Signal) protocol.Signal { x.Sig = ed25519.Sign(browserKey(t), x.Canonical()); return x },
			"rebound session": func(x protocol.Signal) protocol.Signal {
				x.Session = protocol.NewID()
				x.Sig = ed25519.Sign(from.Sign, x.Canonical())
				return x
			},
			"wrong destination": func(x protocol.Signal) protocol.Signal {
				x.To = "other/desk"
				x.Sig = ed25519.Sign(from.Sign, x.Canonical())
				return x
			},
		} {
			x := change(s)
			w.refuses(name, w.call(map[string]any{"op": "open", "signal": marshal(t, x), "from": publicJSON(t, fromPub), "realm": realm, "now": epoch}), "")
		}
		for name, r := range map[string]map[string]any{
			"foreign realm": {"op": "open", "signal": marshal(t, s), "from": publicJSON(t, fromPub), "realm": protocol.NewID(), "now": epoch},
			"expired":       {"op": "open", "signal": marshal(t, s), "from": publicJSON(t, fromPub), "realm": realm, "now": epoch + 5000},
			"future":        {"op": "open", "signal": marshal(t, s), "from": publicJSON(t, fromPub), "realm": realm, "now": epoch - 1001},
			"unknown field": {"op": "open", "signal": strings.TrimSuffix(marshal(t, s), "}") + `,"body":"draft"}`, "from": publicJSON(t, fromPub), "realm": realm, "now": epoch},
		} {
			w.refuses(name, w.call(r), "")
		}
		// Encrypt malformed plaintext with real age and sign it with the correct key.
		rc, _ := browser.Recipient()
		for name, mutate := range map[string]func(*protocol.TypingPlain){
			"spoofed origin":            func(p *protocol.TypingPlain) { p.Origin = "agent:codex" },
			"both scopes":               func(p *protocol.TypingPlain) { p.Conv = strings.Repeat("a", 64); p.Thread = id },
			"encrypted header mismatch": func(p *protocol.TypingPlain) { p.ID = protocol.NewID() },
		} {
			bad := plain
			mutate(&bad)
			crafted := s
			crafted.CT = encryptTo(t, marshalBytes(t, bad), rc)
			crafted.Sig = ed25519.Sign(from.Sign, crafted.Canonical())
			w.refuses(name, w.call(map[string]any{"op": "open", "signal": marshal(t, crafted), "from": publicJSON(t, fromPub), "realm": realm, "now": epoch}), "")
		}
	}
}

func browserKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestBrowserTypingEngineAdmissionAndSend(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/typing_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "typing engine checks passed") {
		t.Fatalf("%s", out)
	}
}
