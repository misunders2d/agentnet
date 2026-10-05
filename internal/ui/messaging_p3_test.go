package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestDeliveryVectorsMatch(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node missing")
	}
	out, err := exec.Command(node, "testdata/messaging_p3_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PASS delivery and receipt checks") {
		t.Fatalf("%v\n%s", err, out)
	}
}
func TestReceivedShowsSentTime(t *testing.T) {
	arrival := time.Unix(1790000000, 0)
	for _, ts := range []int64{0, 253370764800, 1790000001} {
		if got := shownSent(time.Unix(ts, 0), arrival); !got.Equal(arrival) {
			t.Fatalf("invalid claim %d shown as %v", ts, got)
		}
	}
	sent := arrival.Add(-2 * time.Hour)
	if got := shownSent(sent, arrival); !got.Equal(sent) {
		t.Fatal(got)
	}
}
func TestPageWordsNoJargonP3(t *testing.T) {
	words := []string{sentence(&client.NeedsUpdateError{Address: "vitalii/vitalii", Cap: protocol.CapHumanParticipation}), DMStateText("out", "message", "waiting", "Vitalii", "cannot be retried: vitalii/vitalii cannot read hgp1")}
	for _, s := range words {
		for _, bad := range []string{"vitalii/vitalii", "capabilit", "retried", "hgp1"} {
			if strings.Contains(strings.ToLower(s), bad) {
				t.Fatalf("jargon %q in %q", bad, s)
			}
		}
	}
}

func TestGuestCheckRoute(t *testing.T) {
	alice, _, carol, conv, _, _ := liveGuestWorld(t)
	l := NewLive(alice)
	srv := New(l, "127.0.0.1:7777", testToken)
	req := httptest.NewRequest("POST", "http://127.0.0.1:7777/api/dm/guest/check", strings.NewReader(`{"conv":"`+conv+`","host":"`+carol.Address+`"}`))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	req.Header.Set("Origin", "http://127.0.0.1:7777")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var v GuestCheck
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || !v.Ready {
		t.Fatalf("%+v %v", v, err)
	}
	req = httptest.NewRequest("POST", "http://127.0.0.1:7777/api/dm/guest/check", bytes.NewBufferString(`{"conv":"`+conv+`","host":"`+carol.Address+`","grant":true}`))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	req.Header.Set("Origin", "http://127.0.0.1:7777")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("unknown field accepted: %d", w.Code)
	}
	if _, err := l.CheckHuman(context.Background(), GuestCheckRequest{Conv: conv, Host: carol.Address}); err != nil {
		t.Fatal(err)
	}
}
func TestBrowserP3WireFields(t *testing.T) {
	w := startWireNode(t)
	v := w.ok(map[string]any{"op": "setup", "address": "dana/phone"})
	var dana identity.Public
	if err := json.Unmarshal([]byte(v["public"].(string)), &dana); err != nil {
		t.Fatal(err)
	}
	bobID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	bob := bobID.Public("bob/desk")
	recipient, _ := dana.Recipient()
	for _, inner := range []envelope.Inner{{V: 1, ID: protocol.NewID(), Kind: "question", Body: "quote", Quote: protocol.NewID()}, {V: 1, ID: protocol.NewID(), Kind: "answer", ReplyTo: protocol.NewID(), Body: "finished", Status: "done", TopicDone: true}} {
		inner.From = bob.Address
		inner.To = dana.Address
		inner.TS = 1790000000
		env, err := envelope.Seal(inner, bobID.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		got := w.ok(map[string]any{"op": "open", "envelope": marshal(t, env), "from": publicJSON(t, bob)})["inner"].(map[string]any)
		if inner.Quote != "" && got["quote"] != inner.Quote || inner.TopicDone && got["topic_done"] != true {
			t.Fatalf("%v", got)
		}
		inner.From = dana.Address
		inner.To = bob.Address
		reply := w.ok(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": inner})
		var other envelope.Envelope
		if err = json.Unmarshal([]byte(reply["envelope"].(string)), &other); err != nil {
			t.Fatal(err)
		}
		opened, err := envelope.Open(other, bobID, bob.Address, dana)
		if err != nil || opened.Quote != inner.Quote || opened.TopicDone != inner.TopicDone {
			t.Fatalf("%+v %v", opened, err)
		}
	}
	h := client.HistoryItem{V: 1, From: bob.Address, FromKey: bob.Fingerprint(), ID: protocol.NewID(), LID: protocol.NewID(), Kind: "message", Quote: protocol.NewID(), Body: "history quote", At: 1790000000, TS: 1790000000}
	raw := marshal(t, h)
	if got := w.ok(map[string]any{"op": "history", "json": raw})["json"]; got != raw {
		t.Fatalf("history marshal differs:\n%s\n%v", raw, got)
	}
	for _, m := range []map[string]any{{"kind": "answer", "status": "done", "quote": protocol.NewID()}, {"kind": "message", "origin": "agent:codex", "quote": protocol.NewID()}, {"kind": "message", "topic_done": true}} {
		m["id"] = protocol.NewID()
		m["to"] = bob.Address
		m["ts"] = 1790000000
		m["body"] = "invalid"
		if got := w.call(map[string]any{"op": "seal", "to": publicJSON(t, bob), "message": m}); got["error"] == nil {
			t.Fatalf("browser accepted %v", m)
		}
	}
}

func TestLiveP3MessageFields(t *testing.T) {
	alice, _, _, conv, eventually, _ := liveGuestWorld(t)
	live := NewLive(alice)
	parent, err := live.SendDM(DMDraft{Conv: conv, Body: "earlier turn"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = live.SendDM(DMDraft{Conv: conv, Body: "explicit reply", ReplyTo: parent.ID, Quote: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	eventually("native view exposes quote, sent time and delivery", func() bool {
		d, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		var first, reply DMMessage
		for _, m := range d.Messages {
			if m.Body == "earlier turn" {
				first = m
			}
			if m.Body == "explicit reply" {
				reply = m
			}
		}
		if reply.Delivery != "delivered" {
			return false
		}
		if first.ID == "" || reply.Quote != first.ID || reply.SentAt.IsZero() || reply.SentAt.After(reply.At) {
			t.Fatalf("first %+v, reply %+v", first, reply)
		}
		for _, c := range reply.Copies {
			if !c.Own && c.Person != "Bob" {
				t.Fatalf("copy %+v", c)
			}
		}
		return true
	})
}
