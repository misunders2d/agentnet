package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestBrowserReplyReceiverRefusal(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/receiver_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "browser receiver refusal ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestLiveReplyReceiverAllowlistAndThreeSendPaths(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	bin := t.TempDir()
	writeUIHarnessStub(t, bin, "codex", "#!/bin/sh\nexit 97\n", "exit /b 97\r\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, hubDir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	invite, err := a.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), invite, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	runDaemon(t, a)
	runDaemon(t, b)
	l, remote := NewLive(a), NewLive(b)
	if _, _, err = l.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = remote.CreatePerson("Bob"); err != nil {
		t.Fatal(err)
	}
	config := client.Responder{Harness: "codex", Dir: t.TempDir()}
	if err = a.SetResponder(&config); err != nil {
		t.Fatal(err)
	}
	before, _ := a.Responder()
	record, err := a.CreateLocalAgent("Local Receiver A", config)
	if err != nil {
		t.Fatal(err)
	}
	selection := &ReplyReceiverSelection{Kind: "managed_agent", AgentID: record.ID, Instructions: "original local instruction", Mode: "question"}
	// Use the native registry API; these fixture files are not a live harness.
	nativeFile := filepath.Join(t.TempDir(), "native.jsonl")
	if err = os.WriteFile(nativeFile, []byte(`{"type":"session","id":"ui-native-fixture","version":3}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := a.RegisterReplySession(client.ReplySessionRegistration{Harness: "pi", SessionID: "ui-native-fixture", File: nativeFile, Label: "Selected native fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.CloseReplySession(client.ReplySessionCall{Handle: owner.Handle, Generation: owner.Generation, OwnerToken: owner.OwnerToken, SessionID: "ui-native-fixture", File: nativeFile}); err != nil {
		t.Fatal(err)
	}
	liveSelection := &ReplyReceiverSelection{Kind: "live_session", SessionHandle: owner.Handle}
	if got, e := l.selectedReplyReceiver(liveSelection); e != nil || got.SessionHandle != owner.Handle {
		t.Fatalf("known inactive exact selection: %+v %v", got, e)
	}
	for _, wrong := range []*ReplyReceiverSelection{
		{Kind: "human", SessionHandle: owner.Handle}, {Kind: "managed_agent", AgentID: record.ID, Instructions: "original", Mode: "question", SessionHandle: owner.Handle},
		{Kind: "live_session", SessionHandle: owner.Handle, Mode: "task"}, {Kind: "live_session", SessionHandle: owner.Handle, Instructions: "received permission"},
	} {
		if _, e := l.selectedReplyReceiver(wrong); !errors.Is(e, ErrRefused) {
			t.Fatalf("cross-kind/native shape accepted: %+v %v", wrong, e)
		}
	}
	// Every refusal occurs before consumption, including the participant path.
	fileID, err := l.StageFile("kept.txt", bytes.NewBufferString("exact file"))
	if err != nil {
		t.Fatal(err)
	}
	bad := *selection
	bad.AgentID = strings.Repeat("f", 32)
	for _, send := range []func() error{
		func() error {
			_, e := l.Send(Draft{To: b.Address, Body: "draft", Files: []string{fileID}, ReplyReceiver: &bad})
			return e
		},
		func() error {
			_, e := l.SendDM(DMDraft{Conv: "missing", Body: "draft", Files: []string{fileID}, ReplyReceiver: &bad})
			return e
		},
		func() error {
			_, e := l.AskAgent(AgentAsk{PID: "missing", Body: "draft", Files: []string{fileID}, ReplyReceiver: &bad})
			return e
		},
	} {
		if e := send(); !errors.Is(e, ErrRefused) {
			t.Fatal(e)
		}
		if _, ok := l.staged.files[fileID]; !ok {
			t.Fatal("receiver refusal consumed file")
		}
	}
	unknown := &ReplyReceiverSelection{Kind: "live_session", SessionHandle: "unknown-local-handle"}
	for _, send := range []func() error{
		func() error {
			_, e := l.Send(Draft{To: b.Address, Body: "kept native draft", Files: []string{fileID}, ReplyReceiver: unknown})
			return e
		},
		func() error {
			_, e := l.SendDM(DMDraft{Conv: "missing", Body: "kept native draft", Files: []string{fileID}, ReplyReceiver: unknown})
			return e
		},
		func() error {
			_, e := l.AskAgent(AgentAsk{PID: "missing", Body: "kept native draft", Files: []string{fileID}, ReplyReceiver: unknown})
			return e
		},
	} {
		if e := send(); !errors.Is(e, ErrRefused) {
			t.Fatal(e)
		}
		if _, ok := l.staged.files[fileID]; !ok {
			t.Fatal("unknown native handle consumed staged file")
		}
	}
	if err = a.SetLocalAgentResponder(record.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = l.selectedReplyReceiver(selection); !errors.Is(err, ErrRefused) {
		t.Fatal("disabled selection", err)
	}
	if err = a.SetLocalAgentResponder(record.ID, &config); err != nil {
		t.Fatal(err)
	}
	var conv string
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		conv, err = l.NewDM(b.Address)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	direct, err := l.Send(Draft{To: b.Address, Kind: "question", Body: "direct", ReplyReceiver: selection})
	if err != nil {
		t.Fatal(err)
	}
	dm, err := l.SendDM(DMDraft{Conv: conv, Body: "ordinary DM", ReplyReceiver: selection})
	if err != nil {
		t.Fatal(err)
	}
	participation, err := l.InviteAgent(AgentInvite{Conv: conv, Host: b.Address})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, err = remote.DecideAgent(participation.PID, true)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	var ask Sent
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		ask, err = l.AskAgent(AgentAsk{PID: participation.PID, Body: "ask", Kind: "question", ReplyReceiver: selection})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	liveDirect, err := l.Send(Draft{To: b.Address, Kind: "question", Body: "native direct", ReplyReceiver: liveSelection})
	if err != nil {
		t.Fatal(err)
	}
	liveDM, err := l.SendDM(DMDraft{Conv: conv, Body: "native DM", ReplyReceiver: liveSelection})
	if err != nil {
		t.Fatal(err)
	}
	liveAsk, err := l.AskAgent(AgentAsk{PID: participation.PID, Kind: "question", Body: "native ask", ReplyReceiver: liveSelection})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := l.ReplyReceiverBindings()
	if err != nil || len(bindings) != 6 {
		t.Fatalf("bindings %+v %v", bindings, err)
	}
	refs := map[string]bool{direct.ID: true, liveDirect.ID: true}
	thread, err := l.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range thread.Messages {
		if m.ID == dm.ID || m.ID == ask.ID || m.ID == liveDM.ID || m.ID == liveAsk.ID {
			refs[m.ID] = true
			if m.LID != "" {
				refs[m.LID] = true
			}
		}
	}
	for _, binding := range bindings {
		expected, label := selection, record.Label
		if binding.Receiver.Kind == "live_session" {
			expected, label = liveSelection, "Selected native fixture (pi)"
			if binding.State != "pending" {
				t.Fatalf("inactive session silently handled %+v", binding)
			}
		}
		if !reflect.DeepEqual(binding.Receiver, *expected) || binding.Host != a.Address || binding.Label != label || !refs[binding.RequestRef] {
			t.Fatalf("mapping %+v", binding)
		}
	}
	after, _ := a.Responder()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("default changed")
	}
	s := New(l, "127.0.0.1:8123", testToken)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/send", s.send)
	mux.HandleFunc("POST /api/dm/send", s.sendDM)
	mux.HandleFunc("POST /api/dm/agent/ask", s.askAgent)
	mux.HandleFunc("GET /api/reply-receivers", s.replyReceivers)
	mux.HandleFunc("GET /api/reply-sessions", s.replySessions)
	h := s.guard(mux)
	for _, route := range []string{"/api/send", "/api/dm/send", "/api/dm/agent/ask"} {
		for _, field := range []string{`"preset":"task"`, `"binding_id":"arbitrary"`, `"owner_token":"arbitrary"`, `"generation":7`, `"session_id":"native"`, `"file":"/private/native"`, `"executor":{}`} {
			r := httptest.NewRequest("POST", "http://127.0.0.1:8123"+route, strings.NewReader(`{"reply_receiver":{"kind":"human",`+field+`}}`))
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
			r.Header.Set("Origin", "http://127.0.0.1:8123")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("allowlist %s %s: %d", route, field, w.Code)
			}
		}
	}
	for _, authed := range []bool{false, true} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8123/api/reply-receivers", nil)
		if authed {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !authed {
			if w.Code != 401 {
				t.Fatal(w.Code)
			}
			continue
		}
		var got []ReplyReceiverBindingView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 6 || strings.Contains(w.Body.String(), "preset") || strings.Contains(w.Body.String(), "executor") {
			t.Fatalf("read view %d %s", w.Code, w.Body)
		}
	}
	for _, authed := range []bool{false, true} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8123/api/reply-sessions", nil)
		if authed {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !authed {
			if w.Code != 401 {
				t.Fatal(w.Code)
			}
			continue
		}
		var catalog ReplySessionCatalogView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || catalog.Host != a.Address || !catalog.Local || len(catalog.Sessions) != 1 || catalog.Sessions[0].Handle != owner.Handle || catalog.Sessions[0].Active {
			t.Fatalf("safe inactive catalog %d %s", w.Code, w.Body)
		}
		var raw map[string]json.RawMessage
		json.Unmarshal(w.Body.Bytes(), &raw)
		var rows []map[string]json.RawMessage
		json.Unmarshal(raw["sessions"], &rows)
		if len(raw) != 3 || len(rows[0]) != 4 {
			t.Fatal("safe projection expanded", w.Body.String())
		}
		for _, private := range []string{"owner_token", "generation", "session_id", nativeFile, owner.OwnerToken} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("private native detail exposed")
			}
		}
	}

}
