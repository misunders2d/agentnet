package ui

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

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

// roster makes and publishes a person for this device.
func (r *rawAgent) roster(label string) protocol.PersonRoster {
	p := r.newRoster(label)
	r.publish(p)
	return p
}

// newRoster makes a signed person for this device without publishing it.
func (r *rawAgent) newRoster(label string) protocol.PersonRoster {
	p := protocol.PersonRoster{Person: protocol.NewID(), Label: label,
		Devices: []identity.Public{r.id.Public(r.addr)}}
	p.Sign(r.id.Sign)
	return p
}

func (r *rawAgent) publish(p protocol.PersonRoster) {
	r.t.Helper()
	if code, body := r.do("PUT", "/v1/person", marshalBytes(r.t, p), true); code >= 300 {
		r.t.Fatalf("publish person: %d %s", code, body)
	}
}

// forge makes the engine's server lie about p: another record at p's
// first step (a new label, validly signed by r's device), served as p's
// chain and in r's profile. An honest Hub never serves one (its chains are
// linear); a device that sees one freezes the person.
func (w *engineNode) forge(r *rawAgent, p protocol.PersonRoster, label string) {
	w.t.Helper()
	f := protocol.PersonRoster{Person: p.Person, Label: label, Devices: p.Devices}
	f.Sign(r.id.Sign)
	w.ok(map[string]any{"op": "forge", "person": p.Person, "record": string(marshalBytes(w.t, f)), "address": r.addr})
}

// dmRoot is a DM between r's person own and the person peer names (at
// peerPub's device), made by r, with its JSON.
func (r *rawAgent) dmRoot(own protocol.PersonRoster, peerPub identity.Public, peer protocol.PersonRoster) (protocol.ConvRoot, []byte) {
	members := []protocol.ConvMember{{Person: own.Person, Roster: own.Hash()}, {Person: peer.Person, Roster: peer.Hash()}}
	if members[0].Person > members[1].Person {
		members[0], members[1] = members[1], members[0]
	}
	root := protocol.ConvRoot{V: protocol.ConvRootVersion, Kind: protocol.ConvKindDM, Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix(),
		Creator: protocol.ConvCreator{Person: own.Person, Roster: own.Hash(), Address: r.addr, Fingerprint: r.id.Public(r.addr).Fingerprint()}}
	root.Sign(r.id.Sign)
	return root, marshalBytes(r.t, root)
}

