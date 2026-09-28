package ui

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The vendored age library is exactly what webvendor/build.sh makes from the
// pinned package graph.
func TestVendoredAgeMatchesRecipe(t *testing.T) {
	data, err := os.ReadFile("static/vendor/age.mjs")
	if err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile("webvendor/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if want := hex.EncodeToString(sum[:]) + "  age.mjs\n"; string(sums) != want {
		t.Fatalf("static/vendor/age.mjs does not match webvendor/SHA256SUMS: run webvendor/build.sh")
	}
	var pkg struct{ Dependencies, DevDependencies map[string]string }
	raw, err := os.ReadFile("webvendor/package.json")
	if err != nil || json.Unmarshal(raw, &pkg) != nil {
		t.Fatalf("package.json: %v", err)
	}
	exact := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	for name, v := range pkg.Dependencies {
		if !exact.MatchString(v) {
			t.Errorf("%s %q is not an exact version", name, v)
		}
	}
	for name, v := range pkg.DevDependencies {
		if !exact.MatchString(v) {
			t.Errorf("%s %q is not an exact version", name, v)
		}
	}
}

// wireNode is static/wire.mjs running in node with a device's real keys
// (testdata/wire_check.mjs): one JSON request and answer per line.
type wireNode struct {
	t      *testing.T
	in     io.WriteCloser
	out    *bufio.Scanner
	stderr *bytes.Buffer
}

func startWireNode(t *testing.T) *wireNode {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command(node, "testdata/wire_check.mjs")
	w := &wireNode{t: t, stderr: &bytes.Buffer{}}
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
	t.Cleanup(func() { w.in.Close(); cmd.Wait() })
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 0, 1<<20), 8<<20)
	return w
}

// raw sends one request line as it is.
func (w *wireNode) raw(line string) map[string]any {
	w.t.Helper()
	if _, err := io.WriteString(w.in, line+"\n"); err != nil {
		w.t.Fatal(err)
	}
	if !w.out.Scan() {
		w.t.Fatalf("no answer from node: %v\n%s", w.out.Err(), w.stderr)
	}
	var v map[string]any
	if err := json.Unmarshal(w.out.Bytes(), &v); err != nil {
		w.t.Fatal(err)
	}
	return v
}

func (w *wireNode) call(req map[string]any) map[string]any {
	w.t.Helper()
	line, err := json.Marshal(req)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.raw(string(line))
}

// ok is call for a request that must succeed.
func (w *wireNode) ok(req map[string]any) map[string]any {
	w.t.Helper()
	v := w.call(req)
	if e, ok := v["error"]; ok {
		w.t.Fatalf("%v: %v", req["op"], e)
	}
	return v
}

// refuses checks that a request fails with an error mentioning want.
func (w *wireNode) refuses(what string, v map[string]any, want string) {
	w.t.Helper()
	e, _ := v["error"].(string)
	if e == "" || !strings.Contains(e, want) {
		w.t.Errorf("%s: want an error with %q, got %v", what, want, v)
	}
}

