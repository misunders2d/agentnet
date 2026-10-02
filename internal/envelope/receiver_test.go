package envelope

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func receiverFixture(t *testing.T) (party, party, ReceiverRoute, ReceiverRequest, ReceiverChoice) {
	t.Helper()
	a, b := newParty(t, "alice/phone"), newParty(t, "alice/laptop")
	request := ReceiverRequest{ID: strings.Repeat("1", 32), From: a.pub.Address, FromKey: a.pub.Fingerprint(), To: "bob/desk", ToKey: b.pub.Fingerprint(), TS: 1, Kind: KindQuestion, Body: "Original human request <>&\u2028"}
	receiver := ReceiverChoice{Kind: "live_session", SessionHandle: "chosen-session", OnClose: &ReceiverOnClose{AgentID: strings.Repeat("2", 32), Instructions: "Original authorized backup", Mode: KindQuestion}}
	route := ReceiverRoute{Op: "delegate", Host: b.pub.Address, HostKey: b.pub.Fingerprint(), RequestRef: request.ID, DelegationID: strings.Repeat("3", 32)}
	var e error
	route.RequestDigest, e = ReceiverDigest(route, request, receiver)
	if e != nil {
		t.Fatal(e)
	}
	return a, b, route, request, receiver
}
func receiverSetup(t *testing.T, route ReceiverRoute, request ReceiverRequest, choice ReceiverChoice, from, to string) Inner {
	t.Helper()
	body, e := json.Marshal(ReceiverOperation{V: 1, Request: &request, Receiver: &choice})
	if e != nil {
		t.Fatal(e)
	}
	return Inner{V: Version, ID: route.DelegationID, From: from, To: to, TS: 1, Kind: KindTask, Body: string(body), ReceiverRoute: &route}
}
func TestReceiverRouteSetupRoundTrip(t *testing.T) {
	a, b, route, request, choice := receiverFixture(t)
	in := receiverSetup(t, route, request, choice, a.pub.Address, b.pub.Address)
	recipient, e := b.pub.Recipient()
	if e != nil {
		t.Fatal(e)
	}
	env, e := Seal(in, a.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	got, e := Open(env, b.id, b.pub.Address, a.pub)
	if e != nil || got.ReceiverRoute == nil || *got.ReceiverRoute != route || got.Body != in.Body {
		t.Fatalf("age setup roundtrip: %+v %v", got, e)
	}
	ready := route
	ready.Op = "ready"
	in = Inner{V: Version, ID: strings.Repeat("4", 32), From: b.pub.Address, To: a.pub.Address, TS: 2, Kind: KindMessage, Body: `{"v":1}`, ReplyTo: route.DelegationID, ReceiverRoute: &ready}
	recipient, _ = a.pub.Recipient()
	env, e = Seal(in, b.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Open(env, a.id, a.pub.Address, b.pub); e != nil {
		t.Fatal(e)
	}
	in.ReplyTo = strings.Repeat("5", 32)
	if _, e = Seal(in, b.id.Sign, recipient); e == nil {
		t.Fatal("ready accepted other delegation ID")
	}
	in = receiverSetup(t, route, request, choice, a.pub.Address, b.pub.Address)
	in.ID = strings.Repeat("5", 32)
	recipient, _ = b.pub.Recipient()
	if _, e = Seal(in, a.id.Sign, recipient); e == nil {
		t.Fatal("delegate accepted other envelope ID")
	}
}
func TestReceiverDigestFrozenCommitment(t *testing.T) {
	_, _, route, request, choice := receiverFixture(t)
	for _, op := range []string{"delegate", "ready", "request"} {
		r := route
		r.Op = op
		d, e := ReceiverDigest(r, request, choice)
		if e != nil || d != route.RequestDigest {
			t.Fatalf("operation normalization %s: %s %v", op, d, e)
		}
	}
	for name, change := range map[string]func(*ReceiverRoute, *ReceiverRequest, *ReceiverChoice){
		"host": func(r *ReceiverRoute, _ *ReceiverRequest, _ *ReceiverChoice) { r.Host = "alice/other" },
		"host-key": func(r *ReceiverRoute, _ *ReceiverRequest, _ *ReceiverChoice) {
			r.HostKey = "SHA256:" + strings.Repeat("a", 64)
		},
		"delegation": func(r *ReceiverRoute, _ *ReceiverRequest, _ *ReceiverChoice) {
			r.DelegationID = strings.Repeat("9", 32)
		},
		"request-text": func(_ *ReceiverRoute, r *ReceiverRequest, _ *ReceiverChoice) { r.Body = "substitution" },
		"recipient-key": func(_ *ReceiverRoute, r *ReceiverRequest, _ *ReceiverChoice) {
			r.ToKey = "SHA256:" + strings.Repeat("b", 64)
		},
		"receiver": func(_ *ReceiverRoute, _ *ReceiverRequest, r *ReceiverChoice) { r.SessionHandle = "other-session" },
	} {
		t.Run(name, func(t *testing.T) {
			r, q, c := route, request, choice
			change(&r, &q, &c)
			d, e := ReceiverDigest(r, q, c)
			if e == nil && d == route.RequestDigest {
				t.Fatal("changed commitment retained digest")
			}
		})
	}
}

func TestReceiverRequestRootAndRecipient(t *testing.T) {
	a, b, _, request, _ := receiverFixture(t)
	for name, change := range map[string]func(*ReceiverRequest){
		"missing-recipient": func(r *ReceiverRequest) { r.To = "" },
		"missing-key":       func(r *ReceiverRequest) { r.ToKey = "" },
		"wrong-target-address": func(r *ReceiverRequest) {
			r.Target = &Target{Address: "bob/other", Fingerprint: r.ToKey, AgentID: strings.Repeat("a", 32)}
		},
		"wrong-target-key": func(r *ReceiverRequest) {
			r.Target = &Target{Address: r.To, Fingerprint: a.pub.Fingerprint(), AgentID: strings.Repeat("a", 32)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := request
			change(&r)
			if r.Validate() == nil {
				t.Fatal("invalid direct recipient commitment accepted")
			}
		})
	}
	root := protocol.ConvRoot{V: 2, Kind: "dm", Creator: protocol.ConvCreator{Person: strings.Repeat("1", 32), Roster: strings.Repeat("a", 64), Address: a.pub.Address, Fingerprint: a.pub.Fingerprint()}, Members: []protocol.ConvMember{{Person: strings.Repeat("1", 32), Roster: strings.Repeat("a", 64)}, {Person: strings.Repeat("2", 32), Roster: strings.Repeat("b", 64)}}, Nonce: strings.Repeat("3", 32), Created: 1}
	root.Sign(a.id.Sign)
	request.Conv, request.LID, request.To, request.ToKey = root.ID(), request.ID, "", ""
	request.Root, _ = json.Marshal(root)
	request.Target = &Target{Address: b.pub.Address, Fingerprint: b.pub.Fingerprint()}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Root = append([]byte(" "), request.Root...)
	if request.Validate() == nil {
		t.Fatal("alternate raw root encoding accepted")
	}
}
func TestReceiverSetupStrictBounds(t *testing.T) {
	a, b, route, request, choice := receiverFixture(t)
	in := receiverSetup(t, route, request, choice, a.pub.Address, b.pub.Address)
	for name, body := range map[string]string{"unknown": strings.Replace(in.Body, `"v":1`, `"v":1,"owner_token":"forbidden"`, 1), "trailing": in.Body + ` {}`, "oversize": strings.Repeat(" ", MaxReceiverSetup+1), "wrong-version": strings.Replace(in.Body, `"v":1`, `"v":2`, 1)} {
		t.Run(name, func(t *testing.T) {
			if _, e := ParseReceiverOperation([]byte(body), route, "", nil); e == nil {
				t.Fatal("invalid setup accepted")
			}
		})
	}
	catalog := ReceiverRoute{Op: "catalog", Host: b.pub.Address, HostKey: b.pub.Fingerprint()}
	if _, e := ParseReceiverOperation([]byte(`{"v":1}`), catalog, "", nil); e != nil {
		t.Fatal(e)
	}
	sessions := ReceiverOperation{V: 1, Sessions: []ReceiverSession{{Handle: "chosen-session", Harness: "codex", Label: "Safe label", Active: true}}}
	body, _ := json.Marshal(sessions)
	if _, e := ParseReceiverOperation(body, catalog, request.ID, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := ParseReceiverOperation(body, catalog, "", nil); e == nil {
		t.Fatal("catalog request accepted response sessions")
	}
	sessions.Sessions = append(sessions.Sessions, sessions.Sessions[0])
	body, _ = json.Marshal(sessions)
	if _, e := ParseReceiverOperation(body, catalog, request.ID, nil); e == nil {
		t.Fatal("duplicate catalog handle accepted")
	}
	if _, e := ParseReceiverOperation([]byte(in.Body), catalog, request.ID, nil); e == nil {
		t.Fatal("catalog accepted delegation")
	}
	choice.OnClose = &ReceiverOnClose{AgentID: strings.Repeat("2", 32), Instructions: strings.Repeat("x", MaxReceiverInstructions+1), Mode: KindTask}
	if choice.Validate() == nil {
		t.Fatal("oversized backup accepted")
	}
}
func TestReceiverDelegateFileCommitment(t *testing.T) {
	a, b, route, request, choice := receiverFixture(t)
	request.Attachments = []Attachment{{Name: "selected.txt", Size: 5, SHA256: strings.Repeat("a", 64)}, {Name: "second.txt", Size: 5, SHA256: strings.Repeat("a", 64)}}
	var e error
	route.RequestDigest, e = ReceiverDigest(route, request, choice)
	if e != nil {
		t.Fatal(e)
	}
	in := receiverSetup(t, route, request, choice, a.pub.Address, b.pub.Address)
	files := append([]Attachment(nil), request.Attachments...)
	for i := range files {
		files[i].Blob = Blob{ID: strings.Repeat(string(rune('4'+i)), 32), Size: 200, SHA256: strings.Repeat("b", 64)}
	}
	if _, e = ParseReceiverOperation([]byte(in.Body), route, "", files); e != nil {
		t.Fatal(e)
	}
	files[0], files[1] = files[1], files[0]
	if _, e = ParseReceiverOperation([]byte(in.Body), route, "", files); e == nil {
		t.Fatal("reordered same-SHA files accepted")
	}
	files = request.Attachments
	if _, e = ParseReceiverOperation([]byte(in.Body), route, "", files); e == nil {
		t.Fatal("delegate accepted plaintext manifests without encrypted blobs")
	}
}
func TestReceiverOriginalAndLegacyBytes(t *testing.T) {
	a, b, route, request, _ := receiverFixture(t)
	route.Op = "request"
	in := Inner{V: Version, ID: request.ID, From: a.pub.Address, To: "bob/desk", TS: 1, Kind: KindQuestion, Body: "Original question", ReceiverRoute: &route}
	if e := ValidateReceiverRoute(in); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{KindAnswer, KindResult} {
		bad := in
		bad.Kind = kind
		if ValidateReceiverRoute(bad) == nil {
			t.Fatal("output route accepted")
		}
	}
	bad := in
	bad.ReceiverRoute = &ReceiverRoute{Op: "request", Host: b.pub.Address, HostKey: b.pub.Fingerprint(), RequestRef: strings.Repeat("8", 32), DelegationID: route.DelegationID, RequestDigest: route.RequestDigest}
	if ValidateReceiverRoute(bad) == nil {
		t.Fatal("wrong request ref accepted")
	}
	bad = in
	bad.V = Version3
	bad.Kind = KindMessage
	bad.Sub = SubReaction
	bad.Ref = &Ref{ID: request.ID, Fingerprint: a.pub.Fingerprint()}
	bad.Body = `{"emoji":"👍","op":"add","n":1}`
	if ValidateControl(bad) == nil {
		t.Fatal("standalone control validator accepted receiver route")
	}
	legacy := Inner{V: Version, ID: "m1", From: "alice/a", To: "bob/b", TS: 1, Kind: KindMessage, Body: "hello"}
	body, e := json.Marshal(legacy)
	if e != nil || string(body) != `{"v":1,"id":"m1","from":"alice/a","to":"bob/b","ts":1,"kind":"message","body":"hello"}` {
		t.Fatalf("legacy bytes changed: %s %v", body, e)
	}
}