// browserCode is code without its pinned certificate: a browser trusts
// the Hub through its own certificate authorities (here the test Hub's
// certificate, through NODE_EXTRA_CA_CERTS), never through an invitation.
func browserCode(t *testing.T, code string) string {
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	inv.CertPEM = ""
	return inv.Encode()
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
	// An invitation that pins the Hub's own certificate is refused before
	// anything is sent: the join that follows, with the same single-use
	// secret and name, proves nothing was enrolled.
	w.refuses("invitation with a pinned certificate", w.call(map[string]any{"op": "join", "code": code, "name": "phone"}), "certificate")
	if v := w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"}); v["address"] != "dana/phone" {
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
	root := protocol.ConvRoot{V: protocol.ConvRootVersion, Kind: protocol.ConvKindDM, Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix(),
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
	// A participation record that is not the sending device's own (Eve signs
	// one claiming Alice's device wrote it) is held, never shown or counted.
	heldBefore := w.ok(map[string]any{"op": "count", "store": "held"})["n"].(float64)
	forged := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author:   protocol.EventAuthor{Person: eveRoster.Person, Roster: eveRoster.Hash(), Address: alice.Address, Fingerprint: eve.id.Public(eve.addr).Fingerprint()},
		Host:     &protocol.ParticipationHost{Person: eveRoster.Person, Address: eve.addr, Fingerprint: eve.id.Public(eve.addr).Fingerprint()},
		Audience: protocol.AudienceConversation}
	forged.Sign(eve.id.Sign)
	eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Sub: envelope.SubEvent, Body: string(marshalBytes(t, forged)), Conv: root.ID(),
		LID: protocol.NewID(), Root: rootJSON, PID: forged.PID})
	// So is one whose signature does not verify (changed after signing).
	eveAuthor := forged.Author
	eveAuthor.Address = eve.addr
	tampered := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author: eveAuthor, Host: forged.Host, Audience: protocol.AudienceConversation}
	tampered.Sign(eve.id.Sign)
	tampered.Note = "changed after signing"
	eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Sub: envelope.SubEvent, Body: string(marshalBytes(t, tampered)), Conv: root.ID(),
		LID: protocol.NewID(), Root: rootJSON, PID: tampered.PID})
	w.until("the forged and changed records held", func() bool {
		return w.ok(map[string]any{"op": "count", "store": "held"})["n"].(float64) == heldBefore+2
	})
	if a := w.api("/api/dm?id="+root.ID(), nil)["agents"].([]any); len(a) != 0 {
		t.Fatalf("a forged record counts: %v", a)
	}
	inbox := func() float64 { return w.ok(map[string]any{"op": "count", "store": "inbox"})["n"].(float64) }
	held := func() float64 { return w.ok(map[string]any{"op": "count", "store": "held"})["n"].(float64) }
	n0, h0 := inbox(), held()
	eve.send(dPub, msg(lid, "first")) // the same message again, another envelope
	eve.send(dPub, msg(lid, "other")) // other content under the same logical id
	w.until("the conflicting copy held", func() bool { return held() == h0+1 })
	if inbox() != n0 || dmBodies(w.api("/api/dm?id="+root.ID(), nil)) != "in:first" {
		t.Fatalf("duplicate stored: %v", dmBodies(w.api("/api/dm?id="+root.ID(), nil)))
	}
	// A decision counts only from the invited host: Eve invites the agent
	// on Dana's device and accepts it herself; it stays invited.
	named := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author: eveAuthor, Host: &protocol.ParticipationHost{Person: dRoster.Person, Address: "dana/phone", Fingerprint: dPub.Fingerprint()},
		Audience: protocol.AudienceConversation}
	named.Sign(eve.id.Sign)
	selfAccept := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: named.PID, Type: protocol.EventAccept, Prev: named.Hash(),
		TS: time.Now().Unix(), Author: eveAuthor}
	selfAccept.Sign(eve.id.Sign)
	// A dismissal following a record not held here does not count either.
	strayDismiss := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: named.PID, Type: protocol.EventDismiss, Prev: strings.Repeat("ab", 32),
		TS: time.Now().Unix(), Author: eveAuthor}
	strayDismiss.Sign(eve.id.Sign)
	for _, ev := range []protocol.ParticipationEvent{named, selfAccept, strayDismiss} {
		eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Sub: envelope.SubEvent, Body: string(marshalBytes(t, ev)), Conv: root.ID(),
			LID: protocol.NewID(), Root: rootJSON, PID: ev.PID})
	}
	w.until("the invitation naming dana", func() bool {
		a := w.api("/api/dm?id="+root.ID(), nil)["agents"].([]any)
		return len(a) == 1 && a[0].(map[string]any)["held"] == float64(2)
	})
	if a := w.api("/api/dm?id="+root.ID(), nil)["agents"].([]any)[0].(map[string]any); a["state"] != "invited" || a["host_here"] != true || a["can_decide"] != false {
		t.Fatalf("an accept not by the host: %v", a)
	}
	// Eve invites her own agent into the DM and asks it: the record reads as
	// a sentence, and her request to her agent is history here, not held
	// for Dana (this browser never runs anything).
	evePID := protocol.NewID()
	evePubKey := eve.id.Public(eve.addr)
	ev := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: evePID, Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author:   protocol.EventAuthor{Person: eveRoster.Person, Roster: eveRoster.Hash(), Address: eve.addr, Fingerprint: evePubKey.Fingerprint()},
		Host:     &protocol.ParticipationHost{Person: eveRoster.Person, Address: eve.addr, Fingerprint: evePubKey.Fingerprint()},
		Audience: protocol.AudienceConversation}
	ev.Sign(eve.id.Sign)
	eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Sub: envelope.SubEvent, Body: string(marshalBytes(t, ev)), Conv: root.ID(),
		LID: protocol.NewID(), Root: rootJSON, PID: evePID})
	eve.send(dPub, envelope.Inner{V: 2, Kind: "question", Body: "summarize this for me", Conv: root.ID(), LID: protocol.NewID(), Root: rootJSON,
		Origin: "ui", PID: evePID, Target: &envelope.Target{Address: eve.addr, Fingerprint: evePubKey.Fingerprint()}})
	var evMsgs []any
	w.until("eve's record and request", func() bool {
		evMsgs = w.api("/api/dm?id="+root.ID(), nil)["messages"].([]any)
		return len(evMsgs) == 6
	})
	record, request := evMsgs[4].(map[string]any), evMsgs[5].(map[string]any)
	if record["event"] != "Eve invited Eve's agent into this DM." || record["body"] != "" {
		t.Fatalf("the record as the browser shows it: %v", record)
	}
	if request["state"] != "" || request["to"] != eve.addr || request["pid"] != evePID || strings.Contains(fmt.Sprint(request["state_text"]), "Held") {
		t.Fatalf("a request to eve's own agent as the browser shows it: %v", request)
	}
	// The same signed record in two messages (other envelope ids and
	// logical ids) is one record, as the core keeps it: Eve's accept of her
	// own agent, twice, is one decision (active, not a conflict), and the
	// non-host accept again is still one record not counted.
	evAccept := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: evePID, Type: protocol.EventAccept, Prev: ev.Hash(), TS: time.Now().Unix(),
		Author: ev.Author}
	evAccept.Sign(eve.id.Sign)
	for _, e := range []protocol.ParticipationEvent{evAccept, evAccept, selfAccept} {
		eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Sub: envelope.SubEvent, Body: string(marshalBytes(t, e)), Conv: root.ID(),
			LID: protocol.NewID(), Root: rootJSON, PID: e.PID})
	}
	agentByPID := func(pid string) map[string]any {
		for _, x := range w.api("/api/dm?id="+root.ID(), nil)["agents"].([]any) {
			if x.(map[string]any)["pid"] == pid {
				return x.(map[string]any)
			}
		}
		return nil
	}
	w.until("eve's own agent accepted", func() bool { a := agentByPID(evePID); return a != nil && a["state"] != "invited" })
	w.until("all three copies stored, one row per exact record", func() bool { return len(w.api("/api/dm?id="+root.ID(), nil)["messages"].([]any)) == 7 })
	if a := agentByPID(evePID); a["state"] != "active" || a["held"] != float64(0) {
		t.Fatalf("one accept sent twice: %v", a)
	}
	if a := agentByPID(named.PID); a["state"] != "invited" || a["held"] != float64(2) {
		t.Fatalf("a record not counted, sent again: %v", a)
	}
	n0 = inbox()

	// A root Eve did not make (it names Dana as its creator), from Eve: any
	// member device may bring a root, but it must verify with its
	// creator's key as that person's chain names it: refused, never admitted.
	other := root
	other.Creator = protocol.ConvCreator{Person: dRoster.Person, Roster: dRoster.Hash(), Address: "dana/phone", Fingerprint: dPub.Fingerprint()}
	other.Nonce = protocol.NewID()
	other.Sign(eve.id.Sign)
	otherJSON := marshalBytes(t, other)
	eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Body: "unproven", Conv: other.ID(), LID: protocol.NewID(), Root: otherJSON, Origin: "ui"})
	w.until("the forged root held", func() bool { return held() == h0+2 })
	// The server shows another record at Eve's pinned step (a fork): the
	// next send reads it and freezes her, and sends nothing.
	w.forge(eve, eveRoster, "Eve again")
	w.refuses("send in a frozen DM", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": root.ID(), "body": "x"}}), "frozen")
	if w.api("/api/dm?id="+root.ID(), nil)["frozen"] == "" {
		t.Fatal("eve's DM is not frozen")
	}
	eve.send(dPub, msg(protocol.NewID(), "after the change"))
	w.until("held after the change", func() bool { return held() == h0+3 })

	// Alice's key "changes" (the pin now names Eve's key). What Dana kept
	// for her before, one message queued (its post was lost) and one
	// waiting (written offline), is not sent after the change.
	w.ok(map[string]any{"op": "dropPosts", "on": true})
	queued := w.api("/api/dm/send", map[string]any{"conv": c1, "body": "queued before the change"})
	w.ok(map[string]any{"op": "offline", "on": true})
	w.until("disconnected again", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == false })
	waiting := w.api("/api/dm/send", map[string]any{"conv": c2, "body": "waiting before the change"})
	if queued["state"] != "queued" || waiting["state"] != "waiting" {
		t.Fatalf("kept: %v, %v", queued, waiting)
	}
	evePub := eve.id.Public(eve.addr)
	alicePin := w.ok(map[string]any{"op": "tamperPin", "address": alice.Address, "json": publicJSON(t, evePub), "fingerprint": evePub.Fingerprint()})
	w.ok(map[string]any{"op": "dropPosts", "on": false})
	w.ok(map[string]any{"op": "offline", "on": false})
	w.until("connected after the change", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == true })
	w.ok(map[string]any{"op": "flush"})
	for _, m := range []map[string]any{queued, waiting} {
		rec := w.ok(map[string]any{"op": "outbox", "id": m["id"]})["rec"].(map[string]any)
		if rec["state"] != m["state"] || !strings.Contains(rec["detail"].(string), "no longer the one") {
			t.Fatalf("kept message after the change: %v", rec)
		}
	}
	// Her next message is held, not opened with a key that is not hers.
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
	for _, want := range []string{"sent different content", "did not verify", "disagrees with the person record", "changed key"} {
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

// Messages that stay unproven never keep later ones from being looked at
// again: more than a page of them come first (by id), then one whose
// proof comes later, which is admitted when it does.
func TestBrowserEngineRetriesEveryHeld(t *testing.T) {
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
	invite := func(label string) string {
		code, err := alice.Invite(ctx, label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + hub.Addr})
	w.ok(map[string]any{"op": "join", "code": browserCode(t, invite("dana")), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})
	held := func() float64 { return w.ok(map[string]any{"op": "count", "store": "held"})["n"].(float64) }

	// Neither Gus nor Fay has published a person yet: their DMs cannot be
	// proven here. Gus's messages sort first and stay unproven.
	gus, fay := newRawAgent(t, invite("gus"), "lab"), newRawAgent(t, invite("fay"), "lab")
	dPub, dRoster := gus.peer("dana/phone")
	gusRoot, gusJSON := gus.dmRoot(gus.newRoster("Gus"), dPub, dRoster)
	const unproven = 101 // past the old limit of 100, over three pages
	for i := 0; i < unproven; i++ {
		gus.send(dPub, envelope.Inner{ID: fmt.Sprintf("00%030x", i), V: 2, Kind: "message", Body: "unproven",
			Conv: gusRoot.ID(), LID: protocol.NewID(), Root: gusJSON, Origin: "ui"})
	}
	fayRoster := fay.newRoster("Fay")
	fayRoot, fayJSON := fay.dmRoot(fayRoster, dPub, dRoster)
	fay.send(dPub, envelope.Inner{ID: "ff" + strings.Repeat("0", 30), V: 2, Kind: "message", Body: "proven later",
		Conv: fayRoot.ID(), LID: protocol.NewID(), Root: fayJSON, Origin: "ui"})
	w.until("everything held", func() bool { return held() == unproven+1 })

	// Fay publishes her person: the member list that follows is new
	// evidence, and her message is admitted past all of Gus's.
	fay.publish(fayRoster)
	w.until("fay's message admitted", func() bool {
		return w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + fayRoot.ID()})["error"] == nil &&
			dmBodies(w.api("/api/dm?id="+fayRoot.ID(), nil)) == "in:proven later"
	})
	if n := held(); n != unproven {
		t.Fatalf("%v held, want Gus's %d", n, unproven)
	}
}