func strictJSON(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func publicJSON(t *testing.T, p identity.Public) string {
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// signEnvelope signs e as the Go client does (envelope.Seal), for envelopes
// a test puts together itself.
func signEnvelope(e *envelope.Envelope, key ed25519.PrivateKey) {
	e.Sig = nil
	data, _ := json.Marshal(e)
	e.Sig = ed25519.Sign(key, append([]byte("agentnet-envelope-v1\n"), data...))
}

func encryptTo(t *testing.T, plain []byte, r age.Recipient) []byte {
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, r)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(plain)
	w.Close()
	return ct.Bytes()
}

// The browser device's wire code agrees with the Go code in both directions:
// what it signs and seals the Go client and Hub accept, what the Go client
// seals it opens, and it refuses what the Go code refuses. Its keys are real
// device keys that cannot be exported, also after a structured-clone copy
// (how IndexedDB keeps them across reloads).
func TestBrowserWireMatchesGo(t *testing.T) {
	w := startWireNode(t)
	if m := w.ok(map[string]any{"op": "support"})["missing"].([]any); len(m) > 0 {
		t.Skipf("this node lacks %v", m)
	}

	// The device and its directory entry.
	const dana = "dana/phone"
	setup := w.ok(map[string]any{"op": "setup", "address": dana})
	var pub identity.Public
	if err := strictJSON([]byte(setup["public"].(string)), &pub); err != nil {
		t.Fatal(err)
	}
	if err := pub.Verify(); err != nil || pub.Address != dana {
		t.Fatalf("directory entry: %v %+v", err, pub)
	}
	if setup["public"] != publicJSON(t, pub) || setup["fingerprint"] != pub.Fingerprint() {
		t.Fatalf("entry or fingerprint differ from Go: %v", setup)
	}
	if setup["extractable"] != false || setup["export_refused"] != true {
		t.Fatalf("device keys can be exported: %v", setup)
	}
	danaRecipient, err := pub.Recipient()
	if err != nil {
		t.Fatal(err)
	}

	bobID, _ := identity.Generate()
	bob := bobID.Public("bob/desk")
	eveID, _ := identity.Generate()
	eve := eveID.Public("eve/lab")

	t.Run("json strings as Go writes them", func(t *testing.T) {
		in := []string{"", "plain", `<script>alert("x")</script> & 'y'`, `back\slash`, "\b\f\n\r\t", "\x00\x01\x1f\x7f",
			"\u2028 and \u2029", "emoji 😀 é ñ 中文", "\ufeffbom", "é", "\U0010ffff"}
		got := w.ok(map[string]any{"op": "goString", "strings": in})["json"].([]any)
		for i, s := range in {
			want, _ := json.Marshal(s)
			if got[i] != string(want) {
				t.Errorf("%q: JS %v, Go %s", s, got[i], want)
			}
		}
		// A lone surrogate has no UTF-8 form: refused, not changed.
		lone := w.raw(`{"op":"goString","strings":["a\ud800b"]}`)["json"].([]any)
		if !strings.HasPrefix(lone[0].(string), "refused") {
			t.Errorf("lone surrogate: %v", lone[0])
		}
	})

	t.Run("join", func(t *testing.T) {
		secret := "s3cret<&>\"\\ \u2028 😀"
		body := w.ok(map[string]any{"op": "join", "secret": secret})["body"].(string)
		var jr protocol.JoinRequest
		if err := strictJSON([]byte(body), &jr); err != nil {
			t.Fatal(err)
		}
		if err := protocol.VerifyJoin(jr); err != nil || jr.Secret != secret || publicJSON(t, jr.Public) != setup["public"] {
			t.Fatalf("join: %v %+v", err, jr)
		}
		jr.Secret += "x"
		if protocol.VerifyJoin(jr) == nil {
			t.Fatal("a changed join request verified")
		}
	})

	t.Run("signed requests", func(t *testing.T) {
		keyFor := func(agent string) (ed25519.PublicKey, error) {
			if agent != dana {
				return nil, fmt.Errorf("unknown agent")
			}
			return pub.SignKey, nil
		}
		for _, c := range []struct{ method, target, body string }{
			{"POST", "/v1/messages?x=1", `{"a":"é <&>"}`},
			{"GET", "/v1/stream?ad=abc", ""},
		} {
			h := w.ok(map[string]any{"op": "request", "method": c.method, "target": c.target, "body": c.body})["headers"].(map[string]any)
			check := func(target, body string) error {
				r := httptest.NewRequest(c.method, target, strings.NewReader(body))
				for k, v := range h {
					r.Header.Set(k, v.(string))
				}
				sr, err := protocol.ReadSignedRequest(r, keyFor)
				if err == nil && (sr.Agent != dana || string(sr.Body) != body) {
					err = fmt.Errorf("read %+v", sr)
				}
				return err
			}
			if err := check(c.target, c.body); err != nil {
				t.Errorf("%s %s: %v", c.method, c.target, err)
			}
			if check(c.target+"&y=2", c.body) == nil || check(c.target, c.body+" ") == nil {
				t.Errorf("%s %s: a changed request verified", c.method, c.target)
			}
		}
	})

	t.Run("session ad", func(t *testing.T) {
		session := protocol.NewID()
		ad, err := protocol.DecodeAd(w.ok(map[string]any{"op": "ad", "session": session})["ad"].(string))
		if err != nil || ad.Address != dana || ad.Session != session || ad.Endpoint != "" || ad.Verify(pub.SignKey) != nil {
			t.Fatalf("ad: %v %+v", err, ad)
		}
		ad.Session = protocol.NewID()
		if ad.Verify(pub.SignKey) == nil {
			t.Fatal("a changed ad verified")
		}
		w.refuses("bad session", w.call(map[string]any{"op": "ad", "session": "nothex"}), "invalid session")
	})

	t.Run("invites", func(t *testing.T) {
		inv := protocol.Invite{Hub: "https://relay.example:8443", Label: "dana", Secret: "sek<&>ret"}
		got := w.ok(map[string]any{"op": "invite", "code": inv.Encode()})["invite"].(map[string]any)
		if got["hub"] != inv.Hub || got["label"] != inv.Label || got["secret"] != inv.Secret || got["cert"] != "" {
			t.Fatalf("invite: %v", got)
		}
		for _, bad := range []protocol.Invite{
			{Hub: "http://relay.example", Label: "dana", Secret: "s"},
			{Hub: "https://relay.example/path", Label: "dana", Secret: "s"},
			{Hub: "https://relay.example", Label: "Dana", Secret: "s"},
			{Hub: "https://relay.example", Label: "dana"},
		} {
			_, goErr := protocol.DecodeInvite(bad.Encode())
			v := w.call(map[string]any{"op": "invite", "code": bad.Encode()})
			if goErr == nil || v["error"] == nil {
				t.Errorf("%+v: Go %v, JS %v", bad, goErr, v)
			}
		}
		w.refuses("not an invite", w.call(map[string]any{"op": "invite", "code": "hello"}), "not an AgentNet invite")
	})

	t.Run("directory entries", func(t *testing.T) {
		got := w.ok(map[string]any{"op": "public", "public": publicJSON(t, bob)})
		if got["address"] != "bob/desk" || got["fingerprint"] != bob.Fingerprint() {
			t.Fatalf("entry: %v, want %s", got, bob.Fingerprint())
		}
		flipped := bob
		flipped.BoxSig = bytes.Clone(bob.BoxSig)
		flipped.BoxSig[0] ^= 1
		w.refuses("flipped binding", w.call(map[string]any{"op": "public", "public": publicJSON(t, flipped)}), "not signed")
		swapped := bob
		swapped.BoxRecipient = eve.BoxRecipient
		w.refuses("other encryption key", w.call(map[string]any{"op": "public", "public": publicJSON(t, swapped)}), "not signed")
		short := bob
		short.SignKey = bob.SignKey[:31]
		w.refuses("short key", w.call(map[string]any{"op": "public", "public": publicJSON(t, short)}), "bad signing key")
		extra := strings.TrimSuffix(publicJSON(t, bob), "}") + `,"admin":true}`
		w.refuses("unknown field", w.call(map[string]any{"op": "public", "public": extra}), "unknown field")
	})

	// seal asks the device to send m to bob, and opens it as the Go client.
	seal := func(t *testing.T, m map[string]any) (envelope.Envelope, envelope.Inner) {
		t.Helper()
		v := w.ok(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": m})
		raw := v["envelope"].(string)
		var env envelope.Envelope
		if err := strictJSON([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		if canonical, _ := json.Marshal(env); string(canonical) != raw {
			t.Errorf("envelope is not in Go's form:\n%s\n%s", raw, canonical)
		}
		if err := env.VerifySig(pub.SignKey); err != nil {
			t.Fatalf("the Hub would refuse it: %v", err)
		}
		in, err := envelope.Open(env, bobID, "bob/desk", pub)
		if err != nil {
			t.Fatalf("the Go client cannot open it: %v", err)
		}
		return env, in
	}
	now := time.Now().Unix()
	blob := func(n byte) map[string]any {
		return map[string]any{"id": strings.Repeat(string('a'+n), 32), "size": 100 + int(n), "sha256": strings.Repeat("0", 64)}
	}

	t.Run("device to Go", func(t *testing.T) {
		body := "hi <b>&amp;</b> \"q\" \\ \u2028 😀 \x00 é"
		id := protocol.NewID()
		env, in := seal(t, map[string]any{"id": id, "to": "bob/desk", "ts": now, "kind": "message", "body": body})
		if in.Body != body || in.ID != id || in.Kind != "message" || in.TS != now || env.From != dana {
			t.Fatalf("opened %+v", in)
		}
		parent, session := protocol.NewID(), protocol.NewID()
		_, in = seal(t, map[string]any{"id": protocol.NewID(), "to": "bob/desk", "ts": now, "kind": "answer", "body": "4",
			"reply_to": parent, "status": "done", "session": session, "fallback": true,
			"attachments": []any{
				map[string]any{"blob": blob(0), "name": "log <1>.txt", "size": 5, "sha256": strings.Repeat("f", 64)},
				map[string]any{"blob": blob(1), "name": "b", "size": 0, "sha256": strings.Repeat("e", 64)},
			}})
		if in.ReplyTo != parent || in.Status != "done" || in.Session != session || !in.Fallback || len(in.Attachments) != 2 ||
			in.Attachments[0].Name != "log <1>.txt" || in.Attachments[1].Blob.Size != 101 {
			t.Fatalf("opened %+v", in)
		}
		// Size: what fits is sent, what the Go client would refuse is refused.
		_, in = seal(t, map[string]any{"id": protocol.NewID(), "to": "bob/desk", "ts": now, "kind": "message", "body": ""})
		for _, n := range []int{250_000, envelope.MaxCiphertext} {
			v := w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob), "body_bytes": n,
				"message": map[string]any{"id": protocol.NewID(), "to": "bob/desk", "ts": now, "kind": "message"}})
			_, goErr := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: dana, To: "bob/desk", TS: now, Kind: "message",
				Body: strings.Repeat("x", n)}, bobID.Sign, danaRecipient)
			if (v["error"] == nil) != (goErr == nil) {
				t.Errorf("%d bytes: JS %v, Go %v", n, v["error"], goErr)
			}
		}
		for what, c := range map[string]struct {
			m    map[string]any
			want string
		}{
			"kind":        {map[string]any{"id": protocol.NewID(), "to": "bob/desk", "ts": now, "kind": "run", "body": "x"}, "unknown message kind"},
			"other":       {map[string]any{"id": protocol.NewID(), "to": "eve/lab", "ts": now, "kind": "message", "body": "x"}, "does not belong"},
			"bad id":      {map[string]any{"id": "x", "to": "bob/desk", "ts": now, "kind": "message", "body": "x"}, "invalid message header"},
			"attachments": {map[string]any{"id": protocol.NewID(), "to": "bob/desk", "ts": now, "kind": "message", "body": "x", "attachments": nineAttachments(blob)}, "too many"},
		} {
			w.refuses(what, w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": c.m}), c.want)
		}
		lone := fmt.Sprintf(`{"op":"seal","to":%q,"message":{"id":%q,"to":"bob/desk","ts":%d,"kind":"message","body":"a\ud800"}}`,
			publicJSON(t, bob), protocol.NewID(), now)
		w.refuses("lone surrogate", w.raw(lone), "not valid text")
	})

	// open asks the device to open what bob sent.
	open := func(env envelope.Envelope, from identity.Public) map[string]any {
		data, _ := json.Marshal(env)
		return w.call(map[string]any{"op": "open", "envelope": string(data), "from": publicJSON(t, from)})
	}
	goSeal := func(t *testing.T, in envelope.Inner, key ed25519.PrivateKey, r age.Recipient) envelope.Envelope {
		t.Helper()
		env, err := envelope.Seal(in, key, r)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	inner := func(body string) envelope.Inner {
		return envelope.Inner{ID: protocol.NewID(), From: "bob/desk", To: dana, TS: now, Kind: "question", Body: body}
	}

	t.Run("Go to device", func(t *testing.T) {
		body := "which port? <script>&</script> \u2028 \u2029 😀 \x00 \t"
		in := inner(body)
		in.ReplyTo = protocol.NewID()
		in.Attachments = []envelope.Attachment{{Blob: envelope.Blob{ID: protocol.NewID(), Size: 9, SHA256: strings.Repeat("a", 64)},
			Name: "a <b>.txt", Size: 3, SHA256: strings.Repeat("b", 64)}}
		v := open(goSeal(t, in, bobID.Sign, danaRecipient), bob)
		got, _ := v["inner"].(map[string]any)
		if got == nil || got["body"] != body || got["id"] != in.ID || got["reply_to"] != in.ReplyTo || got["kind"] != "question" {
			t.Fatalf("opened %v", v)
		}
		if a := got["attachments"].([]any)[0].(map[string]any); a["name"] != "a <b>.txt" || a["blob"].(map[string]any)["id"] != in.Attachments[0].Blob.ID {
			t.Fatalf("attachment %v", a)
		}
		// The largest message the Go client sends.
		big := inner(strings.Repeat("y", 250_000))
		if v := open(goSeal(t, big, bobID.Sign, danaRecipient), bob); v["error"] != nil {
			t.Fatalf("large message: %v", v["error"])
		}
	})

	t.Run("device refuses what Go refuses", func(t *testing.T) {
		env := goSeal(t, inner("x"), bobID.Sign, danaRecipient)
		flipped := env
		flipped.Sig = bytes.Clone(env.Sig)
		flipped.Sig[0] ^= 1
		w.refuses("flipped signature", open(flipped, bob), "signature invalid")

		w.refuses("signed by another key", open(goSeal(t, inner("x"), eveID.Sign, danaRecipient), bob), "signature invalid")

		eveRecipient, _ := eve.Recipient()
		w.refuses("encrypted to another key", open(goSeal(t, inner("x"), bobID.Sign, eveRecipient), bob), "decrypt")

		other := inner("x")
		other.To = "dana/other"
		w.refuses("for another agent", open(goSeal(t, other, bobID.Sign, danaRecipient), bob), "addressed to another agent")

		w.refuses("sender entry of someone else", open(env, eve), "does not belong")

		// Inner and outer disagree, each validly encrypted and signed.
		mismatch := func(what string, change func(*envelope.Inner), want string) {
			in := inner("x")
			bad := in
			change(&bad)
			plain, _ := json.Marshal(bad)
			e := envelope.Envelope{V: envelope.Version, ID: in.ID, From: in.From, To: in.To, TS: in.TS, Kind: in.Kind,
				CT: encryptTo(t, plain, danaRecipient)}
			signEnvelope(&e, bobID.Sign)
			w.refuses(what, open(e, bob), want)
		}
		mismatch("inner id", func(n *envelope.Inner) { n.V = envelope.Version; n.ID = protocol.NewID() }, "does not match")
		mismatch("inner kind", func(n *envelope.Inner) { n.V = envelope.Version; n.Kind = "task" }, "does not match")
		mismatch("inner from", func(n *envelope.Inner) { n.V = envelope.Version; n.From = "eve/lab" }, "does not match")
		mismatch("inner files", func(n *envelope.Inner) {
			n.V = envelope.Version
			n.Attachments = []envelope.Attachment{{Blob: envelope.Blob{ID: protocol.NewID(), Size: 1, SHA256: strings.Repeat("a", 64)}, SHA256: strings.Repeat("a", 64)}}
		}, "manifest")

		// An inner field Go's strict decoding refuses: the same envelope
		// shape, once to the device and once to a Go client.
		unknownInner := func(to string, r age.Recipient) envelope.Envelope {
			in := inner("x")
			in.V, in.To = envelope.Version, to
			plain, _ := json.Marshal(in)
			plain = append(plain[:len(plain)-1], []byte(`,"run":true}`)...)
			e := envelope.Envelope{V: envelope.Version, ID: in.ID, From: in.From, To: in.To, TS: in.TS, Kind: in.Kind,
				CT: encryptTo(t, plain, r)}
			signEnvelope(&e, bobID.Sign)
			return e
		}
		eveR, _ := eve.Recipient()
		if _, err := envelope.Open(unknownInner("eve/lab", eveR), eveID, "eve/lab", bob); err == nil {
			t.Fatal("Go opened an inner with an unknown field")
		}
		w.refuses("unknown inner field", open(unknownInner(dana, danaRecipient), bob), "unknown field")

		data, _ := json.Marshal(env)
		extra := strings.TrimSuffix(string(data), "}") + `,"admin":true}`
		w.refuses("unknown outer field", w.call(map[string]any{"op": "open", "envelope": extra, "from": publicJSON(t, bob)}), "unknown field")
		short := env
		short.CT = []byte("0123456789") // encodes with padding
		signEnvelope(&short, bobID.Sign)
		data, _ = json.Marshal(short)
		unpadded := strings.Replace(string(data), `==",`, `",`, 1)
		if err := json.Unmarshal([]byte(unpadded), &envelope.Envelope{}); unpadded == string(data) || err == nil {
			t.Fatalf("Go accepts unpadded base64 (%v)", err)
		}
		w.refuses("unpadded base64", w.call(map[string]any{"op": "open", "envelope": unpadded, "from": publicJSON(t, bob)}), "base64")

		v2 := env
		v2.V = 2
		signEnvelope(&v2, bobID.Sign)
		w.refuses("other version", open(v2, bob), "unsupported envelope version")

		huge := env
		huge.CT = make([]byte, envelope.MaxCiphertext+1)
		rand.Read(huge.CT)
		signEnvelope(&huge, bobID.Sign)
		if huge.VerifySig(bob.SignKey) == nil {
			t.Fatal("Go accepts an oversized envelope")
		}
		w.refuses("too large", open(huge, bob), "too large")
	})

	t.Run("keys after a reload", func(t *testing.T) {
		again := w.ok(map[string]any{"op": "reload"})
		if again["public"] != setup["public"] || again["extractable"] != false || again["export_refused"] != true {
			t.Fatalf("after structured cloning: %v", again)
		}
		_, in := seal(t, map[string]any{"id": protocol.NewID(), "to": "bob/desk", "ts": now, "kind": "message", "body": "after reload"})
		if in.Body != "after reload" {
			t.Fatalf("opened %+v", in)
		}
		if v := open(goSeal(t, inner("to the reloaded device"), bobID.Sign, danaRecipient), bob); v["error"] != nil {
			t.Fatalf("open after reload: %v", v["error"])
		}
	})
}

func nineAttachments(blob func(byte) map[string]any) []any {
	var out []any
	for i := byte(0); i < 9; i++ {
		out = append(out, map[string]any{"blob": blob(i), "name": "f", "size": 1, "sha256": strings.Repeat("0", 64)})
	}
	return out
}
