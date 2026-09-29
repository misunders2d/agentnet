package ui

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// engineNode is static/engine.mjs running in node (testdata/engine_check.mjs).
type engineNode struct{ wireNode }

func startEngineNode(t *testing.T, hubDir string) *engineNode {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command(node, "testdata/engine_check.mjs")
	cmd.Env = append(os.Environ(), "NODE_EXTRA_CA_CERTS="+filepath.Join(hubDir, "tls.crt")) // the Hub's own certificate, pinned
	w := &engineNode{wireNode{t: t, stderr: &bytes.Buffer{}}}
	cmd.Stderr = w.stderr
	if w.in, err = cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.in.Close(); cmd.Process.Kill(); cmd.Wait() })
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 0, 1<<20), 8<<20)
	return w
}

// api asks the engine what the page would ask it.
func (w *engineNode) api(path string, body any) map[string]any {
	w.t.Helper()
	req := map[string]any{"op": "api", "path": path}
	if body != nil {
		req["body"] = body
	}
	v := w.ok(req)
	m, _ := v["v"].(map[string]any)
	return m
}

func (w *engineNode) until(what string, cond func() bool) {
	w.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			w.t.Fatalf("timed out waiting for %s\n%s", what, w.stderr)
		}
	}
}

// rawAgent is an enrolled device driven directly through the wire, so a
// test can send what a correct client would not (a repeated logical id, a
// changed person, a root it did not make).
type rawAgent struct {
	t    *testing.T
	id   *identity.Identity
	addr string
	hub  string
	http *http.Client
}

func newRawAgent(t *testing.T, code, name string) *rawAgent {
	t.Helper()
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(inv.CertPEM))
	id, _ := identity.Generate()
	r := &rawAgent{t: t, id: id, addr: protocol.Address(inv.Label, name), hub: inv.Hub,
		http: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 10 * time.Second}}
	jr := protocol.JoinRequest{Secret: inv.Secret, Public: id.Public(r.addr)}
	protocol.SignJoin(&jr, id.Sign)
	if code, body := r.do("POST", "/v1/join", marshalBytes(t, jr), false); code != 200 && code != 201 && code != 204 {
		t.Fatalf("join %s: %d %s", r.addr, code, body)
	}
	return r
}