// A person record that changed is found only by the profile read before a
// send: a new message and a kept one are both refused after that read,
// not sent to the device the changed record names.
func TestBrowserEngineRechecksAfterProfile(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + hub.Addr
	pool := x509.NewCertPool()
	if pem, err := os.ReadFile(filepath.Join(dir, "tls.crt")); err != nil || !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("hub certificate: %v", err)
	}
	// agent joins a Go agent with a person and a running daemon, and
	// returns it as a raw agent too, to publish another person for it.
	agent := func(code, name, label string) (*client.Agent, *rawAgent) {
		home := filepath.Join(t.TempDir(), name)
		a, err := client.Join(ctx, home, code, name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		runDaemon(t, a)
		if _, err := a.CreatePerson(ctx, label); err != nil {
			t.Fatal(err)
		}
		id, err := identity.Load(filepath.Join(home, "identity.json"))
		if err != nil {
			t.Fatal(err)
		}
		return a, &rawAgent{t: t, id: id, addr: a.Address, hub: base,
			http: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 10 * time.Second}}
	}
	alice, aliceRaw := agent(testhub.BootstrapCode(t, dir), "laptop", "Alice")
	code, _ := alice.Invite(ctx, "bob", time.Hour, false)
	_, bobRaw := agent(code, "desk", "Bob")
	code, _ = alice.Invite(ctx, "erin", time.Hour, false)
	_, erinRaw := agent(code, "box", "Erin")
	code, _ = alice.Invite(ctx, "dana", time.Hour, false)
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})
	toAlice := w.api("/api/dm/new", map[string]any{"address": alice.Address})["id"].(string)
	toBob := w.api("/api/dm/new", map[string]any{"address": bobRaw.addr})["id"].(string)
	toErin := w.api("/api/dm/new", map[string]any{"address": erinRaw.addr})["id"].(string)
	for _, r := range []*rawAgent{aliceRaw, bobRaw, erinRaw} {
		w.until(r.addr+" reads conversations", func() bool { return aliceRaw.supports(r.addr) })
	}

	// Offline, Dana keeps a message for Bob; then no events come.
	w.ok(map[string]any{"op": "offline", "on": true})
	kept := w.api("/api/dm/send", map[string]any{"conv": toBob, "body": "kept for bob"})
	if kept["state"] != "waiting" {
		t.Fatalf("offline send: %v", kept)
	}
	w.ok(map[string]any{"op": "stopStream"})
	w.ok(map[string]any{"op": "offline", "on": false})

	// The server shows another record at each one's pinned step (a fork);
	// only a profile read shows it.
	for _, r := range []*rawAgent{aliceRaw, bobRaw} {
		_, p := aliceRaw.peer(r.addr)
		w.forge(r, p, p.Label+" again")
	}
	w.ok(map[string]any{"op": "flush", "connected": true})
	rec := w.ok(map[string]any{"op": "outbox", "id": kept["id"]})["rec"].(map[string]any)
	if rec["state"] != "waiting" || !strings.Contains(rec["detail"].(string), "conflicts") {
		t.Fatalf("kept message after the profile read: %v", rec)
	}
	w.refuses("send after the profile read", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": toAlice, "body": "x"}}), "conflicts")
	if ms := w.api("/api/dm?id="+toAlice, nil)["messages"].([]any); len(ms) != 0 {
		t.Fatalf("messages kept for alice: %v", ms)
	}

	// A conflict seen while a file is still uploading (here another send's
	// profile read) stops the message before the relay is given it: it
	// stays queued, saying why.
	w.ok(map[string]any{"op": "holdUploads"})
	w.ok(map[string]any{"op": "sendFilesLater", "connected": true, "conv": toErin, "files": []any{map[string]any{"name": "late.txt", "b64": base64.StdEncoding.EncodeToString([]byte("late"))}}})
	_, erinP := aliceRaw.peer(erinRaw.addr)
	w.forge(erinRaw, erinP, "Erin again")
	w.refuses("a send that reads Erin's new person", w.call(map[string]any{"op": "api", "path": "/api/dm/send", "body": map[string]any{"conv": toErin, "body": "y"}}), "conflicts")
	late := w.ok(map[string]any{"op": "releaseUploads"})["v"].(map[string]any)
	rec = w.ok(map[string]any{"op": "outbox", "id": late["id"]})["rec"].(map[string]any)
	if rec["state"] != "queued" || !strings.Contains(rec["detail"].(string), "conflicts") || late["state"] != "queued" {
		t.Fatalf("a file message after a conflict seen during its upload: %v %q (answered %v)", rec["state"], rec["detail"], late["state"])
	}
	if st := w.call(map[string]any{"op": "hubStatus", "id": late["id"]}); st["error"] == nil {
		t.Fatalf("the relay holds the message: %v", st)
	}
}

