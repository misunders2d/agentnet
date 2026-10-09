package ui

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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

func startExternalWireNode(t *testing.T) *wireNode {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	cmd := exec.Command(node, "testdata/external_agent_wire_check.mjs")
	w := &wireNode{t: t, stderr: &nodeOutput{}}
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

func TestBrowserExternalAgentWireMatchesGo(t *testing.T) {
	w := startExternalWireNode(t)
	setup := w.ok(map[string]any{"op": "setup", "address": "charlie/browser"})
	var browser identity.Public
	if err := json.Unmarshal([]byte(setup["public"].(string)), &browser); err != nil || browser.Verify() != nil {
		t.Fatal(err)
	}
	alice, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := alice.Public("alice/desk")
	recipient, _ := browser.Recipient()
	pid, originalPID, lid := protocol.NewID(), protocol.NewID(), protocol.NewID()
	text := []byte("selected bytes <&> Ю")
	sum := sha256.Sum256(text)
	h := client.HistoryItem{V: 1, From: pub.Address, FromKey: pub.Fingerprint(), ID: protocol.NewID(), LID: lid, TS: 1700000000, At: 1700000000123, Kind: envelope.KindQuestion, Body: "selected <&> Ю", PID: originalPID, Attachments: []envelope.Attachment{{Name: "selected.txt", Size: int64(len(text)), SHA256: hex.EncodeToString(sum[:])}}}
	carrierHash := sha256.Sum256([]byte(pid + "\x00" + lid + "\x00" + pub.Fingerprint()))
	carrierLID := hex.EncodeToString(carrierHash[:16])
	in := envelope.Inner{V: 2, ID: protocol.NewID(), From: pub.Address, To: browser.Address, TS: 1700000000, Kind: envelope.KindMessage, Body: marshal(t, h), Conv: strings.Repeat("a", 64), Root: json.RawMessage(`{"v":2}`), LID: carrierLID, Sub: envelope.SubExcerpt, Replica: true, PID: pid}
	info := map[string]any{"grant": []protocol.GrantRef{{LID: lid, Fingerprint: pub.Fingerprint()}}}
	got := w.ok(map[string]any{"op": "excerpt", "inner": in, "info": info})
	if got["json"] != marshal(t, h) || got["lid"] != carrierLID || got["requirement"] != protocol.CapExternalParticipation || got["history_requirement"] != protocol.CapExternalParticipation {
		t.Fatal("native/browser excerpt bytes or cap differ", got)
	}
	// Actual Go age encryption and signed envelope accepted by browser.
	sealed, err := envelope.Seal(in, alice.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	opened := w.ok(map[string]any{"op": "open", "json": marshal(t, sealed), "from": marshal(t, pub)})
	if opened["raw"] != marshal(t, in) {
		t.Fatal("Go excerpt inner differs")
	}
	// Browser signed/age carrier accepted by native implementation.
	in.From = browser.Address
	in.To = pub.Address
	back := w.ok(map[string]any{"op": "seal", "inner": in, "to": marshal(t, pub)})
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(back["json"].(string)), &env); err != nil {
		t.Fatal(err)
	}
	native, err := envelope.Open(env, alice, pub.Address, browser)
	if err != nil || marshal(t, native) != marshal(t, in) {
		t.Fatal("native rejected browser excerpt", err)
	}
	// Selected file bytes cross actual age implementations in both directions.
	file := w.ok(map[string]any{"op": "encrypt-file", "bytes": base64.StdEncoding.EncodeToString(text), "name": "selected.txt", "to": marshal(t, pub)})
	ct, err := base64.StdEncoding.DecodeString(file["ct"].(string))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := age.Decrypt(bytes.NewReader(ct), alice.Box)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(plain, text) {
		t.Fatal("native file bytes differ", err)
	}
	var nativeCT bytes.Buffer
	enc, err := age.Encrypt(&nativeCT, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enc.Write(text); err != nil {
		t.Fatal(err)
	}
	if err = enc.Close(); err != nil {
		t.Fatal(err)
	}
	ctSum := sha256.Sum256(nativeCT.Bytes())
	att := envelope.Attachment{Name: "selected.txt", Size: int64(len(text)), SHA256: hex.EncodeToString(sum[:]), Blob: envelope.Blob{ID: protocol.NewID(), Size: int64(nativeCT.Len()), SHA256: hex.EncodeToString(ctSum[:])}}
	decrypted := w.ok(map[string]any{"op": "decrypt-file", "ct": base64.StdEncoding.EncodeToString(nativeCT.Bytes()), "attachment": att})
	if decrypted["bytes"] != base64.StdEncoding.EncodeToString(text) {
		t.Fatal("browser file bytes differ")
	}
	in.Attachments = []envelope.Attachment{att}
	w.ok(map[string]any{"op": "excerpt", "inner": in, "info": info})
	for name, change := range map[string]func(*client.HistoryItem){
		"unselected LID": func(x *client.HistoryItem) { x.LID = protocol.NewID() },
		"unselected key": func(x *client.HistoryItem) { x.FromKey = browser.Fingerprint() },
		"control body":   func(x *client.HistoryItem) { x.Sub = envelope.SubEvent },
		"manifest blob":  func(x *client.HistoryItem) { x.Attachments = []envelope.Attachment{att} },
	} {
		bad := h
		change(&bad)
		copy := in
		copy.Body = marshal(t, bad)
		w.refuses(name, w.call(map[string]any{"op": "excerpt", "inner": copy, "info": info}), "")
	}
	wrong := att
	wrong.Name = "outside.txt"
	in.Attachments = []envelope.Attachment{wrong}
	w.refuses("unselected file", w.call(map[string]any{"op": "excerpt", "inner": in, "info": info}), "")
	// Signed external invite/host decisions reuse native event canonical form.
	ev := protocol.ParticipationEvent{V: 1, Conv: in.Conv, PID: pid, Type: protocol.EventInvite, TS: 1700000000, Author: protocol.EventAuthor{Person: protocol.NewID(), Roster: strings.Repeat("b", 64), Address: pub.Address, Fingerprint: pub.Fingerprint()}, Host: &protocol.ParticipationHost{Person: protocol.NewID(), Address: browser.Address, Fingerprint: browser.Fingerprint(), AgentID: protocol.NewID()}, Audience: protocol.AudienceConversation, Grant: []protocol.GrantRef{{LID: lid, Fingerprint: pub.Fingerprint()}}}
	ev.Sign(alice.Sign)
	event := w.ok(map[string]any{"op": "event", "json": marshal(t, ev), "key": base64.StdEncoding.EncodeToString(pub.SignKey)})
	if event["json"] != marshal(t, ev) || event["hash"] != ev.Hash() {
		t.Fatal("external event canonical differs")
	}
}

func TestBrowserExternalAgentEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/external_agent_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "external-agent engine checks passed") {
		t.Fatalf("%v\n%s", err, out)
	}
}