func marshalBytes(t *testing.T, v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (r *rawAgent) do(method, path string, body []byte, signed bool) (int, []byte) {
	r.t.Helper()
	req, _ := http.NewRequest(method, r.hub+path, bytes.NewReader(body))
	if signed {
		protocol.SignRequest(req, r.addr, r.id.Sign, body)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (r *rawAgent) roster(label string) protocol.PersonRoster {
	p := protocol.PersonRoster{Person: protocol.NewID(), Label: label,
		Devices: []protocol.RosterDevice{{Address: r.addr, Fingerprint: r.id.Public(r.addr).Fingerprint()}}}
	p.Sign(r.id.Sign)
	if code, body := r.do("PUT", "/v1/person", marshalBytes(r.t, p), true); code >= 300 {
		r.t.Fatalf("publish person: %d %s", code, body)
	}
	return p
}

// peer reads address's directory entry and published person.
func (r *rawAgent) peer(address string) (identity.Public, protocol.PersonRoster) {
	r.t.Helper()
	label, agent, _ := protocol.SplitAddress(address)
	_, data := r.do("GET", "/v1/agents/"+label+"/"+agent, nil, true)
	var d protocol.DirectoryEntry
	json.Unmarshal(data, &d)
	_, data = r.do("GET", "/v1/agents/"+label+"/"+agent+"/profile", nil, true)
	var p protocol.Profile
	json.Unmarshal(data, &p)
	roster, err := protocol.ParsePersonRoster(p.Person)
	if err != nil {
		r.t.Fatalf("person of %s: %v (%s)", address, err, data)
	}
	return d.Public, roster
}

// send seals in to pub and posts it; it returns the envelope's id.
func (r *rawAgent) send(pub identity.Public, in envelope.Inner) string {
	r.t.Helper()
	rc, _ := pub.Recipient()
	if in.ID == "" {
		in.ID = protocol.NewID()
	}
	in.From, in.To, in.TS = r.addr, pub.Address, time.Now().Unix()
	env, err := envelope.Seal(in, r.id.Sign, rc)
	if err != nil {
		r.t.Fatal(err)
	}
	if code, body := r.do("POST", "/v1/messages", marshalBytes(r.t, env), true); code >= 300 {
		r.t.Fatalf("send: %d %s", code, body)
	}
	return env.ID
}

func dmBodies(dm map[string]any) string {
	var out []string
	for _, m := range dm["messages"].([]any) {
		x := m.(map[string]any)
		out = append(out, x["dir"].(string)+":"+x["body"].(string))
	}
	return strings.Join(out, "|")
}

// The browser device's engine against a real relay and a real laptop
// daemon: explicit join and person, two separate DMs both ways, a reply,
// a held question, receipts, offline and back, a reload, dedupe, a
// conflicting duplicate, a person conflict freezing a DM, an unproven root
// held, a changed key held, and revocation. Nothing runs anything.
func TestBrowserEngineJourney(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + hub.Addr
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	runDaemon(t, alice)
	if _, err := alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, dir)
	if v := w.ok(map[string]any{"op": "init", "base": base}); v["joined"] != false {
		t.Fatalf("a fresh store holds a device: %v", v)
	}

	// Join explicitly, with the device name the person types.
	code, err := alice.Invite(ctx, "dana", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	// A device talks only to the server that served its page, over https
	// (plain http only on this computer's loopback, for tests).
	for _, c := range []struct{ hub, base, want string }{
		{"https://relay.example", "https://other.example", "comes from"},
		{"https://relay.example", "http://relay.example", "only over https"},
		{"https://relay.example:8443", "https://relay.example", "comes from"},
	} {
		w.refuses(c.base, w.call(map[string]any{"op": "sameOrigin", "hub": c.hub, "base": c.base}), c.want)
	}
	for _, c := range [][2]string{{"https://relay.example", "https://relay.example"}, {"https://127.0.0.1:8443", "http://127.0.0.1:8443"}} {
		w.ok(map[string]any{"op": "sameOrigin", "hub": c[0], "base": c[1]})
	}
	if v := w.ok(map[string]any{"op": "join", "code": code, "name": "phone"}); v["address"] != "dana/phone" {
		t.Fatalf("join: %v", v)
	}
	keys := w.ok(map[string]any{"op": "keys"})
	if keys["extractable"] != false || keys["exported"] != false {
		t.Fatalf("device keys can be exported: %v", keys)
	}
	var danaPub identity.Public
	json.Unmarshal([]byte(keys["public"].(string)), &danaPub)
	o := w.api("/api/overview", nil)
	if o["person"] != nil || o["persons"] != true || len(o["dms"].([]any)) != 0 {
		t.Fatalf("a person was made without being asked: %v", o["person"])
	}
	w.refuses("DM without a person", w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": alice.Address}}), "person")
	p := w.api("/api/person", map[string]any{"label": "Dana"})
	if p["person"].(map[string]any)["state"] != "self" || !strings.Contains(p["note"].(string), "set up") {
		t.Fatalf("person: %v", p)
	}
	w.refuses("second person", w.call(map[string]any{"op": "api", "path": "/api/person", "body": map[string]any{"label": "Dana 2"}}), "second person")
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})

	// Alice starts a DM with Dana's person and writes; Dana starts another.
	var c1 string
	w.until("alice's DM with dana", func() bool { c1, err = alice.CreateDM(ctx, "dana/phone"); return err == nil })
	if _, err := alice.SendConv(ctx, c1, client.ConvOutgoing{Body: "hello dana <&>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.SendConv(ctx, c1, client.ConvOutgoing{Kind: envelope.KindQuestion, Body: "can you check?"}); err != nil {
		t.Fatal(err)
	}
	w.until("both of alice's messages in the browser", func() bool {
		return w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + c1})["error"] == nil && dmBodies(w.api("/api/dm?id="+c1, nil)) == "in:hello dana <&>|in:can you check?"
	})
	d1 := w.api("/api/dm?id="+c1, nil)
	msgs := d1["messages"].([]any)
	q := msgs[1].(map[string]any)
	if q["state"] != "conv_held" || !strings.Contains(q["state_text"].(string), "nothing runs it") || d1["peer"].(map[string]any)["state"] != "pinned" {
		t.Fatalf("held question: %v, peer %v", q, d1["peer"])
	}
	c2 := w.api("/api/dm/new", map[string]any{"address": alice.Address})["id"].(string)
	if c2 == c1 {
		t.Fatal("two DMs share an id")
	}
	sent := w.api("/api/dm/send", map[string]any{"conv": c2, "body": "a separate topic"})
	if sent["state"] == "waiting" || sent["state"] == "failed" {
		t.Fatalf("send: %v", sent)
	}
	helloID := msgs[0].(map[string]any)["id"].(string)
	w.api("/api/dm/send", map[string]any{"conv": c1, "body": "hi alice", "reply_to": helloID})
	w.refuses("reply across DMs", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": c2, "body": "x", "reply_to": helloID}}), "within its conversation")
	aliceBodies := func(conv string) string {
		ms, _ := alice.ConversationMessages(conv)
		var out []string
		for _, m := range ms {
			out = append(out, m.Dir+":"+m.Body)
		}
		return strings.Join(out, "|")
	}
	w.until("dana's messages at alice, each in its DM", func() bool {
		return aliceBodies(c1) == "out:hello dana <&>|out:can you check?|in:hi alice" && aliceBodies(c2) == "in:a separate topic"
	})
	if ms, _ := alice.ConversationMessages(c1); ms[2].ReplyTo == "" {
		t.Fatalf("the reply lost its target: %+v", ms[2])
	}
	w.until("receipts: delivered", func() bool {
		ms := w.api("/api/dm?id="+c2, nil)["messages"].([]any)
		return ms[0].(map[string]any)["state"] == "delivered"
	})
	if n := w.ok(map[string]any{"op": "count", "store": "receipts"})["n"]; n != float64(0) {
		t.Fatalf("%v receipts not sent", n)
	}

	// Offline: what Dana writes is kept and goes out on return; what Alice
	// writes meanwhile waits in the relay's custody.
	w.ok(map[string]any{"op": "offline", "on": true})
	w.until("disconnected", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == false })
	kept := w.api("/api/dm/send", map[string]any{"conv": c2, "body": "written on the train"})
	if kept["state"] != "waiting" || !strings.Contains(kept["detail"].(string), "cannot reach") {
		t.Fatalf("offline send: %v", kept)
	}
	if _, err := alice.SendConv(ctx, c2, client.ConvOutgoing{Body: "while you were away"}); err != nil {
		t.Fatal(err)
	}
	w.ok(map[string]any{"op": "offline", "on": false})
	w.until("both sides caught up", func() bool {
		return strings.Contains(dmBodies(w.api("/api/dm?id="+c2, nil)), "in:while you were away") &&
			strings.Contains(aliceBodies(c2), "in:written on the train")
	})

	// A reload: the same device, the same separate DMs, nothing twice.
	before := w.ok(map[string]any{"op": "count", "store": "inbox"})["n"]
	if v := w.ok(map[string]any{"op": "reload", "base": base}); v["joined"] != true {
		t.Fatalf("reload: %v", v)
	}
	w.until("reconnected", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == true })
	o = w.api("/api/overview", nil)
	if len(o["dms"].([]any)) != 2 || o["person"].(map[string]any)["label"] != "Dana" {
		t.Fatalf("after reload: %v", o["dms"])
	}
	time.Sleep(500 * time.Millisecond)
	if after := w.ok(map[string]any{"op": "count", "store": "inbox"})["n"]; after != before {
		t.Fatalf("inbox %v after reload, %v before", after, before)
	}

	// Eve, driven through the wire, tries what a correct client would not.
	eveCode, _ := alice.Invite(ctx, "eve", time.Hour, false)
	eve := newRawAgent(t, eveCode, "lab")
	eveRoster := eve.roster("Eve")
	dPub, dRoster := eve.peer("dana/phone")
	members := []protocol.ConvMember{{Person: eveRoster.Person, Roster: eveRoster.Hash()}, {Person: dRoster.Person, Roster: dRoster.Hash()}}
	if members[0].Person > members[1].Person {
		members[0], members[1] = members[1], members[0]
	}
	root := protocol.ConvRoot{V: 1, Kind: protocol.ConvKindDM, Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix(),
		Creator: protocol.ConvCreator{Person: eveRoster.Person, Roster: eveRoster.Hash(), Address: eve.addr, Fingerprint: eve.id.Public(eve.addr).Fingerprint()}}
	root.Sign(eve.id.Sign)
	rootJSON := marshalBytes(t, root)
	msg := func(lid, body string) envelope.Inner {
		return envelope.Inner{V: 2, Kind: "message", Body: body, Conv: root.ID(), LID: lid, Root: rootJSON, Origin: "ui"}
	}
	lid := protocol.NewID()
	eve.send(dPub, msg(lid, "first"))
	w.until("eve's DM", func() bool {
		return w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + root.ID()})["error"] == nil
	})
	inbox := func() float64 { return w.ok(map[string]any{"op": "count", "store": "inbox"})["n"].(float64) }
	held := func() float64 { return w.ok(map[string]any{"op": "count", "store": "held"})["n"].(float64) }
	n0, h0 := inbox(), held()
	eve.send(dPub, msg(lid, "first")) // the same message again, another envelope
	eve.send(dPub, msg(lid, "other")) // other content under the same logical id
	w.until("the conflicting copy held", func() bool { return held() == h0+1 })
	if inbox() != n0 || dmBodies(w.api("/api/dm?id="+root.ID(), nil)) != "in:first" {
		t.Fatalf("duplicate stored: %v", dmBodies(w.api("/api/dm?id="+root.ID(), nil)))
	}
	// A root Eve did not make, from Eve: held until proven, never admitted.
	other := root
	other.Creator = protocol.ConvCreator{Person: dRoster.Person, Roster: dRoster.Hash(), Address: "dana/phone", Fingerprint: dPub.Fingerprint()}
	other.Nonce = protocol.NewID()
	other.Sign(eve.id.Sign)
	otherJSON := marshalBytes(t, other)
	eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Body: "unproven", Conv: other.ID(), LID: protocol.NewID(), Root: otherJSON, Origin: "ui"})
	w.until("the unproven root held", func() bool { return held() == h0+2 })
	// Eve publishes another person record for the same device: frozen.
	eve.roster("Eve again")
	w.until("eve's DM frozen", func() bool {
		return w.api("/api/dm?id="+root.ID(), nil)["frozen"] != ""
	})
	w.refuses("send in a frozen DM", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": root.ID(), "body": "x"}}), "frozen")
	eve.send(dPub, msg(protocol.NewID(), "after the change"))
	w.until("held after the change", func() bool { return held() == h0+3 })

	// Alice's key "changes" (the pin now names Eve's key): her next message
	// is held, not opened with a key that is not hers.
	evePub := eve.id.Public(eve.addr)
	alicePin := w.ok(map[string]any{"op": "tamperPin", "address": alice.Address, "json": publicJSON(t, evePub), "fingerprint": evePub.Fingerprint()})
	if _, err := alice.SendConv(ctx, c1, client.ConvOutgoing{Body: "is this still me?"}); err != nil {
		t.Fatal(err)
	}
	w.until("held for a changed key", func() bool { return held() == h0+4 })
	if strings.Contains(dmBodies(w.api("/api/dm?id="+c1, nil)), "still me") {
		t.Fatal("a message was opened with a changed key")
	}
	// Each hold says why, in words that are not a failed signature.
	reasons := map[string]int{}
	for _, q := range w.api("/api/overview", nil)["quarantine"].([]any) {
		reasons[q.(map[string]any)["reason"].(string)]++
	}
	for _, want := range []string{"sent different content", "can be checked here", "disagrees with the person record", "changed key"} {
		found := false
		for r := range reasons {
			found = found || strings.Contains(r, want)
		}
		if !found {
			t.Errorf("no hold says %q: %v", want, reasons)
		}
	}

	// Revoked: the device stops and says so. (Alice's pin is set back first,
	// so her DM is not frozen and a send reaches the Hub.)
	w.ok(map[string]any{"op": "tamperPin", "address": alice.Address, "json": alicePin["json"], "fingerprint": alicePin["fingerprint"]})
	if err := alice.Revoke(ctx, "dana/phone"); err != nil {
		t.Fatal(err)
	}
	// The send that finds out is refused, not kept as if the server were away.
	w.refuses("send that finds the revocation", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": c2, "body": "still here?"}}), "removed")
	w.until("revoked", func() bool { return w.ok(map[string]any{"op": "status"})["revoked"] == true })
	if strings.Contains(dmBodies(w.api("/api/dm?id="+c2, nil)), "still here?") {
		t.Fatal("a message was kept after the device was removed")
	}
	if o := w.api("/api/overview", nil); o["device"].(map[string]any)["revoked"] != true {
		t.Fatalf("overview after revoke: %v", o["device"])
	}
	w.refuses("send after revoke", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": c1, "body": "x"}}), "removed")
	_ = danaPub
}