// supports reads address's profile and says whether it reads conversations.
func (r *rawAgent) supports(address string) bool {
	r.t.Helper()
	label, agent, _ := protocol.SplitAddress(address)
	_, data := r.do("GET", "/v1/agents/"+label+"/"+agent, nil, true)
	var d protocol.DirectoryEntry
	json.Unmarshal(data, &d)
	_, data = r.do("GET", "/v1/agents/"+label+"/"+agent+"/profile", nil, true)
	var p protocol.Profile
	json.Unmarshal(data, &p)
	return p.Supports(address, d.Public.SignKey, protocol.CapEnv2)
}

// The browser invites, asks and dismisses the agent on the other person's
// computer, as the core resolves it, and never hosts, accepts or runs one:
// against a real laptop daemon.
func TestBrowserEngineAgents(t *testing.T) {
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
	runDaemon(t, alice)
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
	w.until("alice's DM with dana", func() bool { conv, err = alice.CreateDM(ctx, "dana/phone"); return err == nil })
	hello, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "the deploy plan"})
	if err != nil {
		t.Fatal(err)
	}
	w.until("alice's message in the browser", func() bool {
		return w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + conv})["error"] == nil && len(w.api("/api/dm?id="+conv, nil)["messages"].([]any)) == 1
	})
	notes := w.api("/api/dm/send", map[string]any{"conv": conv, "body": "my notes"})["id"].(string)
	agents := func() []any { return w.api("/api/dm?id="+conv, nil)["agents"].([]any) }
	if o := w.api("/api/overview", nil); o["agents"] != true {
		t.Fatal("the browser offers no agents")
	}

	// What cannot be invited from here is refused before anything is sent.
	w.refuses("this browser as the host", w.call(map[string]any{"op": "api", "path": "/api/dm/agent/invite",
		"body": map[string]any{"conv": conv, "host": "dana/phone"}}), "runs no agent")
	w.refuses("a message not of this DM", w.call(map[string]any{"op": "api", "path": "/api/dm/agent/invite",
		"body": map[string]any{"conv": conv, "host": alice.Address, "share": []string{protocol.NewID()}}}), "not an earlier message")
	if len(agents()) != 0 {
		t.Fatal("a refused invite left a record")
	}

	// Dana invites Alice's agent, sharing both earlier messages exactly.
	pid := w.api("/api/dm/agent/invite", map[string]any{"conv": conv, "host": alice.Address, "share": []string{hello.ID, notes}, "note": "help"})["pid"].(string)
	var info client.ParticipationInfo
	w.until("the invitation at alice", func() bool {
		info, err = alice.Participation(pid)
		return err == nil && info.State == client.PartInvited
	})
	if !info.HostHere || info.Inviter.Label != "Dana" || info.Note != "help" || len(info.Grant) != 2 {
		t.Fatalf("the browser's invitation as alice resolves it: %+v", info)
	}
	if c, err := alice.ParticipationContext(pid, 0); err != nil || c.Missing != 0 || len(c.Messages) != 2 {
		t.Fatalf("what alice's agent would be shown: %+v %v", c, err)
	}
	a := agents()[0].(map[string]any)
	if a["state"] != "invited" || a["can_decide"] != false || a["can_ask"] != false || len(a["shared"].([]any)) != 2 {
		t.Fatalf("the invitation in the browser: %v", a)
	}

	// Alice accepts on her computer; the browser can then ask it.
	if _, err := alice.AcceptParticipation(ctx, pid); err != nil {
		t.Fatal(err)
	}
	w.until("active in the browser", func() bool { a = agents()[0].(map[string]any); return a["state"] == "active" && a["can_ask"] == true })
	w.refuses("deciding in the browser", w.call(map[string]any{"op": "api", "path": "/api/dm/agent/decide", "body": map[string]any{"pid": pid, "accept": true}}), "runs no agent")
	asked := w.api("/api/dm/agent/ask", map[string]any{"pid": pid, "body": "which branch?"})["id"].(string)
	var req client.ConvMessage
	w.until("the question at alice", func() bool {
		ms, _ := alice.ConversationMessages(conv)
		for _, m := range ms {
			if m.ID == asked {
				req = m
			}
		}
		return req.ID != ""
	})
	if req.PID != pid || req.Target == nil || req.Target.Address != alice.Address || req.Kind != "question" {
		t.Fatalf("the browser's question as alice holds it: %+v", req)
	}

	// Dana dismisses it; Alice's computer resolves the same.
	w.api("/api/dm/agent/dismiss", map[string]any{"pid": pid})
	w.until("dismissed at alice", func() bool { info, _ = alice.Participation(pid); return info.State == client.PartDismissed })
	w.refuses("asking after the dismissal", w.call(map[string]any{"op": "api", "path": "/api/dm/agent/ask", "body": map[string]any{"pid": pid, "body": "x"}}), "not active")

	// Alice invites "Dana's agent": the browser shows it, and cannot accept.
	mine, err := alice.InviteAgent(ctx, conv, "dana/phone", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	w.until("the invitation naming the browser", func() bool {
		for _, x := range agents() {
			if x.(map[string]any)["pid"] == mine.PID {
				a = x.(map[string]any)
				return true
			}
		}
		return false
	})
	if a["host_here"] != true || a["can_decide"] != false || a["can_ask"] != false || !strings.Contains(a["state_text"].(string), "runs none") {
		t.Fatalf("an invitation naming the browser: %v", a)
	}
}

