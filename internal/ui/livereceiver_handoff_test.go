package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestLiveClosedReplyReceiverPreflightAndProjection(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	bin := t.TempDir()
	if e := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 97\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	a, e := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, hubDir), "laptop")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	invite, e := a.Invite(ctx, "bob", time.Hour, false)
	if e != nil {
		t.Fatal(e)
	}
	b, e := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), invite, "desk")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { b.Close() })
	runDaemon(t, a)
	runDaemon(t, b)
	l := NewLive(a)
	config := client.Responder{Harness: "codex", Dir: t.TempDir()}
	record, e := a.CreateLocalAgent("Chosen backup", config)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "native.jsonl")
	if e = os.WriteFile(file, []byte(`{"type":"session","id":"ui-backup-fixture","version":3}`+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	owner, e := a.RegisterReplySession(client.ReplySessionRegistration{Harness: "pi", SessionID: "ui-backup-fixture", File: file, Label: "Exact native fixture"})
	if e != nil {
		t.Fatal(e)
	}
	backup := client.ManagedReplyHandoff{AgentID: record.ID, Instructions: " original local backup instruction ", Mode: "question"}
	selection := ReplyReceiverSelection{Kind: "live_session", SessionHandle: owner.Handle, OnClose: &backup}
	valid, e := l.selectedReplyReceiver(&selection)
	if e != nil || valid.OnClose == nil || valid.OnClose.Instructions != "original local backup instruction" {
		t.Fatalf("valid %+v %v", valid, e)
	}
	staged, e := l.StageFile("kept.txt", bytes.NewBufferString("EXACT BACKUP FILE"))
	if e != nil {
		t.Fatal(e)
	}
	refuse := func(d *ReplyReceiverSelection) {
		t.Helper()
		for _, send := range []func() error{
			func() error {
				_, e := l.Send(Draft{To: b.Address, Body: "kept", Files: []string{staged}, ReplyReceiver: d})
				return e
			},
			func() error {
				_, e := l.SendDM(DMDraft{Conv: "missing", Body: "kept", Files: []string{staged}, ReplyReceiver: d})
				return e
			},
			func() error {
				_, e := l.AskAgent(AgentAsk{PID: "missing", Body: "kept", Files: []string{staged}, ReplyReceiver: d})
				return e
			},
		} {
			if e := send(); !errors.Is(e, ErrRefused) {
				t.Fatalf("invalid backup %+v: %v", d, e)
			}
			if _, ok := l.staged.files[staged]; !ok {
				t.Fatal("backup refusal consumed staged bytes")
			}
		}
	}
	for _, bad := range []client.ManagedReplyHandoff{
		{AgentID: strings.Repeat("f", 32), Instructions: "original", Mode: "question"},
		{AgentID: record.ID, Instructions: "", Mode: "question"},
		{AgentID: record.ID, Instructions: strings.Repeat("x", 4097), Mode: "question"},
		{AgentID: record.ID, Instructions: "original", Mode: "message"},
	} {
		d := selection
		d.OnClose = &bad
		refuse(&d)
	}
	for _, kind := range []string{"human", "managed_agent"} {
		d := selection
		d.Kind = kind
		refuse(&d)
	}
	if e = a.SetLocalAgentResponder(record.ID, nil); e != nil {
		t.Fatal(e)
	}
	refuse(&selection)
	if e = a.SetLocalAgentResponder(record.ID, &config); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{`"preset":"task"`, `"origin_session":"fake"`, `"closure_generation":4`, `"executor":{}`, `"owner_token":"fake"`, `"file":"/native"`} {
		var d ReplyReceiverSelection
		if e = json.Unmarshal([]byte(`{"kind":"live_session","session_handle":"known","on_close":{"agent_id":"`+record.ID+`","instructions":"local","mode":"question",`+field+`}}`), &d); e == nil {
			t.Fatal("nested private field accepted", field)
		}
	}
	selection.OnClose = &client.ManagedReplyHandoff{AgentID: record.ID, Instructions: "original local backup instruction", Mode: "question"}
	sent, e := l.Send(Draft{To: b.Address, Kind: "question", Body: "BACKUP ORIGINAL", Files: []string{staged}, ReplyReceiver: &selection})
	if e != nil {
		t.Fatal(e)
	}
	rows, e := l.ReplyReceiverBindings()
	if e != nil || len(rows) != 1 {
		t.Fatalf("projection %+v %v", rows, e)
	}
	row := rows[0]
	if row.RequestRef != sent.ID || row.HandoffState != "preauthorized" || row.HandoffLabel != record.Label || row.Receiver.OnClose == nil || *row.Receiver.OnClose != *selection.OnClose {
		t.Fatalf("preauthorization %+v", row)
	}
	// Native snapshot owns post-send changes. It holds the original selection,
	// and UI only projects the resulting truth without reconciling from a read.
	changed := config
	changed.Dir = t.TempDir()
	if e = a.SetLocalAgentResponder(record.ID, &changed); e != nil {
		t.Fatal(e)
	}
	if e = a.CloseReplySession(client.ReplySessionCall{Handle: owner.Handle, Generation: owner.Generation, OwnerToken: owner.OwnerToken, SessionID: "ui-backup-fixture", File: file, CloseReason: "shutdown"}); e != nil {
		t.Fatal(e)
	}
	rows, e = l.ReplyReceiverBindings()
	if e != nil || rows[0].HandoffState != "held" || rows[0].Receiver.Kind != "live_session" || !strings.Contains(rows[0].Detail, "no safe automatic handoff") {
		t.Fatalf("changed snapshot %+v %v", rows, e)
	}
	raw, e := json.Marshal(rows)
	if e != nil {
		t.Fatal(e)
	}
	for _, private := range []string{`"preset":`, `"executor":`, `"origin_session":`, `"closure_generation":`, `"owner_token":`, owner.OwnerToken, file, config.Dir} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("projection leaked %s", private)
		}
	}
	again, e := l.ReplyReceiverBindings()
	if e != nil || again[0].HandoffState != "held" || again[0].Receiver.OnClose.AgentID != record.ID {
		t.Fatalf("read changed authority %+v %v", again, e)
	}
}
