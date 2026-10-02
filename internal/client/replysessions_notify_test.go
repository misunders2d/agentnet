package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Hook/CLI writes use a different Agent and changefeed from the running daemon.
// Exercise real subprocess writes: the daemon must wake after their commit.
func TestReplySessionWritesWakeDaemonAcrossProcesses(t *testing.T) {
	if payload := os.Getenv("AGENTNET_REPLY_NOTIFY_TEST_PAYLOAD"); payload != "" {
		var in struct {
			Home, Action, Result string
			Registration         ReplySessionRegistration
			Ack                  ReplyReceiverAck
		}
		raw, err := os.ReadFile(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		a, err := Open(in.Home)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		switch in.Action {
		case "register":
			owner, e := a.RegisterReplySession(in.Registration)
			if e != nil {
				t.Fatal(e)
			}
			raw, e = json.Marshal(owner)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(in.Result, raw, 0600); e != nil {
				t.Fatal(e)
			}
		case "ack":
			if ok, e := a.AckReplyReceiverInput(in.Ack); e != nil || !ok {
				t.Fatalf("ACK accepted=%v: %v", ok, e)
			}
		case "close":
			if e := a.CloseReplySession(in.Ack.ReplySessionCall); e != nil {
				t.Fatal(e)
			}
		default:
			t.Fatal("unknown fixture action")
		}
		return
	}
	w := newWorld(t, "")
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	stop, err := listenKicks(w.alice.home, w.alice.NoteChange)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	private := t.TempDir()
	payload := filepath.Join(private, "call.json")
	result := filepath.Join(private, "owner.json")
	child := func(action string, registration ReplySessionRegistration, ack ReplyReceiverAck) {
		t.Helper()
		raw, e := json.Marshal(struct {
			Home, Action, Result string
			Registration         ReplySessionRegistration
			Ack                  ReplyReceiverAck
		}{w.alice.home, action, result, registration, ack})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(payload, raw, 0600); e != nil {
			t.Fatal(e)
		}
		_, changed := w.alice.Changed()
		cmd := exec.Command(os.Args[0], "-test.run=^TestReplySessionWritesWakeDaemonAcrossProcesses$")
		cmd.Env = append(os.Environ(), "AGENTNET_REPLY_NOTIFY_TEST_PAYLOAD="+payload)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s subprocess: %v\n%s", action, e, out)
		}
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s committed without waking daemon changefeed", action)
		}
	}
	child("register", ReplySessionRegistration{Harness: "pi", SessionID: call.SessionID, File: call.File, Leaf: call.Leaf, Handle: r.Handle}, ReplyReceiverAck{})
	raw, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	call.Generation, call.OwnerToken = r.Generation, r.OwnerToken
	sessions, err := w.alice.ReplySessions()
	if err != nil || len(sessions) != 1 || !sessions[0].Active {
		t.Fatalf("registered projection: %v %v", sessions, err)
	}
	binding, input := nativeInput(t, w.alice, w.bob, r.Handle)
	d, err := w.alice.TakeReplyReceiverInput(call)
	if err != nil || d == nil {
		t.Fatalf("take: %v %v", d, err)
	}
	marker := map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "details": map[string]string{"handle": r.Handle}}
	nativeWrite(t, call.File, call.SessionID, marker, nativeEntry(d, "accepted", "marker"))
	call.Leaf = "accepted"
	ack := ReplyReceiverAck{ReplySessionCall: call, BindingID: binding, InputID: input, ClaimID: d.ClaimID, InputToken: d.InputToken}
	child("ack", ReplySessionRegistration{}, ack)
	rows := receiverBindings(t, w.alice)
	if len(rows) != 1 || len(rows[0].Inputs) != 1 || rows[0].Inputs[0].State != "accepted" {
		t.Fatalf("committed accepted projection: %+v", rows)
	}
	child("close", ReplySessionRegistration{}, ack)
	sessions, err = w.alice.ReplySessions()
	if err != nil || len(sessions) != 1 || sessions[0].Active {
		t.Fatalf("closed projection: %v %v", sessions, err)
	}
}