// Notifications on the browser device, against the Hub's notification API
// (NOTIFY.md §3): nothing until the person turns them on; senders only from
// the person's own decisions and never a changed key; mutes by the channel
// of that DM; the attention hint both ways; a presentation report by its
// channel; a channel resolves only here. No alert is ever due (every
// attention message meets a mute or an unallowed sender), so the Hub
// pushes nothing to a real push service.
func TestBrowserEngineNotify(t *testing.T) {
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
	runDaemon(t, alice)
	if _, err := alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + hub.Addr, "notify": true})
	code, _ := alice.Invite(ctx, "dana", time.Hour, false)
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})
	type call struct {
		Method, Path string
		Body         map[string]any
		Signed       bool
	}
	calls := func() (out []call) {
		data, _ := json.Marshal(w.ok(map[string]any{"op": "notifyCalls"})["calls"])
		json.Unmarshal(data, &out)
		return out
	}
	prefs := func() map[string]any { return w.ok(map[string]any{"op": "notifyPrefs"}) }
	// Alice starts a DM with Dana: an arrival, not Dana's decision.
	var conv string
	w.until("alice's DM with dana", func() bool { conv, err = alice.CreateDM(ctx, "dana/phone"); return err == nil })
	hello, _ := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "hello"})
	w.until("the DM in the browser", func() bool { return w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + conv})["error"] == nil })
	n := w.api("/api/overview", nil)["notify"].(map[string]any)
	if n["available"] != true || n["enabled"] != false {
		t.Fatalf("before turning on: %v", n)
	}
	w.api("/api/notify/seen", map[string]any{"conv": conv, "ids": []string{hello.ID}}) // off: nothing to report
	w.ok(map[string]any{"op": "reload", "base": "https://" + hub.Addr})                // never turned on: a reconnect writes nothing
	w.until("reconnected", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == true })
	time.Sleep(300 * time.Millisecond)
	for _, c := range calls() {
		if c.Method != "GET" {
			t.Fatalf("something was sent before the person turned notifications on: %+v", c)
		}
	}
	if p := prefs(); p["subscribed"] != false || p["prefs"].(map[string]any)["enabled"] != false {
		t.Fatalf("the Hub before turning on: %v", p)
	}

	// Turned on: subscribed, then preferences with no sender yet.
	calls() // (the test's own look at the Hub above)
	w.api("/api/notify/enable", map[string]any{})
	got := calls()
	if len(got) != 2 || got[0].Path != "/v1/notify/subscription" || got[0].Method != "PUT" || !got[0].Signed ||
		got[1].Path != "/v1/notify/prefs" || got[1].Body["enabled"] != true || len(got[1].Body["senders"].([]any)) != 0 {
		t.Fatalf("turning on: %+v", got)
	}
	if p := prefs(); p["subscribed"] != true || p["prefs"].(map[string]any)["enabled"] != true {
		t.Fatalf("the Hub after turning on: %v", p)
	}
	// Dana mutes this DM, then allows Alice: her exact key.
	chanWant := protocol.NotifyChannel(conv, w.ok(map[string]any{"op": "keys"})["fingerprint"].(string))
	w.api("/api/notify/mute", map[string]any{"conv": conv, "muted": true})
	alicePerson := w.api("/api/dm?id="+conv, nil)["peer"].(map[string]any)["person"].(string)
	w.api("/api/notify/allow", map[string]any{"person": alicePerson, "allowed": true})
	held := prefs()["prefs"].(map[string]any)
	senders, mutes := held["senders"].([]any), held["mutes"].([]any)
	if len(senders) != 1 || senders[0].(map[string]any)["address"] != alice.Address || senders[0].(map[string]any)["fingerprint"] != alice.Self().Fingerprint() {
		t.Fatalf("senders after allowing alice: %v", senders)
	}
	if len(mutes) != 1 || mutes[0] != chanWant {
		t.Fatalf("mutes: %v, want [%s]", mutes, chanWant)
	}
	calls()

	// The hint both ways: Dana's DM message asks for Alice's attention on
	// Alice's channel of this DM; Alice's asks for Dana's, checked here.
	sent := w.api("/api/dm/send", map[string]any{"conv": conv, "body": "on my phone"})
	rec := w.ok(map[string]any{"op": "outbox", "id": sent["id"]})["rec"].(map[string]any)
	var env envelope.Envelope
	strictJSON([]byte(rec["envelope"].(string)), &env)
	if !env.Attn || env.Chan != protocol.NotifyChannel(conv, alice.Self().Fingerprint()) {
		t.Fatalf("dana's DM message: attn %v chan %q", env.Attn, env.Chan)
	}
	w.until("alice holds dana's message", func() bool {
		ms, _ := alice.ConversationMessages(conv)
		for _, m := range ms {
			if m.ID == sent["id"] {
				return true
			}
		}
		return false
	})
	back, _ := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "seen it"}) // muted here: no alert is due
	w.until("alice's reply in the browser", func() bool { return w.ok(map[string]any{"op": "inboxRec", "id": back.ID})["rec"] != nil })
	if r := w.ok(map[string]any{"op": "inboxRec", "id": back.ID})["rec"].(map[string]any); r["attn"] != "ok" {
		t.Fatalf("alice's reply as the browser holds it: %v", r)
	}

	// Presented messages are reported by that channel; the channel resolves here.
	calls()
	w.api("/api/notify/seen", map[string]any{"conv": conv, "ids": []string{hello.ID, back.ID}})
	got = calls()
	if len(got) != 1 || got[0].Path != "/v1/notify/seen" || got[0].Body["channel"] != chanWant || fmt.Sprint(got[0].Body["ids"]) != "["+hello.ID+" "+back.ID+"]" {
		t.Fatalf("seen: %+v", got)
	}
	if r := w.api("/api/notify/resolve?chan="+chanWant, nil); r["conv"] != conv {
		t.Fatalf("resolve: %v", r)
	}
	if r := w.api("/api/notify/resolve?chan=AAAAAAAAAAAAAAAAAAAAAA", nil); r["conv"] != "" {
		t.Fatalf("an unknown channel resolved: %v", r)
	}
	// A changed key is not inherited: Alice's pin now names another key.
	other, _ := identity.Generate()
	otherPub := other.Public(alice.Address)
	w.ok(map[string]any{"op": "tamperPin", "address": alice.Address, "json": publicJSON(t, otherPub), "fingerprint": otherPub.Fingerprint()})
	w.api("/api/notify/mute", map[string]any{"conv": conv, "muted": false})
	if p := prefs()["prefs"].(map[string]any); len(p["senders"].([]any)) != 0 || len(p["mutes"].([]any)) != 0 {
		t.Fatalf("after alice's key changed: %v", p)
	}
	// Off while the relay cannot be reached, then a reload: off here and
	// pending, and the relay is told on reconnect (no later alert can come).
	w.ok(map[string]any{"op": "offline", "on": true})
	w.until("disconnected", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == false })
	off := w.api("/api/notify/disable", map[string]any{})
	if n := w.api("/api/overview", nil)["notify"].(map[string]any); n["enabled"] != false || n["pending"] != true || !strings.Contains(off["note"].(string), "reconnects") {
		t.Fatalf("off while offline: %v (%v)", n, off)
	}
	w.ok(map[string]any{"op": "reload", "base": "https://" + hub.Addr})
	w.ok(map[string]any{"op": "offline", "on": false})
	w.until("the relay told", func() bool { return w.api("/api/overview", nil)["notify"].(map[string]any)["pending"] == false })
	if p := prefs(); p["prefs"].(map[string]any)["enabled"] != false || p["subscribed"] != true {
		t.Fatalf("after reconnecting: %v", p)
	}
	// Off and told: a reconnect writes nothing more.
	calls()
	w.ok(map[string]any{"op": "reload", "base": "https://" + hub.Addr})
	w.until("reconnected", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == true })
	time.Sleep(300 * time.Millisecond)
	for _, c := range calls() {
		if c.Method != "GET" {
			t.Fatalf("a reconnect wrote to the relay with nothing pending: %+v", c)
		}
	}
}

