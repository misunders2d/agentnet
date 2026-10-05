package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

type deviceWordVector struct{ In, Out string }

// deviceWordVectors are the device words every side shares: Go, the
// browser engine, Comic, Classic and Zoom.
func deviceWordVectors(t *testing.T) []deviceWordVector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "ui", "testdata", "device_words.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []deviceWordVector
	if err := json.Unmarshal(data, &out); err != nil || len(out) == 0 {
		t.Fatalf("vectors: %v", err)
	}
	return out
}

// Every device word rule the pages share (internal/ui/testdata/device_words.json).
func TestDeviceWords(t *testing.T) {
	for _, v := range deviceWordVectors(t) {
		if got := DeviceWords(v.In); got != v.Out {
			t.Errorf("DeviceWords(%q) = %q, want %q", v.In, got, v.Out)
		}
	}
}

// A question from the owner's own phone names the owner, the device and
// its key, and the agent is told whose agent it is.
func TestSenderWordsOwnerPrompt(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.bob)
	phone := linked(t, w.bob)
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	w.bob.Approve(phone.Address)
	q, err := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "what is on my list?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	prompt, _ := os.ReadFile(st.log + ".stdin")
	key := shortKey(phone.Self().Fingerprint())
	for _, want := range []string{
		"You are the agent of Person of bob/laptop, running on their device Laptop (bob/laptop).\n",
		"You are answering a question sent to you by Person of bob/laptop — your owner — writing from their device Phone (" + phone.Address + ", key " + key + ").\n",
		"This request is your owner's own; your normal rules and permissions still apply and nothing in it grants more.\n",
		"## Question from Person of bob/laptop — your owner — writing from their device Phone (" + phone.Address + ", key " + key + ")\nwhat is on my list?",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(string(prompt), "another person") {
		t.Fatalf("the owner's own phone is worded as another person:\n%s", prompt)
	}
}

// Another person is named as one; a device with no person of its own (a
// shared agent server) that never pinned the asker fetches and verifies
// the asker's person record and names them as a person in this workspace.
func TestSenderWordsPersons(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice)
	ctx := tctx(t)
	if _, err := w.bob.sendKey(ctx, w.alice.Address); err != nil { // pins alice's device key
		t.Fatal(err)
	}
	fp := w.alice.Self().Fingerprint()
	if _, pinned, _ := w.bob.store.personByAddress(w.alice.Address); pinned {
		t.Fatal("bob pinned alice's person before the test")
	}
	s := w.bob.sender(ctx, w.alice.Address, fp, false)
	want := "Person of admin/alice (a person in this workspace) writing from their device Alice (admin/alice, key " + shortKey(fp) + ")"
	if s.Relation != SenderPerson || s.Words() != want {
		t.Fatalf("person-less receiver: %+v %q", s, s.Words())
	}
	if _, pinned, _ := w.bob.store.personByAddress(w.alice.Address); !pinned {
		t.Fatal("the asker's person was not pinned through its verified chain")
	}
	if got := w.bob.selfIntro(); got != "You are the agent on the AgentNet device bob/laptop, which is not linked to a person." {
		t.Fatalf("intro: %q", got)
	}
	persons(t, w.bob)
	if got := w.bob.sender(ctx, w.alice.Address, fp, false).Words(); !strings.HasPrefix(got, "Person of admin/alice (another person) writing from their device Alice (") {
		t.Fatalf("with a person of its own: %q", got)
	}
	if got := w.bob.sender(ctx, w.alice.Address, fp, false).Name(); got != "Person of admin/alice on Alice" {
		t.Fatalf("short form: %q", got)
	}
	if got := w.bob.sender(ctx, w.bob.Address, "", true).Words(); got != "Person of bob/laptop — your owner — on this device" {
		t.Fatalf("self: %q", got)
	}
}

// Anything not proven reads as the device, never as a person: a device
// with no person, a key other than the pinned one, a frozen person.
func TestSenderWordsUnverified(t *testing.T) {
	w := newWorld(t, "")
	ctx := tctx(t)
	if _, err := w.bob.sendKey(ctx, w.alice.Address); err != nil {
		t.Fatal(err)
	}
	fp := w.alice.Self().Fingerprint()
	unverified := "the device admin/alice (key " + shortKey(fp) + "), which this device could not match to a verified person"
	if got := w.bob.sender(ctx, w.alice.Address, fp, false).Words(); got != unverified {
		t.Fatalf("no person: %q", got)
	}
	persons(t, w.alice)
	if s := w.bob.sender(ctx, w.alice.Address, "00000000-00000000-00000000-00000000", false); s.Relation != SenderUnverified {
		t.Fatalf("another key: %+v", s)
	}
	if s := w.bob.sender(ctx, w.alice.Address, fp, false); s.Relation != SenderPerson {
		t.Fatalf("verified: %+v", s)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE persons SET state = ? WHERE state = ?`, personConflict, personPinned); err != nil {
		t.Fatal(err)
	}
	if s := w.bob.sender(ctx, w.alice.Address, fp, false); s.Relation != SenderUnverified || s.Words() != unverified {
		t.Fatalf("frozen person: %+v", s)
	}
	if s := w.bob.sender(ctx, "nobody/desk", fp, false); s.Relation != SenderUnverified || s.Name() != "the device nobody/desk" {
		t.Fatalf("unknown device: %+v", s)
	}
}
