package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// desktopChatsFixture lists four chats; connecting is slow (to show the
// pending state) and refuses one chat (to show the failure and retry).
type desktopChatsFixture struct {
	*Fixture
	delay time.Duration
	mu    sync.Mutex
	calls []DesktopChatConnect
}

func (f *desktopChatsFixture) DesktopChats(context.Context) (DesktopChatsView, error) {
	return DesktopChatsView{Supported: true, Chats: []DesktopChat{
		{Kind: "claude", ID: "sess-ok", Dir: "/home/alice/work/site", Ready: true},
		{Kind: "codex", ID: "thread-fail", Name: "Release notes", Dir: "/home/alice/notes", Ready: true, Updated: 1},
		{Kind: "claude", ID: "sess-closed", Dir: "/home/alice/old", Ready: false},
		{Kind: "codex", ID: "thread-done", Name: "Docs", Dir: "/home/alice/docs", Ready: true, AgentID: strings.Repeat("d", 32), AgentLabel: "Docs helper"},
	}}, nil
}

func (f *desktopChatsFixture) ConnectDesktopChat(ctx context.Context, c DesktopChatConnect) (AgentCatalogChangeResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return AgentCatalogChangeResult{}, ctx.Err()
	}
	if c.ID == "thread-fail" {
		return AgentCatalogChangeResult{}, Refuse("Codex Desktop is not running on this computer.")
	}
	return AgentCatalogChangeResult{Saved: true, Published: true, Note: "Connected."}, nil
}

func TestDesktopChatsRoutesGuarded(t *testing.T) {
	f := &desktopChatsFixture{Fixture: NewFixture(time.Now)}
	h := New(f, "127.0.0.1:12345", testToken).Handler()
	call := func(method, body, origin, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:12345/api/desktop-chats", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookieName+"="+cookie)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	if r := call("GET", "", "", ""); r.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", r.Code)
	}
	if r := call("POST", `{"kind":"claude","id":"sess-ok","label":"x"}`, "https://foreign.example", testToken); r.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", r.Code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a refused request reached the provider: %v", f.calls)
	}
	r := call("GET", "", "", testToken)
	var v DesktopChatsView
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &v) != nil || !v.Supported || len(v.Chats) != 4 {
		t.Fatalf("list: %d %s", r.Code, r.Body)
	}
	r = call("POST", `{"kind":"claude","id":"sess-ok","label":"Site"}`, "http://127.0.0.1:12345", testToken)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"saved":true`) || len(f.calls) != 1 {
		t.Fatalf("connect: %d %s", r.Code, r.Body)
	}
	// A provider without desktop chats answers not found, never a guess.
	plain := New(NewFixture(time.Now), "127.0.0.1:12345", testToken).Handler()
	req := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/desktop-chats", nil)
	req.Header.Set("Cookie", cookieName+"="+testToken)
	res := httptest.NewRecorder()
	plain.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("no provider: %d", res.Code)
	}
}

// The live provider lists a Codex Desktop chat from CODEX_HOME, refuses a
// missing name or unknown kind, and reports the client's refusal (here: no
// running Codex Desktop) without creating an agent.
func TestLiveDesktopChatsListAndRefuse(t *testing.T) {
	if runtime.GOOS != "linux" {
		l := &Live{timeout: time.Second}
		v, err := l.DesktopChats(context.Background())
		if err != nil || v.Supported || v.Note != desktopChatsLinuxOnly {
			t.Fatalf("off Linux: %+v %v", v, err)
		}
		return
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	thread := "01a21849-d9de-7310-9cf5-f5e06640cecd"
	dir := filepath.Join(codexHome, "sessions", "2026", "11", "26")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": thread, "cwd": codexHome, "originator": "Codex Desktop", "cli_version": "0.171.0", "source": "vscode"}})
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-26T14-37-02-"+thread+".jsonl"), append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, hubDir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	l := NewLive(a)
	v, err := l.DesktopChats(ctx)
	if err != nil || !v.Supported {
		t.Fatalf("list: %+v %v", v, err)
	}
	var found bool
	for _, c := range v.Chats {
		if c.Kind == "codex" && c.ID == thread {
			found = c.Ready && c.AgentID == ""
		}
	}
	if !found {
		t.Fatalf("the Codex Desktop chat is not offered: %+v", v.Chats)
	}
	for _, bad := range []DesktopChatConnect{{Kind: "codex", ID: thread}, {Kind: "other", ID: thread, Label: "x"}, {Kind: "codex", Label: "x"}} {
		if _, err := l.ConnectDesktopChat(ctx, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := l.ConnectDesktopChat(ctx, DesktopChatConnect{Kind: "codex", ID: thread, Label: "Notes"}); err == nil {
		t.Fatal("connected without a running Codex Desktop")
	}
	agents, err := a.LocalAgents()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range agents {
		if e.Responder != nil && e.Responder.CodexChat != nil {
			t.Fatalf("a refused connect created an agent: %+v", e.Record)
		}
	}
}

func TestDesktopChatsConnectRendered(t *testing.T) {
	if testing.Short() {
		t.Skip("rendered")
	}
	f := &desktopChatsFixture{Fixture: NewFixture(time.Now), delay: 1500 * time.Millisecond}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	t.Setenv("AGENTNET_SCREENSHOTS", privateScreenshots(t))
	out := browserCheck(t, "testdata/desktopchats_rendered.cjs", ts.URL+"/?t="+testToken)
	if !strings.Contains(out, "desktop chats rendered PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var got []string
	for _, c := range f.calls {
		got = append(got, c.Kind+":"+c.ID+":"+c.Label)
	}
	// Two light/dark passes, each: one success and one refused attempt. Cancel,
	// an empty name and the disabled closed chat never reach the provider.
	want := "claude:sess-ok:Site agent,codex:thread-fail:Release notes"
	if strings.Join(got, ",") != want+","+want {
		t.Fatalf("connect calls %v", got)
	}
	t.Logf("%s", out)
}