// devicePublic is the browser device's directory entry.
func (w *engineNode) devicePublic(t *testing.T) identity.Public {
	var p identity.Public
	if err := json.Unmarshal([]byte(w.ok(map[string]any{"op": "keys"})["public"].(string)), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// uploadBlob puts ciphertext on the Hub for recipient to, as the client does.
func (r *rawAgent) uploadBlob(to string, ct []byte) envelope.Blob {
	r.t.Helper()
	sum := sha256.Sum256(ct)
	b := envelope.Blob{ID: protocol.NewID(), Size: int64(len(ct)), SHA256: hex.EncodeToString(sum[:])}
	if code, body := r.do("POST", "/v1/blobs", marshalBytes(r.t, protocol.BlobReserve{ID: b.ID, Recipient: to, Size: b.Size, SHA256: b.SHA256}), true); code >= 300 {
		r.t.Fatalf("reserve: %d %s", code, body)
	}
	if code, body := r.do("PUT", "/v1/blobs/"+b.ID+"?offset=0", ct, true); code >= 300 {
		r.t.Fatalf("chunk: %d %s", code, body)
	}
	if code, body := r.do("POST", "/v1/blobs/"+b.ID+"/complete", nil, true); code >= 300 {
		r.t.Fatalf("complete: %d %s", code, body)
	}
	return b
}

// Files in DMs between the browser device and Go (MEL-489): the browser
// encrypts to the recipient before anything leaves it and uploads through
// the Hub's blob API before the message; Go's age decrypts what it sent.
// A file Go sends is decrypted in the browser only if it matches the
// signed manifest; a hostile name is made safe; only raster images are
// images. A file written offline is kept with its message through a
// reload and sent on return.
func TestBrowserEngineFiles(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + hub.Addr
	aliceHome := filepath.Join(t.TempDir(), "alice")
	alice, err := client.Join(ctx, aliceHome, testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	stopAlice := runDaemon(t, alice)
	if _, err := alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	aliceID, err := identity.Load(filepath.Join(aliceHome, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pem, _ := os.ReadFile(filepath.Join(dir, "tls.crt"))
	pool.AppendCertsFromPEM(pem)
	aliceRaw := &rawAgent{t: t, id: aliceID, addr: alice.Address, hub: base,
		http: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 10 * time.Second}}
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	code, _ := alice.Invite(ctx, "dana", time.Hour, false)
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})
	var conv string
	w.until("alice's DM with dana", func() bool { conv, err = alice.CreateDM(ctx, "dana/phone"); return err == nil })
	alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "hi"})
	w.until("the DM in the browser", func() bool { return w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + conv})["error"] == nil })
	file := func(name string, data []byte) map[string]any {
		return map[string]any{"name": name, "b64": base64.StdEncoding.EncodeToString(data)}
	}
	// aliceGot fetches a blob as Alice and decrypts it with her key.
	aliceGot := func(id string) []byte {
		t.Helper()
		code, ct := aliceRaw.do("GET", "/v1/blobs/"+id+"/data", nil, true)
		if code != 200 {
			t.Fatalf("blob %s at the Hub for alice: %d", id, code)
		}
		r, err := age.Decrypt(bytes.NewReader(ct), aliceID.Box)
		if err != nil {
			t.Fatalf("Go cannot decrypt the browser's file: %v", err)
		}
		pt, _ := io.ReadAll(r)
		return pt
	}

	// Browser to Go: a file-only message; nothing to send refused.
	w.refuses("an empty message", w.call(map[string]any{"op": "sendFiles", "conv": conv, "files": []any{}}), "Write a message or add a file")
	notes := []byte("line one\nline two\n")
	sent := w.ok(map[string]any{"op": "sendFiles", "conv": conv, "files": []any{file("notes.txt", notes)}})["v"].(map[string]any)
	rec := w.ok(map[string]any{"op": "outbox", "id": sent["id"]})["rec"].(map[string]any)
	w.until("custody", func() bool {
		rec = w.ok(map[string]any{"op": "outbox", "id": sent["id"]})["rec"].(map[string]any)
		return rec["state"] == "custody" || rec["state"] == "delivered"
	})
	atts := rec["attachments"].([]any)
	if len(atts) != 1 || atts[0].(map[string]any)["name"] != "notes.txt" || rec["files"].([]any)[0].(map[string]any)["ct"] != nil {
		t.Fatalf("the sent file's record: %v", rec)
	}
	blobID := atts[0].(map[string]any)["blob"].(map[string]any)["id"].(string)
	if got := aliceGot(blobID); !bytes.Equal(got, notes) {
		t.Fatalf("alice decrypted %q", got)
	}
	var env envelope.Envelope
	strictJSON([]byte(rec["envelope"].(string)), &env)
	in, err := envelope.Open(env, aliceID, alice.Address, w.devicePublic(t))
	if err != nil || in.Body != "" || len(in.Attachments) != 1 || in.Attachments[0].Name != "notes.txt" || in.Attachments[0].Size != int64(len(notes)) {
		t.Fatalf("the signed manifest as Go opens it: %+v %v", in.Attachments, err)
	}
	if sum := sha256.Sum256(notes); in.Attachments[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("the manifest's plaintext digest is not the file's")
	}
	// The browser device reopens what it sent from the copy it kept for
	// itself (as the Go client does from kept/).
	back := w.ok(map[string]any{"op": "openFile", "id": sent["id"], "i": 0})
	if gotBack, _ := base64.StdEncoding.DecodeString(back["b64"].(string)); !bytes.Equal(gotBack, notes) || back["name"] != "notes.txt" {
		t.Fatalf("a sent file read back: name %v equal %v", back["name"], bytes.Equal(gotBack, notes))
	}
	// The direction is exact: asked as a received message, a sent id opens
	// nothing (never the kept copy); asked as sent, it opens the kept copy.
	w.refuses("a sent id asked as incoming", w.call(map[string]any{"op": "openFile", "id": sent["id"], "i": 0, "dir": "in"}), "No received message")
	if out := w.ok(map[string]any{"op": "openFile", "id": sent["id"], "i": 0, "dir": "out"}); out["name"] != "notes.txt" {
		t.Fatalf("a sent id asked as sent: %v", out["name"])
	}
	for _, d := range w.api("/api/overview", nil)["dms"].([]any) {
		if d := d.(map[string]any); d["id"] == conv && d["last"] != "📎 notes.txt" {
			t.Fatalf("a message of files only in the DM list: %q", d["last"])
		}
	}

	// Go's own client reads the browser's file, checked against the digest
	// the browser signed; a flipped ciphertext byte at the Hub is refused.
	has := func(id string) bool {
		msgs, _ := alice.ConversationMessages(conv)
		for _, m := range msgs {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	w.until("alice holds the browser's file message", func() bool { return has(sent["id"].(string)) })
	rc, info, err := alice.OpenAttachment(ctx, sent["id"].(string), 0)
	if err != nil {
		t.Fatalf("Go opens the browser's file: %v", err)
	}
	got0, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got0, notes) || info.Name != "notes.txt" {
		t.Fatalf("Go read %q as %q", got0, info.Name)
	}
	flip := func(id string) {
		t.Helper()
		p := filepath.Join(dir, "blobs", id+".blob")
		os.Chmod(p, 0o600)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		b[len(b)/2] ^= 1
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Alice's daemon is stopped while the file reaches the Hub and is changed
	// there: a running daemon keeps a received file's ciphertext as soon as it
	// holds the message (prefetchFiles), and a copy kept before the change is
	// the genuine one, so the change would never reach Go.
	stopAlice()
	bad := w.ok(map[string]any{"op": "sendFiles", "conv": conv, "files": []any{file("flip.txt", []byte("flip me"))}})["v"].(map[string]any)
	w.until("the file to flip at the Hub", func() bool {
		r := w.ok(map[string]any{"op": "outbox", "id": bad["id"]})["rec"].(map[string]any)
		return r["state"] == "custody" || r["state"] == "delivered"
	})
	badRec := w.ok(map[string]any{"op": "outbox", "id": bad["id"]})["rec"].(map[string]any)
	flip(badRec["attachments"].([]any)[0].(map[string]any)["blob"].(map[string]any)["id"].(string))
	alice.Close()
	if alice, err = client.Open(aliceHome); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, alice)
	w.until("alice holds the flipped file's message", func() bool { return has(bad["id"].(string)) })
	if rc, _, err := alice.OpenAttachment(ctx, bad["id"].(string), 0); err == nil {
		rc.Close()
		t.Fatal("Go opened a file whose ciphertext was changed at the Hub")
	}
	if left, _ := os.ReadDir(filepath.Join(aliceHome, "opened")); len(left) != 0 {
		t.Fatalf("a refused file left %d opened copies", len(left))
	}

	// Go's own client sends files (SendConv); the browser decrypts them, and
	// refuses one whose ciphertext was changed at the Hub.
	photo := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{9}, 900)...)
	photoPath := filepath.Join(t.TempDir(), "photo.png")
	os.WriteFile(photoPath, photo, 0o600)
	gs, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Files: []client.OutgoingFile{{Name: "photo.png", Path: photoPath}}})
	if err != nil {
		t.Fatalf("Go sends a file: %v", err)
	}
	w.until("Go's file in the browser", func() bool { return w.ok(map[string]any{"op": "inboxRec", "id": gs.ID})["rec"] != nil })
	gp := w.ok(map[string]any{"op": "openFile", "id": gs.ID, "i": 0})
	gotPhoto, _ := base64.StdEncoding.DecodeString(gp["b64"].(string))
	if !bytes.Equal(gotPhoto, photo) || gp["image"] != "image/png" || gp["name"] != "photo.png" {
		t.Fatalf("Go's PNG in the browser: name %v image %v equal %v", gp["name"], gp["image"], bytes.Equal(gotPhoto, photo))
	}
	// Offline while it is sent and changed: the browser fetches (and keeps)
	// nothing before the change.
	w.ok(map[string]any{"op": "offline", "on": true})
	gs2, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "and this", Files: []client.OutgoingFile{{Name: "photo.png", Path: photoPath}}})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := alice.ConversationMessages(conv)
	for _, m := range msgs {
		if m.ID == gs2.ID {
			flip(m.Attachments[0].BlobID)
		}
	}
	w.ok(map[string]any{"op": "offline", "on": false})
	w.until("the flipped Go file in the browser", func() bool { return w.ok(map[string]any{"op": "inboxRec", "id": gs2.ID})["rec"] != nil })
	w.refuses("a Go file changed at the Hub", w.call(map[string]any{"op": "openFile", "id": gs2.ID, "i": 0}), "not the one the sender signed")

	// Go to browser: a PNG with a hostile name, and a file whose manifest
	// lies about its plaintext.
	dPub := w.devicePublic(t)
	drc, _ := dPub.Recipient()
	enc := func(pt []byte) []byte {
		var b bytes.Buffer
		wc, _ := age.Encrypt(&b, drc)
		wc.Write(pt)
		wc.Close()
		return b.Bytes()
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)
	lie := []byte("the real contents")
	pngCT, lieCT := enc(png), enc(lie)
	pngBlob, lieBlob := aliceRaw.uploadBlob("dana/phone", pngCT), aliceRaw.uploadBlob("dana/phone", lieCT)
	pngSum, otherSum := sha256.Sum256(png), sha256.Sum256([]byte("something else"))
	cr := json.RawMessage(w.ok(map[string]any{"op": "convRoot", "conv": conv})["root"].(string))
	in2 := envelope.Inner{V: 2, ID: protocol.NewID(), From: alice.Address, To: "dana/phone", TS: time.Now().Unix(), Kind: "message", Body: "two files",
		Conv: conv, LID: protocol.NewID(), Root: cr, Origin: "ui", Attachments: []envelope.Attachment{
			{Blob: pngBlob, Name: "../../evil<script>.png", Size: int64(len(png)), SHA256: hex.EncodeToString(pngSum[:])},
			{Blob: lieBlob, Name: "report.html", Size: int64(len(lie)), SHA256: hex.EncodeToString(otherSum[:])}}}
	genv, err := envelope.Seal(in2, aliceID.Sign, drc)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := aliceRaw.do("POST", "/v1/messages", marshalBytes(t, genv), true); code >= 300 {
		t.Fatalf("post: %d %s", code, body)
	}
	w.until("the files in the browser", func() bool { return w.ok(map[string]any{"op": "inboxRec", "id": in2.ID})["rec"] != nil })
	got := w.ok(map[string]any{"op": "openFile", "id": in2.ID, "i": 0})
	gotPNG, _ := base64.StdEncoding.DecodeString(got["b64"].(string))
	if !bytes.Equal(gotPNG, png) || got["image"] != "image/png" || got["name"] != "_.._evil_script_.png" {
		t.Fatalf("the Go-sent PNG in the browser: name %v image %v", got["name"], got["image"])
	}
	w.refuses("a file unlike its manifest", w.call(map[string]any{"op": "openFile", "id": in2.ID, "i": 1}), "not the one described")
	view := w.api("/api/dm?id="+conv, nil)["messages"].([]any)
	last := view[len(view)-1].(map[string]any)["attachments"].([]any)
	if len(last) != 2 || last[0].(map[string]any)["name"] != "_.._evil_script_.png" {
		t.Fatalf("the DM view's files: %v", last)
	}

	// Offline, then a reload: the file is kept with its message and sent on return.
	w.ok(map[string]any{"op": "offline", "on": true})
	w.until("disconnected", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == false })
	train := bytes.Repeat([]byte("x"), 700<<10) // more than one upload chunk
	kept := w.ok(map[string]any{"op": "sendFiles", "conv": conv, "body": "from the train", "files": []any{file("big.bin", train)}})["v"].(map[string]any)
	if kept["state"] != "waiting" {
		t.Fatalf("offline send with a file: %v", kept)
	}
	w.ok(map[string]any{"op": "reload", "base": base})
	w.ok(map[string]any{"op": "offline", "on": false})
	w.until("the kept file sent", func() bool {
		r := w.ok(map[string]any{"op": "outbox", "id": kept["id"]})["rec"].(map[string]any)
		return r["state"] == "custody" || r["state"] == "delivered"
	})
	r := w.ok(map[string]any{"op": "outbox", "id": kept["id"]})["rec"].(map[string]any)
	id2 := r["attachments"].([]any)[0].(map[string]any)["blob"].(map[string]any)["id"].(string)
	if !bytes.Equal(aliceGot(id2), train) {
		t.Fatal("the file kept offline is not what alice gets")
	}
}

