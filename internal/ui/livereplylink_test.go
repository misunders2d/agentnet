package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// An agent's answer names its executor's copy of the request. When this
// device sent the request, it shows it under another copy's id (the one to
// the other person); the page links the answer to the request as shown
// here, so a UI never has to know copy ids. Bob asks, from his desk, his
// own agent on his phone in his DM with Alice: the desk shows the request
// under Alice's copy, the phone answers its own. The agent is a stand-in
// binary named like a supported harness (no model is called).
func TestLiveAnswerLinksToTheRequestAsShown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\nprintf 'the build is green\\n\\nemotion: calm\\n'\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	desk, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { desk.Close() })
	runDaemon(t, alice)
	runDaemon(t, desk)
	if _, err = alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err = desk.CreatePerson(ctx, "Bob"); err != nil {
		t.Fatal(err)
	}
	offer, err := desk.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := client.JoinAndLink(ctx, t.TempDir(), offer.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	linked := make(chan error, 1)
	go func() { _, e := phone.AwaitLink(ctx); linked <- e }()
	waitFor(t, "the phone's link request", func() bool {
		rows, e := desk.PendingLinks()
		if e != nil || len(rows) == 0 {
			return false
		}
		return desk.DecideLink(ctx, rows[0].ID, true) == nil
	})
	if err = <-linked; err != nil {
		t.Fatal(err)
	}
	runDaemon(t, phone)
	if err := phone.SetResponder(&client.Responder{Harness: "claude", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	pd := NewLive(desk)
	var conv string
	waitFor(t, "a DM with Alice", func() bool { conv, err = pd.NewDM(alice.Address); return err == nil })
	if _, err := pd.SendDM(DMDraft{Conv: conv, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the phone holds the DM", goHas(phone, conv, "hi", nil))
	inv, err := desk.InviteAgent(ctx, conv, phone.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the phone's agent active", func() bool {
		if p, e := phone.Participation(inv.PID); e == nil && p.State == client.PartInvited {
			phone.AcceptParticipation(ctx, inv.PID)
		}
		p, e := desk.Participation(inv.PID)
		return e == nil && p.Claimable()
	})
	asked, err := pd.AskAgent(AgentAsk{PID: inv.PID, Kind: "question", Body: "is the build green?"})
	if err != nil {
		t.Fatal(err)
	}
	var request, answer DMMessage
	waitFor(t, "the answer on the desk", func() bool {
		d, e := pd.DM(conv)
		if e != nil {
			return false
		}
		for _, m := range d.Messages {
			switch {
			case m.Body == "is the build green?":
				request = m
			case m.Kind == envelope.KindAnswer:
				answer = m
			}
		}
		return request.ID != "" && answer.ID != ""
	})
	// As stored, the answer names the phone's copy, not the one shown.
	raw := goMessage(desk, conv, "the build is green")
	if raw.ReplyTo == "" || raw.ReplyTo == request.ID || len(request.Copies) != 2 {
		t.Fatalf("the precondition: the answer names %q, the request (asked as %s) is shown as %s with copies %+v", raw.ReplyTo, asked.ID, request.ID, request.Copies)
	}
	if answer.ReplyTo != request.ID || !answer.VerifiedAgent {
		t.Fatalf("the answer links to %q, the request is shown as %q", answer.ReplyTo, request.ID)
	}
}

// A logical id is unique per sender key only: another member may send a
// message under the logical id of a request it saw. A reply naming that
// logical id then names no one message here, so it stays as sent, while a
// reply naming a copy still links to its message; a logical id one key
// used (on two of its rows) still links.
func TestLinkRepliesThroughAnUnambiguousLogicalIDOnly(t *testing.T) {
	const lid, own, other = "10000000000000000000000000000001", "a1", "b2"
	msgs := []client.ConvMessage{
		{ID: "request", LID: lid, Key: own, Copies: []client.ConvCopy{{ID: "copy-to-bob"}, {ID: "copy-to-phone"}}},
		{ID: "same-lid", LID: lid, Key: other},
		{ID: "own-1", LID: "20000000000000000000000000000002", Key: own},
		{ID: "own-2", LID: "20000000000000000000000000000002", Claimed: own},
		{ID: "answer-by-lid", LID: "3", Key: other, ReplyTo: lid},
		{ID: "answer-by-copy", LID: "4", Key: other, ReplyTo: "copy-to-phone"},
		{ID: "reply-to-own", LID: "5", Key: other, ReplyTo: "20000000000000000000000000000002"},
	}
	out := make([]DMMessage, len(msgs))
	for i, m := range msgs {
		out[i] = DMMessage{ID: m.ID, ReplyTo: m.ReplyTo}
	}
	linkReplies(msgs, out)
	for id, want := range map[string]string{"answer-by-lid": lid, "answer-by-copy": "request", "reply-to-own": "own-2"} {
		for _, m := range out {
			if m.ID == id && m.ReplyTo != want {
				t.Errorf("%s links to %q, want %q", id, m.ReplyTo, want)
			}
		}
	}
}
