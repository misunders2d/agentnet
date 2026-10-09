package ui

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func startGroupWireNode(t *testing.T) *wireNode {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	cmd := exec.Command(node, "testdata/group_wire_check.mjs")
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

// Actual native public rosters, signed group records and standard age ciphertext.
func TestBrowserGroupWireMatchesGo(t *testing.T) {
	w := startGroupWireNode(t)
	setup := w.ok(map[string]any{"op": "setup", "address": "browser/desk"})
	for _, cap := range w.ok(map[string]any{"op": "caps"})["caps"].([]any) {
		if cap == protocol.CapGroup {
			t.Fatal("group capability advertised before Engine support")
		}
	}
	var browser identity.Public
	var browserRoster protocol.PersonRoster
	if json.Unmarshal([]byte(setup["public"].(string)), &browser) != nil || browser.Verify() != nil {
		t.Fatal("browser public invalid")
	}
	if json.Unmarshal([]byte(setup["roster"].(string)), &browserRoster) != nil || browserRoster.VerifyFirst() != nil {
		t.Fatal("browser roster invalid")
	}
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	alicePub, bobPub := alice.Public("alice/desk"), bob.Public("bob/phone")
	ar := protocol.PersonRoster{Person: strings.Repeat("a", 32), Label: "Alice <&> Ю", Devices: []identity.Public{alicePub}}
	ar.Sign(alice.Sign)
	br := protocol.PersonRoster{Person: strings.Repeat("b", 32), Label: "Bob", Devices: []identity.Public{bobPub}}
	br.Sign(bob.Sign)
	rosters := []protocol.PersonRoster{ar, br, browserRoster}
	resolve := func(p, h string) (protocol.PersonRoster, bool) {
		for _, r := range rosters {
			if r.Person == p && r.Hash() == h {
				return r, true
			}
		}
		return protocol.PersonRoster{}, false
	}
	root := protocol.ConvRoot{V: 3, Kind: "group", Creator: protocol.ConvCreator{Person: ar.Person, Roster: ar.Hash(), Address: alicePub.Address, Fingerprint: alicePub.Fingerprint()}, Nonce: protocol.NewID(), Created: 1700000000, Realm: protocol.NewID(), Title: "Group <&> Ю", Admins: []string{ar.Person}}
	for _, r := range rosters {
		root.Members = append(root.Members, protocol.ConvMember{Person: r.Person, Roster: r.Hash()})
	}
	slices.SortFunc(root.Members, func(a, b protocol.ConvMember) int { return strings.Compare(a.Person, b.Person) })
	root.Sign(alice.Sign)
	s0 := protocol.GroupState{V: 1, Conv: root.ID(), Realm: root.Realm, Title: root.Title, Actor: ar.Person, ActorRoster: ar.Hash(), By: alicePub.Fingerprint()}
	for _, m := range root.Members {
		r, _ := resolve(m.Person, m.Roster)
		a := protocol.GroupAdmission{Conv: s0.Conv, Realm: s0.Realm, Person: m.Person, Roster: m.Roster, By: r.Devices[0].Fingerprint()}
		if m.Person == ar.Person {
			a.Sign(alice.Sign)
		} else if m.Person == br.Person {
			a.History = []protocol.GroupHistoryRef{{LID: protocol.NewID(), Author: alicePub.Fingerprint(), Hash: strings.Repeat("d", 64)}}
			a.Sign(bob.Sign)
		} else {
			signed := w.ok(map[string]any{"op": "sign", "type": "admission", "json": marshal(t, a)})
			if json.Unmarshal([]byte(signed["json"].(string)), &a) != nil {
				t.Fatal("browser admission decode")
			}
			if err := a.Verify(resolve); err != nil {
				t.Fatal(err)
			}
		}
		s0.Members = append(s0.Members, protocol.GroupMember{ConvMember: m, Admin: m.Person == ar.Person, Admission: a})
	}
	s0.Sign(alice.Sign)
	if err := s0.Verify(root, nil, resolve, nil); err != nil {
		t.Fatal(err)
	}
	s1 := s0
	s1.Members = slices.Clone(s0.Members)
	s1.Seq = 1
	s1.Prev = s0.Hash()
	s1.Title = "Transferred <&> Ю"
	for i := range s1.Members {
		s1.Members[i].Admin = s1.Members[i].Person == browserRoster.Person
	}
	s1.Sign(alice.Sign)
	if err := s1.Verify(root, &s0, resolve, nil); err != nil {
		t.Fatal(err)
	}
	bobMember, _ := s0.Member(br.Person)
	withdrawal := protocol.GroupWithdrawal{Conv: s0.Conv, Realm: s0.Realm, Person: br.Person, Admission: bobMember.Admission.Hash(), Roster: br.Hash(), By: bobPub.Fingerprint()}
	withdrawal.Sign(bob.Sign)
	packet := client.GroupContext{Root: root, Proof: []protocol.GroupState{s0}, State: s1, Withdrawals: []protocol.GroupWithdrawal{withdrawal}}
	if err := client.VerifyGroupContext(packet, resolve); err != nil {
		t.Fatal(err)
	}
	encrypt := func(raw []byte) []byte {
		t.Helper()
		recipient, _ := browser.Recipient()
		var out bytes.Buffer
		e, err := age.Encrypt(&out, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err = e.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	commit := func(s protocol.GroupState) protocol.GroupCommit {
		return protocol.GroupCommit{Bootstrap: root.Creator.Fingerprint, V: 1, Conv: s.Conv, Realm: s.Realm, Seq: s.Seq, Prev: s.Prev, Hash: s.Hash(), Admins: s.Admins(), Writer: alicePub.Address, Actor: s.Actor, ActorRoster: s.ActorRoster, Ciphertext: encrypt([]byte(marshal(t, client.GroupContext{Root: root, State: s})))}
	}
	c0, c1 := commit(s0), commit(s1)
	c0.Sign(alice.Sign)
	c1.Sign(alice.Sign)
	if err := c0.VerifyChain(root, nil, resolve); err != nil {
		t.Fatal(err)
	}
	if err := c1.VerifyChain(root, &c0, resolve); err != nil {
		t.Fatal(err)
	}
	page := protocol.GroupJournalPage{Records: []protocol.GroupCommit{c0, c1}}
	carrier := protocol.GroupCarrier{V: 1, Seq: 1, Hash: s1.Hash(), ToKey: browser.Fingerprint()}
	w.refuses("group root not admitted by legacy DM parser", w.call(map[string]any{"op": "legacy-root", "json": marshal(t, root)}), "")
	// Go canonical bytes and signatures remain byte-equal in JS, nested signatures included.
	for _, v := range []struct {
		kind      string
		value     any
		canonical []byte
		hash      string
	}{
		{"root", root, root.Canonical(), root.ID()}, {"admission", bobMember.Admission, bobMember.Admission.Canonical(), bobMember.Admission.Hash()},
		{"state", s0, s0.Canonical(), s0.Hash()}, {"state", s1, s1.Canonical(), s1.Hash()}, {"withdrawal", withdrawal, withdrawal.Canonical(), ""}, {"commit", c0, c0.Canonical(), ""}, {"commit", c1, c1.Canonical(), ""},
		{"context", packet, nil, ""}, {"context", client.GroupContext{Root: root, State: s1, Withdrawals: []protocol.GroupWithdrawal{withdrawal}}, nil, ""}, {"journal", page, nil, ""}, {"carrier", carrier, nil, ""},
	} {
		got := w.ok(map[string]any{"op": "record", "type": v.kind, "json": marshal(t, v.value)})
		if got["json"] != marshal(t, v.value) {
			t.Fatal(v.kind, "JSON differs")
		}
		if v.canonical != nil && got["canonical"] != string(v.canonical) {
			t.Fatal(v.kind, "canonical differs")
		}
		if v.hash != "" && got["hash"] != v.hash {
			t.Fatal(v.kind, "hash differs")
		}
	}
	verify := func(kind string, extra map[string]any) map[string]any {
		r := map[string]any{"op": "verify", "type": kind, "root": marshal(t, root), "rosters": rosters}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	w.ok(verify("context", map[string]any{"json": marshal(t, packet)}))
	w.ok(verify("withdrawal", map[string]any{"json": marshal(t, withdrawal), "state": marshal(t, s0)}))
	w.ok(verify("state", map[string]any{"state": marshal(t, s1), "previous": marshal(t, s0), "withdrawals": []string{marshal(t, withdrawal)}}))
	w.ok(verify("proof", map[string]any{"json": marshal(t, page), "realm": root.Realm}))
	w.ok(verify("proof", map[string]any{"json": marshal(t, page), "realm": root.Realm, "previousCommit": marshal(t, c1), "records": []string{marshal(t, c0), marshal(t, c1)}}))
	w.ok(verify("current", map[string]any{"state": marshal(t, s1), "authority": marshal(t, c1), "records": []string{marshal(t, c0)}, "withdrawals": []string{marshal(t, withdrawal)}}))
	// Browser signatures checked by actual native state/withdrawal/commit/root primitives.
	browserMember, _ := s0.Member(browserRoster.Person)
	bw := protocol.GroupWithdrawal{Conv: s0.Conv, Realm: s0.Realm, Person: browserRoster.Person, Admission: browserMember.Admission.Hash(), Roster: browserRoster.Hash(), By: browser.Fingerprint()}
	signed := w.ok(map[string]any{"op": "sign", "type": "withdrawal", "json": marshal(t, bw)})
	json.Unmarshal([]byte(signed["json"].(string)), &bw)
	if err := bw.Verify(s0, resolve); err != nil {
		t.Fatal(err)
	}
	s2 := s1
	s2.Members = slices.Clone(s1.Members)
	s2.Seq = 2
	s2.Prev = s1.Hash()
	s2.Actor = browserRoster.Person
	s2.ActorRoster = browserRoster.Hash()
	s2.By = browser.Fingerprint()
	s2.Title = "Browser <&> Ю"
	signed = w.ok(map[string]any{"op": "sign", "type": "state", "json": marshal(t, s2)})
	json.Unmarshal([]byte(signed["json"].(string)), &s2)
	if signed["canonical"] != string(s2.Canonical()) {
		t.Fatal("browser state canonical differs")
	}
	if err := s2.Verify(root, &s1, resolve, []protocol.GroupWithdrawal{withdrawal}); err != nil {
		t.Fatal(err)
	}
	c2 := commit(s2)
	c2.Writer = browser.Address
	signed = w.ok(map[string]any{"op": "sign", "type": "commit", "json": marshal(t, c2)})
	json.Unmarshal([]byte(signed["json"].(string)), &c2)
	if signed["canonical"] != string(c2.Canonical()) {
		t.Fatal("browser commit canonical differs")
	}
	if err := c2.VerifyChain(root, &c1, resolve); err != nil {
		t.Fatal(err)
	}
	browserRoot := root
	browserRoot.Creator = protocol.ConvCreator{Person: browserRoster.Person, Roster: browserRoster.Hash(), Address: browser.Address, Fingerprint: browser.Fingerprint()}
	browserRoot.Admins = []string{browserRoster.Person}
	signed = w.ok(map[string]any{"op": "sign", "type": "root", "json": marshal(t, browserRoot)})
	json.Unmarshal([]byte(signed["json"].(string)), &browserRoot)
	if err := browserRoot.Verify(browser.SignKey); err != nil {
		t.Fatal(err)
	}
	// >2KiB signed group root parses; DM v2 bytes and 2KiB bound stay unchanged.
	large := root
	large.Members = slices.Clone(root.Members)
	for i := 0; i < 32; i++ {
		large.Members = append(large.Members, protocol.ConvMember{Person: fmt.Sprintf("%032x", i), Roster: strings.Repeat("f", 64)})
	}
	slices.SortFunc(large.Members, func(a, b protocol.ConvMember) int { return strings.Compare(a.Person, b.Person) })
	large.Sign(alice.Sign)
	if len(marshal(t, large)) <= protocol.MaxConvRoot {
		t.Fatal("large fixture too small")
	}
	w.ok(map[string]any{"op": "record", "type": "root", "json": marshal(t, large)})
	dm := protocol.ConvRoot{V: 2, Kind: "dm", Creator: root.Creator, Members: []protocol.ConvMember{{Person: ar.Person, Roster: ar.Hash()}, {Person: br.Person, Roster: br.Hash()}}, Nonce: root.Nonce, Created: root.Created}
	dm.Sign(alice.Sign)
	got := w.ok(map[string]any{"op": "record", "type": "root", "json": marshal(t, dm)})
	if got["canonical"] != string(dm.Canonical()) || got["json"] != marshal(t, dm) {
		t.Fatal("legacy root changed")
	}
	// Exact selected history triples only; no title, membership or LID inference.
	ref := bobMember.Admission.History[0]
	if w.ok(map[string]any{"op": "history", "json": marshal(t, bobMember.Admission), "ref": ref})["allowed"] != true {
		t.Fatal("selected history missing")
	}
	ref.Hash = strings.Repeat("e", 64)
	if w.ok(map[string]any{"op": "history", "json": marshal(t, bobMember.Admission), "ref": ref})["allowed"] != false {
		t.Fatal("foreign history granted")
	}
	// Structurally valid freshly signed counterexamples exercise native/JS authority agreement.
	for name, change := range map[string]func(*protocol.GroupState){
		"last admin": func(s *protocol.GroupState) {
			for i := range s.Members {
				s.Members[i].Admin = false
			}
		},
		"wrong seq": func(s *protocol.GroupState) { s.Seq = 4 }, "wrong prev": func(s *protocol.GroupState) { s.Prev = strings.Repeat("f", 64) },
		"foreign realm": func(s *protocol.GroupState) { s.Realm = protocol.NewID() },
		"nonadmin actor": func(s *protocol.GroupState) {
			s.Actor = br.Person
			s.ActorRoster = br.Hash()
			s.By = bobPub.Fingerprint()
		},
		"silent admission replacement": func(s *protocol.GroupState) {
			for i := range s.Members {
				if s.Members[i].Person == br.Person {
					s.Members[i].Admission.Seq = 1
					s.Members[i].Admission.Prev = s.Prev
					s.Members[i].Admission.Sign(bob.Sign)
				}
			}
		},
		"admin forged consent": func(s *protocol.GroupState) {
			for i := range s.Members {
				if s.Members[i].Person == br.Person {
					s.Members[i].Admission.History = nil
				}
			}
		},
	} {
		bad := s1
		bad.Members = slices.Clone(s1.Members)
		change(&bad)
		if bad.By == bobPub.Fingerprint() {
			bad.Sign(bob.Sign)
		} else {
			bad.Sign(alice.Sign)
		}
		if bad.Verify(root, &s0, resolve, nil) == nil {
			t.Fatal(name, "native accepted")
		}
		w.refuses(name, w.call(verify("state", map[string]any{"state": marshal(t, bad), "previous": marshal(t, s0)})), "")
	}
	// Withdrawn ordinary admission cannot be promoted; fresh consent can rejoin.
	promoted := s1
	promoted.Members = slices.Clone(s1.Members)
	for i := range promoted.Members {
		if promoted.Members[i].Person == br.Person {
			promoted.Members[i].Admin = true
		}
	}
	promoted.Sign(alice.Sign)
	w.refuses("withdrawn promotion", w.call(verify("state", map[string]any{"state": marshal(t, promoted), "previous": marshal(t, s0), "withdrawals": []string{marshal(t, withdrawal)}})), "")
	rejoin := s1
	rejoin.Members = slices.Clone(s1.Members)
	for i := range rejoin.Members {
		if rejoin.Members[i].Person == br.Person {
			rejoin.Members[i].Admission.Seq = 1
			rejoin.Members[i].Admission.Prev = rejoin.Prev
			rejoin.Members[i].Admission.Sign(bob.Sign)
		}
	}
	rejoin.Sign(alice.Sign)
	if err := rejoin.Verify(root, &s0, resolve, []protocol.GroupWithdrawal{withdrawal}); err != nil {
		t.Fatal(err)
	}
	w.ok(verify("state", map[string]any{"state": marshal(t, rejoin), "previous": marshal(t, s0), "withdrawals": []string{marshal(t, withdrawal)}}))
	forgedW := withdrawal
	forgedW.Person = ar.Person
	forgedW.Sign(bob.Sign)
	w.refuses("withdraw other", w.call(verify("withdrawal", map[string]any{"json": marshal(t, forgedW), "state": marshal(t, s0)})), "")
	badC := c1
	badC.Ciphertext = slices.Clone(c1.Ciphertext)
	badC.Ciphertext[0] ^= 1
	w.refuses("ciphertext tamper", w.call(verify("proof", map[string]any{"json": marshal(t, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c0, badC}}), "realm": root.Realm})), "")
	badC = c1
	badC.Hash = strings.Repeat("f", 64)
	badC.Sign(alice.Sign)
	w.refuses("conflicting replay", w.call(verify("proof", map[string]any{"json": marshal(t, protocol.GroupJournalPage{Records: []protocol.GroupCommit{badC}}), "realm": root.Realm, "previousCommit": marshal(t, c1), "records": []string{marshal(t, c0), marshal(t, c1)}})), "")
	badPrefix := c1
	badPrefix.Realm = protocol.NewID()
	w.refuses("foreign proof predecessor", w.call(verify("proof", map[string]any{"json": marshal(t, protocol.GroupJournalPage{}), "realm": root.Realm, "previousCommit": marshal(t, badPrefix)})), "")
	w.refuses("foreign proof realm", w.call(verify("proof", map[string]any{"json": marshal(t, page), "realm": protocol.NewID()})), "")
	w.refuses("missing proof prefix", w.call(verify("proof", map[string]any{"json": marshal(t, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c1}}), "realm": root.Realm})), "")
	w.refuses("missing admission authority", w.call(verify("current", map[string]any{"state": marshal(t, s1), "authority": marshal(t, c1)})), "")
	badC = c1
	badC.Hash = strings.Repeat("f", 64)
	w.refuses("mismatched current authority", w.call(verify("current", map[string]any{"state": marshal(t, s1), "authority": marshal(t, badC), "records": []string{marshal(t, c0)}})), "")
	w.refuses("missing verified roster", w.call(verify("context", map[string]any{"json": marshal(t, packet), "rosters": []protocol.PersonRoster{ar}})), "")
	for _, tc := range []struct{ kind, raw string }{
		{"carrier", `{"v":1,"seq":0,"hash":"` + strings.Repeat("a", 64) + `","to_key":"` + browser.Fingerprint() + `","extra":true}`},
		{"journal", `{"records":[],"more":true}`}, {"root", strings.TrimSuffix(marshal(t, root), "}") + `,"unknown":1}`},
		{"commit", strings.Replace(marshal(t, c0), `"ciphertext":"`, `"ciphertext":"!`, 1)},
	} {
		w.refuses("malformed "+tc.kind, w.call(map[string]any{"op": "record", "type": tc.kind, "json": tc.raw}), "")
	}

	// Independent public append authority refuses a valid signature by an ordinary actor.
	unauthorized := c1
	unauthorized.Actor = br.Person
	unauthorized.ActorRoster = br.Hash()
	unauthorized.Writer = bobPub.Address
	unauthorized.Sign(bob.Sign)
	if unauthorized.VerifyChain(root, &c0, resolve) == nil {
		t.Fatal("native ordinary public append accepted")
	}
	w.refuses("ordinary public append", w.call(verify("proof", map[string]any{"json": marshal(t, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c0, unauthorized}}), "realm": root.Realm})), "")
	// Strict bounds and exact array semantics, including nil versus empty history.
	emptyAdmission := bobMember.Admission
	emptyAdmission.History = []protocol.GroupHistoryRef{}
	emptyAdmission.Sign(bob.Sign)
	got = w.ok(map[string]any{"op": "record", "type": "admission", "json": marshal(t, emptyAdmission)})
	if got["canonical"] != string(emptyAdmission.Canonical()) {
		t.Fatal("empty history bytes changed")
	}
	overAdmission := bobMember.Admission
	overAdmission.History = nil
	for i := 0; i < protocol.MaxGroupHistory+1; i++ {
		overAdmission.History = append(overAdmission.History, protocol.GroupHistoryRef{LID: fmt.Sprintf("%032x", i), Author: alicePub.Fingerprint(), Hash: strings.Repeat("a", 64)})
	}
	w.refuses("history count", w.call(map[string]any{"op": "record", "type": "admission", "json": marshal(t, overAdmission)}), "")
	badCipher := c0
	badCipher.Ciphertext = make([]byte, protocol.MaxGroupCiphertext+1)
	w.refuses("ciphertext bound", w.call(map[string]any{"op": "record", "type": "commit", "json": marshal(t, badCipher)}), "")
	oversizedPage := protocol.GroupJournalPage{}
	for i := 0; i < 17; i++ {
		oversizedPage.Records = append(oversizedPage.Records, c0)
	}
	w.refuses("proof page count", w.call(map[string]any{"op": "record", "type": "journal", "json": marshal(t, oversizedPage)}), "")
	for _, tc := range []struct{ kind, raw string }{
		{"root", marshal(t, root) + strings.Repeat(" ", protocol.MaxGroupRoot)},
		{"root", marshal(t, dm) + strings.Repeat(" ", protocol.MaxConvRoot)},
		{"context", marshal(t, packet) + strings.Repeat(" ", protocol.MaxGroupState)},
		{"carrier", `{"v":1,"seq":9007199254740992,"hash":"` + strings.Repeat("a", 64) + `","to_key":"` + browser.Fingerprint() + `"}`},
	} {
		w.refuses("bounds "+tc.kind, w.call(map[string]any{"op": "record", "type": tc.kind, "json": tc.raw}), "")
	}
	// Quiet carrier envelopes cross real Go/JS signing and age in both directions.
	recipient, _ := browser.Recipient()
	raw := []byte(marshal(t, page))
	ciphertext := encrypt(raw)
	plainHash, cipherHash := sha256.Sum256(raw), sha256.Sum256(ciphertext)
	attachment := envelope.Attachment{Name: envelope.SubGroupProof + ".json", Size: int64(len(raw)), SHA256: hex.EncodeToString(plainHash[:]), Blob: envelope.Blob{ID: protocol.NewID(), Size: int64(len(ciphertext)), SHA256: hex.EncodeToString(cipherHash[:])}}
	in := envelope.Inner{V: 2, ID: protocol.NewID(), From: alicePub.Address, To: browser.Address, TS: 1700000000, Kind: "message", Conv: root.ID(), LID: protocol.NewID(), Root: json.RawMessage(marshal(t, root)), Sub: envelope.SubGroupProof, Body: marshal(t, carrier), Attachments: []envelope.Attachment{attachment}}
	env, err := envelope.Seal(in, alice.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	opened := w.ok(map[string]any{"op": "open", "json": marshal(t, env), "from": marshal(t, alicePub)})
	if opened["sub"] != envelope.SubGroupProof || opened["body"] != in.Body {
		t.Fatal("Go carrier differs")
	}
	in.From = browser.Address
	in.To = alicePub.Address
	back := w.ok(map[string]any{"op": "seal", "inner": in, "to": marshal(t, alicePub)})
	json.Unmarshal([]byte(back["json"].(string)), &env)
	if _, err = envelope.Open(env, alice, alicePub.Address, browser); err != nil {
		t.Fatal(err)
	}

	contextInner := in
	contextInner.Sub = envelope.SubGroupContext
	contextInner.Attachments = slices.Clone(in.Attachments)
	contextInner.Attachments[0].Name = envelope.SubGroupContext + ".json"
	contextInner.Attachments[0].Size = int64(len(marshal(t, client.GroupContext{Root: root, State: s1})))
	contextBack := w.ok(map[string]any{"op": "seal", "inner": contextInner, "to": marshal(t, alicePub)})
	json.Unmarshal([]byte(contextBack["json"].(string)), &env)
	if _, err = envelope.Open(env, alice, alicePub.Address, browser); err != nil {
		t.Fatal(err)
	}
	// Existing visitor proof/context carriers use a valid PID discriminator.
	// It stays opaque protocol context, never an ordinary turn or grant.
	for _, base := range []envelope.Inner{in, contextInner} {
		base.PID, base.From, base.To = protocol.NewID(), alicePub.Address, browser.Address
		pidEnv, e := envelope.Seal(base, alice.Sign, recipient)
		if e != nil {
			t.Fatal(e)
		}
		result := w.ok(map[string]any{"op": "open", "json": marshal(t, pidEnv), "from": marshal(t, alicePub)})
		if result["sub"] != base.Sub || result["body"] != base.Body {
			t.Fatal("PID carrier changed")
		}
		base.From, base.To = browser.Address, alicePub.Address
		result = w.ok(map[string]any{"op": "seal", "inner": base, "to": marshal(t, alicePub)})
		if json.Unmarshal([]byte(result["json"].(string)), &pidEnv) != nil {
			t.Fatal("PID carrier decode")
		}
		if got, e := envelope.Open(pidEnv, alice, alicePub.Address, browser); e != nil || got.PID != base.PID {
			t.Fatalf("PID carrier roundtrip: %v", e)
		}
	}

	for _, tc := range []struct {
		name  string
		inner envelope.Inner
	}{
		{"context attachment bound", contextInner}, {"proof attachment bound", in}, {"group root required", in},
	} {
		bad := tc.inner
		bad.Attachments = slices.Clone(tc.inner.Attachments)
		switch tc.name {
		case "context attachment bound":
			bad.Attachments[0].Size = protocol.MaxGroupState + 1
		case "proof attachment bound":
			bad.Attachments[0].Size = protocol.MaxBody - 1024 + 1
		case "group root required":
			bad.Root = json.RawMessage(marshal(t, dm))
			bad.Conv = dm.ID()
		}
		w.refuses(tc.name, w.call(map[string]any{"op": "seal", "inner": bad, "to": marshal(t, alicePub)}), "")
	}
	w.refuses("explicit empty carrier fan", w.call(map[string]any{"op": "seal", "inner": map[string]any{"v": 2, "id": in.ID, "to": in.To, "ts": in.TS, "kind": in.Kind, "body": in.Body, "conv": in.Conv, "lid": in.LID, "root": root, "sub": in.Sub, "attachments": in.Attachments, "fan": []any{}}, "to": marshal(t, alicePub)}), "")

	for name, change := range map[string]func(*envelope.Inner){"wrong conv": func(x *envelope.Inner) { x.Conv = strings.Repeat("f", 64) }, "question": func(x *envelope.Inner) { x.Kind = "question" }, "replica": func(x *envelope.Inner) { x.Replica = true }, "invalid PID": func(x *envelope.Inner) { x.PID = "invalid" }, "wrong attachment": func(x *envelope.Inner) { x.Attachments[0].Name = "other.json" }, "extra fan": func(x *envelope.Inner) { x.Fan = []envelope.Fan{{Person: ar.Person, Roster: ar.Hash()}} }} {
		bad := in
		bad.Attachments = slices.Clone(in.Attachments)
		change(&bad)
		w.refuses(name, w.call(map[string]any{"op": "seal", "inner": bad, "to": marshal(t, alicePub)}), "")
	}
}