// The browser with a person on two devices (MEL-433): Alice's laptop links
// her phone; Dana's browser sees one Alice with two devices, its message
// goes to both (one copy each), a reply from the phone and a DM the laptop
// starts both reach it.
func TestBrowserEngineTwoDevicePerson(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + hub.Addr
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
	w.ok(map[string]any{"op": "init", "base": base})
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
	var alice map[string]any
	for _, p := range w.api("/api/overview", nil)["people"].([]any) {
		if p := p.(map[string]any); p["label"] == "Alice" {
			alice = p
		}
	}
	if alice == nil || len(alice["devices"].([]any)) != 2 {
		t.Fatalf("one Alice with two devices: %v", alice)
	}
	// Dana's message: one copy to each of Alice's devices, both delivered.
	sent := w.api("/api/dm/send", map[string]any{"conv": conv, "body": "hi both"})
	if len(sent["copies"].([]any)) != 2 {
		t.Fatalf("copies: %v", sent)
	}
	got := func(a *client.Agent, body string) func() bool {
		return func() bool {
			ms, _ := a.ConversationMessages(conv)
			for _, m := range ms {
				if m.Body == body && m.Dir == "in" {
					return true
				}
			}
			return false
		}
	}
	until("the laptop has it", got(laptop, "hi both"))
	until("the phone has it", got(phone, "hi both"))
	w.until("both copies delivered", func() bool {
		ms := w.api("/api/dm?id="+conv, nil)["messages"].([]any)
		m := ms[len(ms)-1].(map[string]any)
		cs, _ := m["copies"].([]any)
		if len(cs) != 2 {
			return false
		}
		for _, c := range cs {
			if c.(map[string]any)["state"] != "delivered" {
				return false
			}
		}
		return m["state"] == "delivered"
	})
	// A reply from the phone; a DM the laptop starts.
	if _, err := phone.SendConv(ctx, conv, client.ConvOutgoing{Body: "from alice's phone"}); err != nil {
		t.Fatal(err)
	}
	w.until("the phone's reply", func() bool { return strings.Contains(dmBodies(w.api("/api/dm?id="+conv, nil)), "from alice's phone") })
	c2, err := laptop.CreateDM(ctx, "dana/phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.SendConv(ctx, c2, client.ConvOutgoing{Body: "a new DM from the laptop"}); err != nil {
		t.Fatal(err)
	}
	w.until("the laptop's DM", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + c2})
		return v["error"] == nil && strings.Contains(dmBodies(v["v"].(map[string]any)), "a new DM from the laptop")
	})
	// The phone sees Dana's messages in the laptop's DM too (Dana's copies
	// go to both of Alice's devices).
	w.api("/api/dm/send", map[string]any{"conv": c2, "body": "answer to both"})
	until("the phone has Dana's answer in the laptop's DM", func() bool {
		ms, _ := phone.ConversationMessages(c2)
		for _, m := range ms {
			if m.Body == "answer to both" {
				return true
			}
		}
		return false
	})
}
